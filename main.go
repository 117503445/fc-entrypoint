package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/117503445/goutils"
	"github.com/rs/zerolog/log"
)

type ProcessRequest struct {
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
}

type Process struct {
	ID         int64  `json:"id"`
	Command    string `json:"command"`
	WorkingDir string `json:"working_dir"`
	Status     string `json:"status"` // running, completed, failed
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
}

var (
	processes   []*Process
	processesMu sync.RWMutex
	LOG_BODY    = true
)

func main() {
	goutils.InitZeroLog()

	// 检查并启动 entrypoint.sh 进程
	if _, err := os.Stat("/entrypoint.sh"); err == nil {
		log.Info().Msg("Found /entrypoint.sh, starting as process")
		startEntrypointProcess()
	} else {
		log.Info().Msg("/entrypoint.sh not found, skipping")
	}

	// 设置路由
	http.HandleFunc("/_entrypoint/processes", handleProcesses)
	http.HandleFunc("/_entrypoint/processes/", handleProcesses)

	// 其他所有路径转发到 localhost:8000
	http.HandleFunc("/", reverseProxyHandler)

	log.Info().Msg("Starting server on :9000")
	if err := http.ListenAndServe(":9000", nil); err != nil {
		log.Fatal().Err(err).Msg("Server failed to start")
	}
}

func startEntrypointProcess() {
	processesMu.Lock()
	id := int64(len(processes) + 1)
	process := &Process{
		ID:         id,
		Command:    "/entrypoint.sh",
		WorkingDir: "/",
		Status:     "running",
	}
	processes = append(processes, process)
	processesMu.Unlock()

	// 异步执行命令
	go executeProcess(process)

	log.Info().Int64("process_id", id).Msg("Started entrypoint process")
}

func handleProcesses(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodPost:
		handleCreateProcess(w, r)
	case http.MethodGet:
		handleListProcesses(w, r)
	default:
		http.Error(w, `{"error": "Method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func handleCreateProcess(w http.ResponseWriter, r *http.Request) {
	var req ProcessRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error": "Invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	if req.Command == "" {
		http.Error(w, `{"error": "command field is required"}`, http.StatusBadRequest)
		return
	}

	processesMu.Lock()
	id := int64(len(processes) + 1)
	process := &Process{
		ID:         id,
		Command:    req.Command,
		WorkingDir: req.WorkingDir,
		Status:     "running",
	}
	processes = append(processes, process)
	processesMu.Unlock()

	// 异步执行命令
	go executeProcess(process)

	response := map[string]int64{"id": id}
	json.NewEncoder(w).Encode(response)
}

func handleListProcesses(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	processesMu.RLock()
	defer processesMu.RUnlock()

	processList := make([]*Process, len(processes))
	copy(processList, processes)

	json.NewEncoder(w).Encode(processList)
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

// logWriter 是一个自定义的writer，用于实时输出进程日志
type logWriter struct {
	prefix string
	level  string
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	// 移除末尾的换行符，避免日志重复换行
	line := strings.TrimRight(string(p), "\n")
	if line == "" {
		return len(p), nil
	}

	switch w.level {
	case "info":
		log.Info().Str("output", w.prefix+line).Msg("Process output")
	case "error":
		log.Error().Str("output", w.prefix+line).Msg("Process error")
	default:
		log.Info().Str("output", w.prefix+line).Msg("Process output")
	}

	return len(p), nil
}

func reverseProxyHandler(w http.ResponseWriter, r *http.Request) {
	// 构建目标URL
	targetURL := "http://localhost:8000" + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// 如果启用日志，读取并打印请求body
	var reqBodyBytes []byte
	var err error
	if LOG_BODY && r.Body != nil {
		reqBodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read request body for logging")
		} else {
			log.Info().Str("request_body", string(reqBodyBytes)).Msg("Forwarding request body")
		}
		r.Body = io.NopCloser(bytes.NewReader(reqBodyBytes))
	}

	// 创建新的请求
	req, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "Failed to create request", http.StatusInternalServerError)
		return
	}

	// 复制请求头
	for key, values := range r.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 检查是否为流式响应
	contentType := resp.Header.Get("Content-Type")
	isStreaming := contentType == "text/event-stream" ||
		contentType == "application/x-ndjson" ||
		resp.Header.Get("Transfer-Encoding") == "chunked" ||
		strings.Contains(resp.Header.Get("Cache-Control"), "no-cache")

	// 如果启用日志且不是流式响应，读取并打印响应body
	var respBodyBytes []byte
	if LOG_BODY && !isStreaming {
		respBodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Error().Err(err).Msg("Failed to read response body for logging")
		} else {
			log.Info().Str("response_body", string(respBodyBytes)).Msg("Received response body")
		}
		resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
	}

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// 设置状态码
	w.WriteHeader(resp.StatusCode)

	// 复制响应体
	if isStreaming {
		// 对于流式响应，使用流式复制以支持实时数据传输
		log.Debug().Str("content_type", contentType).Msg("Streaming response detected")
		io.Copy(w, resp.Body)
	} else {
		// 对于普通响应，直接复制
		io.Copy(w, resp.Body)
	}
}
