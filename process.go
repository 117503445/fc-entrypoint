package main

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"github.com/rs/zerolog/log"
)

var (
	processes   []*Process
	processesMu sync.RWMutex
)

// createProcess 创建并启动一个新进程
func createProcess(command, workingDir string) int64 {
	processesMu.Lock()
	id := int64(len(processes) + 1)
	process := &Process{
		ID:         id,
		Command:    command,
		WorkingDir: workingDir,
		Status:     "running",
	}
	processes = append(processes, process)
	processesMu.Unlock()

	// 异步执行命令
	go executeProcess(process)

	return id
}

func startEntrypointProcess(entrypointPath string) {
	id := createProcess(entrypointPath, "/")
	log.Info().Int64("process_id", id).Str("path", entrypointPath).Msg("Started entrypoint process")
}

func executeProcess(process *Process) {
	cmd := exec.Command("sh", "-c", process.Command)
	if process.WorkingDir != "" {
		cmd.Dir = process.WorkingDir
	}

	var stdout, stderr bytes.Buffer

	// 创建同时写入缓冲区和日志的writer
	stdoutWriter := io.MultiWriter(&stdout, &logWriter{prefix: fmt.Sprintf("[process:%d:stdout] ", process.ID), level: "info"})
	stderrWriter := io.MultiWriter(&stderr, &logWriter{prefix: fmt.Sprintf("[process:%d:stderr] ", process.ID), level: "error"})

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	// 执行命令
	err := cmd.Run()

	processesMu.Lock()
	defer processesMu.Unlock()

	process.Output = stdout.String()
	process.Error = stderr.String()

	if err != nil {
		process.Status = "failed"
		log.Error().Err(err).Int64("process_id", process.ID).Msg("Process failed")
	} else {
		process.Status = "completed"
		log.Info().Int64("process_id", process.ID).Msg("Process completed")
	}
}
