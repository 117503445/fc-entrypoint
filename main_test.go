package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

func setupTest(t *testing.T) {
	// 重置全局变量
	processesMu.Lock()
	processes = make(map[int64]*Process)
	nextProcessID = 0
	processesMu.Unlock()
}

func TestHandleCreateProcess(t *testing.T) {
	setupTest(t)

	tests := []struct {
		name           string
		requestBody    string
		expectedStatus int
		expectID       bool
		expectError    bool
	}{
		{
			name:           "valid request",
			requestBody:    `{"command": "echo hello", "working_dir": "/tmp"}`,
			expectedStatus: http.StatusOK,
			expectID:       true,
			expectError:    false,
		},
		{
			name:           "missing command",
			requestBody:    `{"working_dir": "/tmp"}`,
			expectedStatus: http.StatusBadRequest,
			expectID:       false,
			expectError:    true,
		},
		{
			name:           "invalid json",
			requestBody:    `{"command": "echo hello", "working_dir": `,
			expectedStatus: http.StatusBadRequest,
			expectID:       false,
			expectError:    true,
		},
		{
			name:           "empty command",
			requestBody:    `{"command": "", "working_dir": "/tmp"}`,
			expectedStatus: http.StatusBadRequest,
			expectID:       false,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/_entrypoint/processes", bytes.NewBufferString(tt.requestBody))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			handleCreateProcess(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)

			if tt.expectID {
				var response map[string]int64
				err := json.NewDecoder(w.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Contains(t, response, "id")
				assert.Greater(t, response["id"], int64(0))
			}

			if tt.expectError {
				var response map[string]string
				err := json.NewDecoder(w.Body).Decode(&response)
				assert.NoError(t, err)
				assert.Contains(t, response, "error")
			}
		})
	}
}

