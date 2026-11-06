package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/abietic/switchback/internal/platform/qualificationio"
	"github.com/abietic/switchback/internal/workerruntime/adapter/shimruntime"
	sdk "github.com/coder/acp-go-sdk"
)

const (
	shimVersion     = "switchback-worker-shim-m1"
	acpSDKVersion   = "v0.13.5"
	codexACPVersion = "1.1.2"
	codexVersion    = "0.144.1"
	liteLLMVersion  = "1.93.0"
)

type buildInfo struct {
	ShimVersion     string `json:"shim_version"`
	ACPGoSDKVersion string `json:"acp_go_sdk_version"`
	ACPProtocol     int    `json:"acp_protocol"`
	CodexACPVersion string `json:"codex_acp_version"`
	CodexVersion    string `json:"codex_version"`
	LiteLLMVersion  string `json:"litellm_version"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runContext(ctx, os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	return runContext(context.Background(), arguments, output)
}

func runContext(ctx context.Context, arguments []string, output io.Writer) error {
	if len(arguments) == 1 && arguments[0] == "--build-info" {
		return json.NewEncoder(output).Encode(currentBuildInfo())
	}
	if len(arguments) == 1 && arguments[0] == "qualify-worker" {
		return qualificationio.RunWorker(ctx, output)
	}
	if len(arguments) != 0 {
		return fmt.Errorf("Worker Shim argument is unsupported")
	}
	return shimruntime.Run(ctx, output, os.Stderr)
}

func currentBuildInfo() buildInfo {
	return buildInfo{
		ShimVersion:     shimVersion,
		ACPGoSDKVersion: acpSDKVersion,
		ACPProtocol:     sdk.ProtocolVersionNumber,
		CodexACPVersion: codexACPVersion,
		CodexVersion:    codexVersion,
		LiteLLMVersion:  liteLLMVersion,
	}
}
