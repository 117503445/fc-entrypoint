# fc-entrypoint

一个用于 Function Compute 的入口点代理服务，支持进程管理和请求转发。

## 功能特性

- **反向代理**: 将 9000 端口的请求转发到 8000 端口
- **进程管理**: 提供 REST API 管理后台进程
- **自动执行**: 支持自动运行入口点脚本
- **日志输出**: 实时输出进程的标准输出和错误输出

## 快速开始

启动命令设置为

```sh
bash -c 'wget https://github.com/117503445/fc-entrypoint/releases/latest/download/fc-entrypoint -O /tmp/fc-entrypoint && chmod +x /tmp/fc-entrypoint && /tmp/fc-entrypoint'
```

端口设置为 9000

## 工作原理

1. 服务启动时默认等待 8000 端口可用
2. 在 9000 端口启动 HTTP 服务
3. 如果存在入口点脚本（由 `ENTRYPOINT_SCRIPT` 环境变量指定），自动执行
4. 所有请求转发到 `localhost:8000`
5. 提供进程管理 API 用于监控和控制后台进程

## 环境变量

| 变量名 | 说明 | 默认值 |
|--------|------|--------|
| `SKIP_WAIT_FOR_PORT_8000` | 跳过等待 8000 端口 | `""` (默认等待) |
| `ENTRYPOINT_SCRIPT` | 入口点脚本路径 | `/entrypoint.sh` |

## API 接口

### 列出所有进程

```bash
curl http://localhost:9000/_entrypoint/processes
```

响应示例:
```json
[
  {
    "id": 1,
    "command": "/entrypoint.sh",
    "working_dir": "/",
    "status": "running",
    "output": "",
    "error": ""
  }
]
```

### 创建新进程

```bash
curl -X POST http://localhost:9000/_entrypoint/processes \
  -H "Content-Type: application/json" \
  -d '{"command": "your-command", "working_dir": "/path/to/dir"}'
```

响应示例:
```json
{"id": 2}
```