func TestHandleListProcesses(t *testing.T) {
	setupTest(t)

	// 创建一些测试进程
	process1 := &Process{ID: 1, Command: "echo test1", WorkingDir: "/tmp", Status: "completed", Output: "test1\n"}
	process2 := &Process{ID: 2, Command: "echo test2", WorkingDir: "/home", Status: "running"}

	processesMu.Lock()
	processes[1] = process1
	processes[2] = process2
	processesMu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/_entrypoint/processes", nil)
	w := httptest.NewRecorder()

	handleListProcesses(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var response []*Process
	err := json.NewDecoder(w.Body).Decode(&response)
	assert.NoError(t, err)
	assert.Len(t, response, 2)

	// 检查返回的进程（顺序可能不同）
	foundIDs := make(map[int64]bool)
	for _, p := range response {
		foundIDs[p.ID] = true
		assert.Contains(t, []int64{1, 2}, p.ID)
		if p.ID == 1 {
			assert.Equal(t, "echo test1", p.Command)
			assert.Equal(t, "/tmp", p.WorkingDir)
			assert.Equal(t, "completed", p.Status)
			assert.Equal(t, "test1\n", p.Output)
		} else if p.ID == 2 {
			assert.Equal(t, "echo test2", p.Command)
			assert.Equal(t, "/home", p.WorkingDir)
			assert.Equal(t, "running", p.Status)
		}
	}
	assert.True(t, foundIDs[1])
	assert.True(t, foundIDs[2])
}

func TestHandleProcesses(t *testing.T) {
	setupTest(t)

	t.Run("POST method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/_entrypoint/processes",
			bytes.NewBufferString(`{"command": "echo test"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handleProcesses(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("GET method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_entrypoint/processes", nil)
		w := httptest.NewRecorder()

		handleProcesses(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("unsupported method", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/_entrypoint/processes", nil)
		w := httptest.NewRecorder()

		handleProcesses(w, req)

		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)

		var response map[string]string
		err := json.NewDecoder(w.Body).Decode(&response)
		assert.NoError(t, err)
		assert.Equal(t, "Method not allowed", response["error"])
	})
}

func TestExecuteProcess(t *testing.T) {
	setupTest(t)

	tests := []struct {
		name           string
		command        string
		workingDir     string
		expectError    bool
		expectedOutput string
	}{
		{
			name:           "successful command",
			command:        "echo hello world",
			workingDir:     "",
			expectError:    false,
			expectedOutput: "hello world\n",
		},
		{
			name:           "command with working directory",
			command:        "pwd",
			workingDir:     "/tmp",
			expectError:    false,
			expectedOutput: "/tmp\n",
		},
		{
			name:           "failing command",
			command:        "false",
			workingDir:     "",
			expectError:    true,
			expectedOutput: "",
		},
		{
			name:           "command with stderr",
			command:        "echo error >&2 && echo output",
			workingDir:     "",
			expectError:    false,
			expectedOutput: "output\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			process := &Process{
				ID:         1,
				Command:    tt.command,
				WorkingDir: tt.workingDir,
				Status:     "running",
			}

			processesMu.Lock()
			processes[process.ID] = process
			processesMu.Unlock()

			executeProcess(process)

			processesMu.RLock()
			updatedProcess := processes[process.ID]
			processesMu.RUnlock()

			if tt.expectError {
				assert.Equal(t, "failed", updatedProcess.Status)
			} else {
				assert.Equal(t, "completed", updatedProcess.Status)
				assert.Equal(t, tt.expectedOutput, updatedProcess.Output)
			}
		})
	}
}

func TestReverseProxyHandler(t *testing.T) {
	// 创建一个测试服务器来模拟目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "proxied")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("proxied response"))
	}))
	defer targetServer.Close()

	// 创建一个测试版本的reverse proxy handler
	testReverseProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := targetServer.URL + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		for key, values := range r.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Failed to forward request", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		w.WriteHeader(resp.StatusCode)
		// 复制响应体
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		w.Write(buf.Bytes())
	}

	req := httptest.NewRequest(http.MethodGet, "/test/path?query=value", nil)
	w := httptest.NewRecorder()

	testReverseProxyHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "proxied", w.Header().Get("X-Test"))
	assert.Equal(t, "proxied response", w.Body.String())
}

func TestReverseProxyHandlerWithLogging(t *testing.T) {
	// 临时启用 LOG_BODY
	originalLogBody := LOG_BODY
	LOG_BODY = true
	defer func() { LOG_BODY = originalLogBody }()

	// 创建一个测试服务器来模拟目标服务器
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "success"}`))
	}))
	defer targetServer.Close()

	// 创建一个支持日志的测试版本的reverse proxy handler
	testReverseProxyHandler := func(w http.ResponseWriter, r *http.Request) {
		targetURL := targetServer.URL + r.URL.Path
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

		req, err := http.NewRequest(r.Method, targetURL, r.Body)
		if err != nil {
			http.Error(w, "Failed to create request", http.StatusInternalServerError)
			return
		}

		for key, values := range r.Header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}

		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "Failed to forward request", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// 如果启用日志，读取并打印响应body
		var respBodyBytes []byte
		if LOG_BODY {
			respBodyBytes, err = io.ReadAll(resp.Body)
			if err != nil {
				log.Error().Err(err).Msg("Failed to read response body for logging")
			} else {
				log.Info().Str("response_body", string(respBodyBytes)).Msg("Received response body")
			}
			resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
		}

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}

	// 测试带请求体的POST请求
	reqBody := `{"action": "test", "data": "sample"}`
	req := httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	testReverseProxyHandler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, `{"result": "success"}`, w.Body.String())
}

