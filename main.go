package main

import (
	"os"

	"github.com/117503445/goutils"
	"github.com/rs/zerolog/log"
)

func main() {
	goutils.InitZeroLog()

	// 读取开关环境变量，默认为打开
	isWaitForPort8000 := os.Getenv("SKIP_WAIT_FOR_PORT_8000") == ""
	if isWaitForPort8000 {
		go waitForPort8000AndStartServer()
	} else {
		go setupAndStartServer()
	}

	// 检查并启动 entrypoint.sh 进程
	entrypointPath := os.Getenv("ENTRYPOINT_SCRIPT")
	if entrypointPath == "" {
		entrypointPath = "/entrypoint.sh"
	}
	if _, err := os.Stat(entrypointPath); err == nil {
		log.Info().Str("path", entrypointPath).Msg("Found entrypoint script, starting as process")
		startEntrypointProcess(entrypointPath)
	} else {
		log.Info().Str("path", entrypointPath).Msg("Entrypoint script not found, skipping")
	}

	select {}
}
