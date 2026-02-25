package main

import (
	"github.com/alecthomas/kong"
)

var cli struct {
	Serve CmdServe `cmd:"" help:"Start the entrypoint server"`
}

type CmdServe struct {
	Port                  string `help:"Port for the entrypoint server" default:"9000"`
	SkipWaitForPort8000   bool   `help:"Skip waiting for port 8000 to be available" env:"SKIP_WAIT_FOR_PORT_8000"`
	EntrypointScriptPath  string `help:"Path to entrypoint script" env:"ENTRYPOINT_SCRIPT" default:"/entrypoint.sh"`
	LogBody               bool   `help:"Log request/response bodies" env:"LOG_BODY" default:"true"`
	DirData               string `help:"Data directory for process storage" env:"DIR_DATA" default:"./data"`
	CleanupProcessDir     bool   `help:"Cleanup process directories after execution" env:"CLEANUP_PROCESS_DIR" default:"true"`
}

func (cmd *CmdServe) Run(ctx *kong.Context) error {
	return RunServe(cmd)
}