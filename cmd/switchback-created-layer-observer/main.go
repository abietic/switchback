package main

import (
	"context"
	"fmt"
	ipc "github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	"io"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"syscall"
)

var observerDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type operations struct {
	supported      bool
	key            func() ([]byte, error)
	serve          func(context.Context, []byte, string) error
	serveInstalled func(context.Context, []byte, string) error
}

func run(ctx context.Context, args []string, out io.Writer, ops operations) int {
	if ctx == nil || ctx.Err() != nil || len(args) != 2 || args[0] != "serve" || !observerDigestPattern.MatchString(args[1]) {
		fmt.Fprintln(out, "observer: invalid invocation")
		return 2
	}
	if !ops.supported {
		fmt.Fprintln(out, "observer: unsupported platform")
		return 2
	}
	key, e := ops.key()
	if e != nil {
		fmt.Fprintln(out, "observer: key unavailable")
		return 1
	}
	if e = ops.serve(ctx, key, args[1]); e != nil {
		fmt.Fprintln(out, "observer: service unavailable")
		return 1
	}
	return 0
}

type observerService interface{ Serve(context.Context) error }
type observerServerFactory func([]byte, string) (observerService, error)

// Factories are private startup seams. Main always binds the two fixed public
// constructors; neither a cgroup string nor a policy switch comes from CLI/env.
func observerServiceOperations(supported bool, key func() ([]byte, error), legacy, qualification observerServerFactory) operations {
	serve := func(factory observerServerFactory) func(context.Context, []byte, string) error {
		return func(ctx context.Context, key []byte, digest string) error {
			s, err := factory(key, digest)
			if err != nil {
				return err
			}
			return s.Serve(ctx)
		}
	}
	return operations{supported: supported, key: key, serve: serve(legacy), serveInstalled: serve(qualification)}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ops := observerServiceOperations(runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"), ipc.ReadServerKey,
		func(key []byte, digest string) (observerService, error) { return ipc.NewServer(key, digest) },
		func(key []byte, digest string) (observerService, error) {
			return ipc.NewQualificationServer(key, digest)
		})
	code := 2
	if len(os.Args) > 1 && os.Args[1] == "serve-installed" {
		code = runInstalled(ctx, os.Args[1:], os.Stderr, ops, installedOperations{capacitysnapshot.AwaitDeploymentQualification, verifyInstalledObserver})
	} else {
		code = run(ctx, os.Args[1:], os.Stderr, ops)
	}
	stop()
	os.Exit(code)
}
