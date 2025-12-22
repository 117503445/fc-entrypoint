package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"sync/atomic"

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
	processes     = make(map[int64]*Process)
	processesMu   sync.RWMutex
	nextProcessID int64
)

func main() {
	goutils.InitZeroLog()

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

	id := atomic.AddInt64(&nextProcessID, 1)

	process := &Process{
		ID:         id,
		Command:    req.Command,
		WorkingDir: req.WorkingDir,
		Status:     "running",
	}

	processesMu.Lock()
	processes[id] = process
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

	processList := make([]*Process, 0, len(processes))
	for _, p := range processes {
		processList = append(processList, p)
	}

	json.NewEncoder(w).Encode(processList)
}

func executeProcess(process *Process) {
	cmd := exec.Command("sh", "-c", process.Command)
	if process.WorkingDir != "" {
		cmd.Dir = process.WorkingDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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

func reverseProxyHandler(w http.ResponseWriter, r *http.Request) {
	// 构建目标URL
	targetURL := "http://localhost:8000" + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
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

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// 设置状态码
	w.WriteHeader(resp.StatusCode)

	// 复制响应体
	io.Copy(w, resp.Body)
}
