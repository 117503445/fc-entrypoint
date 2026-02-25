package main

import (
	"github.com/117503445/goutils/glog"
	"github.com/alecthomas/kong"
	"github.com/rs/zerolog/log"

	"github.com/117503445/fc-entrypoint/internal/buildinfo"
)

func init() {
	glog.InitZeroLog()
}

func main() {
	log.Info().
		Str("BuildTime", buildinfo.BuildTime).
		Str("GitBranch", buildinfo.GitBranch).
		Str("GitCommit", buildinfo.GitCommit).
		Str("GitTag", buildinfo.GitTag).
		Str("GitDirty", buildinfo.GitDirty).
		Str("GitVersion", buildinfo.GitVersion).
		Str("BuildDir", buildinfo.BuildDir).
		Msg("build info")

	ctx := kong.Parse(&cli)
	log.Info().Interface("cli", cli).Send()
	if err := ctx.Run(); err != nil {
		log.Fatal().Err(err).Msg("run failed")
	}
}