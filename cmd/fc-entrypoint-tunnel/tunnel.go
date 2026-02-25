package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"github.com/rs/zerolog/log"
)

// RunTunnel runs the tunnel client
func RunTunnel(cmd *CmdTunnel) error {
	ctx := context.Background()
	ctx = log.Logger.WithContext(ctx)

	// Find available port
	port := cmd.LocalPort
	if cmd.AutoSelectPort {
		var err error
		port, err = findAvailablePort(cmd.LocalPort)
		if err != nil {
			return fmt.Errorf("failed to find available port: %w", err)
		}
		if port != cmd.LocalPort {
			log.Ctx(ctx).Info().Int("original", cmd.LocalPort).Int("selected", port).Msg("Port was occupied, selected new port")
		}
	}

	// Build WebSocket URL
	wsURL, err := buildWebSocketURL(cmd.RemoteHost)
	if err != nil {
		return fmt.Errorf("failed to build WebSocket URL: %w", err)
	}

	log.Ctx(ctx).Info().Str("remote", wsURL).Int("localPort", port).Msg("Starting SSH tunnel")

	// Start local listener
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}
	defer listener.Close()

	log.Ctx(ctx).Info().Int("port", port).Msg("Listening for SSH connections")
	fmt.Printf("SSH tunnel is ready. Connect with: ssh -p %d root@127.0.0.1\n", port)

	// Accept connections
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("Failed to accept connection")
			continue
		}

		go handleConnection(ctx, conn, wsURL)
	}
}

// findAvailablePort finds an available port starting from the given port
func findAvailablePort(startPort int) (int, error) {
	for port := startPort; port < 65535; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found starting from %d", startPort)
}

// buildWebSocketURL builds the WebSocket URL from the remote host
func buildWebSocketURL(remoteHost string) (string, error) {
	u, err := url.Parse(remoteHost)
	if err != nil {
		return "", err
	}

	// Convert http(s) to ws(s)
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "wss", "ws":
		// Already WebSocket scheme
	default:
		// Assume https if no scheme
		u.Scheme = "wss"
	}

	// Ensure path ends with /_entrypoint/ssh
	u.Path = strings.TrimSuffix(u.Path, "/") + "/_entrypoint/ssh"

	return u.String(), nil
}

// handleConnection handles a single SSH connection
func handleConnection(ctx context.Context, localConn net.Conn, wsURL string) {
	defer localConn.Close()

	log.Ctx(ctx).Info().Str("remote", localConn.RemoteAddr().String()).Msg("New connection accepted")

	// Connect to remote WebSocket
	wsConn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to connect to remote WebSocket")
		return
	}
	defer wsConn.CloseNow()

	log.Ctx(ctx).Info().Msg("Connected to remote server, starting bidirectional proxy")

	// Create a context for managing goroutines
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Proxy local -> WebSocket
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := localConn.Read(buf)
			if err != nil {
				if err != io.EOF && proxyCtx.Err() == nil {
					log.Ctx(ctx).Debug().Err(err).Msg("Local read error")
				}
				return
			}
			if err := wsConn.Write(proxyCtx, websocket.MessageBinary, buf[:n]); err != nil {
				if proxyCtx.Err() == nil {
					log.Ctx(ctx).Debug().Err(err).Msg("WebSocket write error")
				}
				return
			}
		}
	}()

	// Proxy WebSocket -> local
	for {
		msgType, data, err := wsConn.Read(proxyCtx)
		if err != nil {
			if proxyCtx.Err() == nil {
				log.Ctx(ctx).Debug().Err(err).Msg("WebSocket read error")
			}
			return
		}
		if msgType == websocket.MessageBinary {
			if _, err := localConn.Write(data); err != nil {
				log.Ctx(ctx).Debug().Err(err).Msg("Local write error")
				return
			}
		}
	}
}
