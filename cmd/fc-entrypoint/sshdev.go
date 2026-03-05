package main

import (
	"context"
	"path/filepath"

	"github.com/117503445/sshdev/pkg/sshlib"
	"github.com/rs/zerolog/log"
)

// StartSSHDev starts the sshdev server on port 2222
func StartSSHDev(ctx context.Context, dataDir string) error {
	// Create host key path
	hostKeyPath := filepath.Join(dataDir, "sshdev_host_key")

	// Create config
	cfg := &sshlib.Config{
		ListenAddr:     ":2222",
		HostKeyPath:    hostKeyPath,
		AuthMode:       sshlib.AuthModePassword,
		Username:       "root",
		Password:       "123456",
		Shell:          "/bin/bash",
		AuthorizedKeys: "",
	}

	// Create SSH server
	srv, err := sshlib.NewServer(cfg)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("Failed to create SSH server")
		return err
	}

	// Start SSH server in background
	go func() {
		log.Ctx(ctx).Info().Str("port", "2222").Msg("Starting SSH dev server")
		if err := srv.Start(); err != nil {
			log.Ctx(ctx).Error().Err(err).Msg("SSH server stopped")
		}
	}()

	return nil
}
