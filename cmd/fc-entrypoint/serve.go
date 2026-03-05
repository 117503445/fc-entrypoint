package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/rs/zerolog/log"
)

// RunServe runs the entrypoint server
func RunServe(cmd *CmdServe) error {
	ctx := context.Background()
	ctx = log.Logger.WithContext(ctx)

	// Set global variables from command options
	os.Setenv("SKIP_WAIT_FOR_PORT_8000", boolToEnv(cmd.SkipWaitForPort8000))
	os.Setenv("ENTRYPOINT_SCRIPT", cmd.EntrypointScriptPath)
	LOG_BODY = cmd.LogBody
	os.Setenv("DIR_DATA", cmd.DirData)
	os.Setenv("CLEANUP_PROCESS_DIR", boolToEnv(cmd.CleanupProcessDir))

	// Start SSH dev server on port 2222
	if err := StartSSHDev(ctx, cmd.DirData); err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("Failed to start SSH dev server, continuing anyway")
	}

	if cmd.SkipWaitForPort8000 {
		go setupAndStartServer(ctx, cmd.Port)
	} else {
		go waitForPort8000AndStartServer(ctx, cmd.Port)
	}

	// Check and start entrypoint.sh process
	if _, err := os.Stat(cmd.EntrypointScriptPath); err == nil {
		log.Ctx(ctx).Info().Str("path", cmd.EntrypointScriptPath).Msg("Found entrypoint script, starting as process")
		go startEntrypointProcess(ctx, cmd.EntrypointScriptPath)
	} else {
		log.Ctx(ctx).Info().Str("path", cmd.EntrypointScriptPath).Msg("Entrypoint script not found, skipping")
	}

	select {}
}

func boolToEnv(b bool) string {
	if b {
		return ""
	}
	return "false"
}

// checkPort checks if the specified port is being listened to
func checkPort(ctx context.Context, port int) bool {
	log.Ctx(ctx).Info().Msg("Checking port 8000 is available...")

	address := fmt.Sprintf("localhost:%d", port)
	conn, err := net.DialTimeout("tcp", address, time.Second*1)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// setupAndStartServer sets up routing and starts the server on the specified port
func setupAndStartServer(ctx context.Context, port string) {
	// Set up routes
	http.HandleFunc("/_entrypoint/processes", func(w http.ResponseWriter, r *http.Request) {
		handleProcesses(ctx, w, r)
	})
	http.HandleFunc("/_entrypoint/processes/", func(w http.ResponseWriter, r *http.Request) {
		handleProcesses(ctx, w, r)
	})

	// WebSocket to SSH proxy endpoint
	http.HandleFunc("/_entrypoint/ssh", func(w http.ResponseWriter, r *http.Request) {
		handleSSHWebSocket(ctx, w, r)
	})

	// All other paths are proxied to localhost:8000
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reverseProxyHandler(ctx, w, r)
	})

	log.Ctx(ctx).Info().Str("port", port).Msg("Starting server")
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("Server failed to start")
	}
}

// waitForPort8000AndStartServer waits for port 8000 to be listened to, then starts the server on the specified port
func waitForPort8000AndStartServer(ctx context.Context, port string) {
	for {
		if checkPort(ctx, 8000) {
			log.Ctx(ctx).Info().Msg("Port 8000 is now available, starting server")
			break
		}
		time.Sleep(time.Second)
	}

	setupAndStartServer(ctx, port)
}

func handleProcesses(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodPost:
		handleCreateProcess(ctx, w, r)
	case http.MethodGet:
		handleListProcesses(ctx, w, r)
	default:
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func handleCreateProcess(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var req ProcessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	if req.Command == "" {
		http.Error(w, `{"error": "command field is required"}`, http.StatusBadRequest)
		return
	}

	id := createProcess(ctx, req)
	response := map[string]string{"id": id}
	json.NewEncoder(w).Encode(response)
}

func handleListProcesses(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	processesMu.RLock()
	defer processesMu.RUnlock()

	processList := make([]*Process, len(processes))
	copy(processList, processes)

	json.NewEncoder(w).Encode(processList)
}

// handleSSHWebSocket handles WebSocket connections and proxies them to local SSH port 2222
func handleSSHWebSocket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	// Accept WebSocket connection
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to accept WebSocket connection")
		return
	}
	defer wsConn.CloseNow()

	log.Ctx(ctx).Info().Msg("WebSocket connection accepted, connecting to SSH server")

	// Connect to local SSH server on port 2222
	sshConn, err := net.Dial("tcp", "localhost:2222")
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to connect to SSH server")
		wsConn.Close(websocket.StatusInternalError, "Failed to connect to SSH server")
		return
	}
	defer sshConn.Close()

	log.Ctx(ctx).Info().Msg("Connected to SSH server, starting bidirectional proxy")

	// Create a context for managing goroutines
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Proxy WebSocket -> SSH
	go func() {
		defer cancel()
		for {
			msgType, data, err := wsConn.Read(proxyCtx)
			if err != nil {
				if proxyCtx.Err() == nil {
					log.Ctx(ctx).Debug().Err(err).Msg("WebSocket read error")
				}
				return
			}
			if msgType == websocket.MessageBinary {
				if _, err := sshConn.Write(data); err != nil {
					log.Ctx(ctx).Debug().Err(err).Msg("SSH write error")
					return
				}
			}
		}
	}()

	// Proxy SSH -> WebSocket
	buf := make([]byte, 32*1024)
	for {
		n, err := sshConn.Read(buf)
		if err != nil {
			if err != io.EOF && proxyCtx.Err() == nil {
				log.Ctx(ctx).Debug().Err(err).Msg("SSH read error")
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
}
