package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"
)

var (
	processes      []*Process
	processesMu    sync.RWMutex
	processCounter int64
)

// getDirData returns the data directory from DIR_DATA env or default "./data"
func getDirData() string {
	dir := os.Getenv("DIR_DATA")
	if dir == "" {
		dir = "./data"
	}
	return dir
}

// shouldCleanup returns whether to cleanup process directories after execution
func shouldCleanup() bool {
	cleanup := os.Getenv("CLEANUP_PROCESS_DIR")
	// Default to true if not set
	return cleanup != "false"
}

// createProcess creates and starts a new process
func createProcess(ctx context.Context, req ProcessRequest) string {
	processesMu.Lock()
	processCounter++
	id := fmt.Sprintf("%s_%d", GetInstanceID(), processCounter)
	process := &Process{
		ID:         id,
		Command:    req.Command,
		WorkingDir: req.WorkingDir,
		Image:      req.Image,
		Status:     "running",
	}
	processes = append(processes, process)
	processesMu.Unlock()

	// Execute command asynchronously
	go executeProcess(ctx, process, req.ImageUsername, req.ImagePassword)

	return id
}

func startEntrypointProcess(entrypointPath string) {
	ctx := context.Background()
	ctx = log.Logger.WithContext(ctx)
	id := createProcess(ctx, ProcessRequest{Command: entrypointPath, WorkingDir: "/"})
	log.Ctx(ctx).Info().Str("process_id", id).Str("path", entrypointPath).Msg("Started entrypoint process")
}

func executeProcess(ctx context.Context, process *Process, username, password string) {
	var stdout, stderr bytes.Buffer
	var err error

	if process.Image != "" {
		// Execute in container image environment
		err = executeWithImage(ctx, process, username, password, &stdout, &stderr)
	} else {
		// Execute directly (original logic)
		err = executeDirectly(ctx, process, &stdout, &stderr)
	}

	processesMu.Lock()
	defer processesMu.Unlock()

	process.Output = stdout.String()
	process.Error = stderr.String()

	if err != nil {
		process.Status = "failed"
		log.Ctx(ctx).Error().Err(err).Str("process_id", process.ID).Msg("Process failed")
	} else {
		process.Status = "completed"
		log.Ctx(ctx).Info().Str("process_id", process.ID).Msg("Process completed")
	}
}

// executeDirectly executes command directly without container isolation
func executeDirectly(ctx context.Context, process *Process, stdout, stderr *bytes.Buffer) error {
	cmd := exec.Command("sh", "-c", process.Command)
	if process.WorkingDir != "" {
		cmd.Dir = process.WorkingDir
	}

	// Create writers that write to both buffer and log
	stdoutWriter := io.MultiWriter(stdout, &logWriter{prefix: fmt.Sprintf("[process:%s:stdout] ", process.ID), level: "info"})
	stderrWriter := io.MultiWriter(stderr, &logWriter{prefix: fmt.Sprintf("[process:%s:stderr] ", process.ID), level: "error"})

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	return cmd.Run()
}

// executeWithImage executes command in a container image environment
func executeWithImage(ctx context.Context, process *Process, username, password string, stdout, stderr *bytes.Buffer) error {
	// Prepare image (pull if needed)
	rootfsTar, err := PrepareImage(ctx, process.Image, username, password)
	if err != nil {
		return fmt.Errorf("failed to prepare image: %w", err)
	}

	process.RootfsPath = rootfsTar

	// Create process directory
	processDir := filepath.Join(getDirData(), "processes", process.ID)

	// Create writers that write to both buffer and log
	stdoutWriter := io.MultiWriter(stdout, &logWriter{prefix: fmt.Sprintf("[process:%s:stdout] ", process.ID), level: "info"})
	stderrWriter := io.MultiWriter(stderr, &logWriter{prefix: fmt.Sprintf("[process:%s:stderr] ", process.ID), level: "error"})

	// Execute in chroot
	err = ExecuteInChroot(ctx, rootfsTar, processDir, process.Command, process.WorkingDir, stdoutWriter, stderrWriter)

	// Cleanup if configured
	if shouldCleanup() {
		if removeErr := os.RemoveAll(processDir); removeErr != nil {
			log.Ctx(ctx).Warn().Err(removeErr).Str("path", processDir).Msg("Failed to cleanup process directory")
		}
	}

	return err
}