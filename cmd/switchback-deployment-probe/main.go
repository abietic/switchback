package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeployment)) }

// run's observer is a private test seam; production main binds the fixed probe.
func run(args []string, stdout, stderr io.Writer, observe func(context.Context) (capacityprobe.DeploymentObservation, error)) int {
	fail := func() int { _, _ = io.WriteString(stderr, "deployment diagnostic failed\n"); return 1 }
	if len(args) != 0 || observe == nil {
		return fail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, e := observe(ctx)
	if e != nil {
		return fail()
	}
	raw, e := json.Marshal(value)
	if e != nil {
		return fail()
	}
	raw = append(raw, '\n')
	if len(raw) > 32768 {
		return fail()
	}
	n, e := stdout.Write(raw)
	if e != nil || n != len(raw) {
		return fail()
	}
	for _, ns := range value.Namespaces {
		if ns.Before.Errno == 13 || ns.Before.Errno == 1 {
			return 2
		}
	}
	return 0
}
