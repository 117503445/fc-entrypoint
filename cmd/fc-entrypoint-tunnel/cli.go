package main

import (
	"github.com/alecthomas/kong"
)

var cli struct {
	Tunnel CmdTunnel `cmd:"" default:"withargs" help:"Start the SSH tunnel client"`
}

type CmdTunnel struct {
	RemoteHost     string `help:"Remote host to connect, example: https://devpod-bfeefcee-vuxzljmeaj.cn-hangzhou.ide.fc.aliyun.com" required:"true" short:"r"`
	LocalPort      int    `help:"Local port to bind" default:"10022" short:"l"`
	AutoSelectPort bool   `help:"Automatically select an available high port if the specified port is occupied" short:"a"`
}

func (cmd *CmdTunnel) Run(ctx *kong.Context) error {
	return RunTunnel(cmd)
}