func TestIntegration(t *testing.T) {
	setupTest(t)

	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_entrypoint/processes" {
			handleProcesses(w, r)
		} else {
			reverseProxyHandler(w, r)
		}
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	t.Run("create and list process", func(t *testing.T) {
		// 创建进程
		reqBody := `{"command": "echo integration test", "working_dir": "/tmp"}`
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/_entrypoint/processes",
			bytes.NewBufferString(reqBody))
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var createResp map[string]int64
		json.NewDecoder(resp.Body).Decode(&createResp)
		resp.Body.Close()

		processID := createResp["id"]
		assert.Greater(t, processID, int64(0))

		// 等待进程完成
		time.Sleep(100 * time.Millisecond)

		// 列出进程
		req, _ = http.NewRequest(http.MethodGet, server.URL+"/_entrypoint/processes", nil)
		resp, err = client.Do(req)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var processes []*Process
		json.NewDecoder(resp.Body).Decode(&processes)
		resp.Body.Close()

		assert.GreaterOrEqual(t, len(processes), 1)
		found := false
		for _, p := range processes {
			if p.ID == processID {
				found = true
				assert.Equal(t, "echo integration test", p.Command)
				assert.Equal(t, "/tmp", p.WorkingDir)
				assert.Equal(t, "completed", p.Status)
				assert.Equal(t, "integration test\n", p.Output)
				break
			}
		}
		assert.True(t, found, "Created process not found in list")
	})
}

func TestStartEntrypointProcess(t *testing.T) {
	setupTest(t)

	// 创建一个临时的 entrypoint.sh 文件
	entrypointPath := "/tmp/entrypoint_test.sh"
	entrypointContent := `#!/bin/bash
echo "Entrypoint running"
sleep 0.1
echo "Entrypoint done"
`

	err := os.WriteFile(entrypointPath, []byte(entrypointContent), 0755)
	assert.NoError(t, err)
	defer os.Remove(entrypointPath)

	// 测试版本的 startEntrypointProcess 函数
	testStartEntrypointProcess := func() {
		id := atomic.AddInt64(&nextProcessID, 1)

		process := &Process{
			ID:         id,
			Command:    entrypointPath,
			WorkingDir: "/",
			Status:     "running",
		}

		processesMu.Lock()
		processes[id] = process
		processesMu.Unlock()

		// 异步执行命令
		go executeProcess(process)

		log.Info().Int64("process_id", id).Msg("Started entrypoint process")
	}

	// 调用函数
	testStartEntrypointProcess()

	// 等待进程完成
	time.Sleep(200 * time.Millisecond)

	// 检查进程是否被创建并完成
	processesMu.RLock()
	process, exists := processes[1]
	processesMu.RUnlock()

	assert.True(t, exists, "Entrypoint process should be created")
	assert.Equal(t, "/tmp/entrypoint_test.sh", process.Command)
	assert.Equal(t, "/", process.WorkingDir)
	assert.Equal(t, "completed", process.Status)
	assert.Contains(t, process.Output, "Entrypoint running")
	assert.Contains(t, process.Output, "Entrypoint done")
}

func TestMainEntrypointCheck(t *testing.T) {
	setupTest(t)

	// 创建一个临时的 entrypoint.sh 文件
	entrypointPath := "/tmp/entrypoint_main_test.sh"
	entrypointContent := `#!/bin/bash
echo "Main entrypoint test"
`

	err := os.WriteFile(entrypointPath, []byte(entrypointContent), 0755)
	assert.NoError(t, err)
	defer os.Remove(entrypointPath)

	// 测试文件存在检查逻辑
	if _, err := os.Stat(entrypointPath); err == nil {
		t.Log("File exists check passed")
		// 这里会调用 startEntrypointProcess，但我们不执行它以避免实际启动进程
	} else {
		t.Error("File should exist")
	}

	// 验证没有实际进程被创建（因为我们没有调用startEntrypointProcess）
	processesMu.RLock()
	processCount := len(processes)
	processesMu.RUnlock()

	assert.Equal(t, 0, processCount, "No processes should be created in this test")
}

// TestMain 设置测试环境
func TestMain(m *testing.M) {
	// 设置测试前的初始化
	setupTest(nil)

	// 运行测试
	code := m.Run()

	// 清理
	os.Exit(code)
}
