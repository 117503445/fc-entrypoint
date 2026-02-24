package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// checkPort 检查指定端口是否被监听
func checkPort(port int) bool {
	log.Info().Msg("Checking port 8000 is available...")

	address := fmt.Sprintf("localhost:%d", port)
	conn, err := net.DialTimeout("tcp", address, time.Second*1)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// setupAndStartServer 设置路由并启动9000端口服务
func setupAndStartServer() {
	// 设置路由
	http.HandleFunc("/_entrypoint/processes", handleProcesses)
	http.HandleFunc("/_entrypoint/processes/", handleProcesses)

	// 其他所有路径转发到 localhost:8000
	http.HandleFunc("/", reverseProxyHandler)

	log.Info().Msg("Starting server on :9000")
	if err := http.ListenAndServe("0.0.0.0:9000", nil); err != nil {
		log.Fatal().Err(err).Msg("Server failed to start")
	}
}

// waitForPort8000AndStartServer 等待8000端口被监听，然后启动9000端口服务
func waitForPort8000AndStartServer() {
	for {
		if checkPort(8000) {
			log.Info().Msg("Port 8000 is now available, starting server on :9000")
			break
		}
		time.Sleep(time.Second)
	}

	setupAndStartServer()
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

	id := createProcess(req)
	response := map[string]string{"id": id}
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
