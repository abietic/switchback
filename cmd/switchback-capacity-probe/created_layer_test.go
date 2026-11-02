package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

const commandCID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCreatedLayerCommandRejectsBeforeReads(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {strings.Repeat("A", 64)}, {"short"}, {commandCID, "extra"}} {
		calls := 0
		var out, err bytes.Buffer
		exit := runCreatedLayerDiagnostic(args, "linux", "arm64", &out, &err, func(context.Context, capacityprobe.CreatedLayerTarget) capacityprobe.CreatedLayerCollection {
			calls++
			panic("unexpected reads")
		})
		if exit != 2 || calls != 0 || out.Len() != 0 || strings.Contains(err.String(), "short") {
			t.Fatal("invalid request performed reads/leaked input")
		}
	}
	for _, platform := range [][2]string{{"darwin", "arm64"}, {"linux", "386"}, {"windows", "amd64"}} {
		var out, err bytes.Buffer
		exit := runCreatedLayerDiagnostic([]string{commandCID}, platform[0], platform[1], &out, &err, func(context.Context, capacityprobe.CreatedLayerTarget) capacityprobe.CreatedLayerCollection {
			panic("unexpected reads")
		})
		if exit != 2 || out.Len() != 0 {
			t.Fatal("unsupported platform accepted")
		}
	}
	// Calls the actual main dispatch with invalid input; no Engine or /mnt access.
	if run([]string{"collect-created-layer", "bad"}) != 2 {
		t.Fatal("main did not dispatch")
	}
	if runtime.GOOS != "linux" {
		if run([]string{"collect-created-layer", commandCID}) != 2 {
			t.Fatal("unsupported main dispatch")
		}
	}
}

type badDiagnosticWriter struct{}

func (badDiagnosticWriter) Write([]byte) (int, error) { return 0, errors.New("SECRET-WRITER-CANARY") }
func TestCreatedLayerCommandPrintsOnlyCollectorDiagnosticAndStaysBlocked(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			collect := func(ctx context.Context, target capacityprobe.CreatedLayerTarget) capacityprobe.CreatedLayerCollection {
				calls++
				if target.ContainerID != commandCID {
					t.Fatal("CID changed")
				}
				if d, ok := ctx.Deadline(); !ok || time.Until(d) > 30*time.Second {
					t.Fatal("sampling budget missing")
				}
				// Real collector, intentionally incomplete; no actual Engine or physical IO.
				return capacityprobe.CollectCreatedLayer(ctx, target, capacityprobe.CreatedLayerSources{Get: func(context.Context, string) ([]byte, error) { return nil, errors.New("SECRET-ENGINE-CANARY") }, Physical: func(context.Context, string) (*capacityprobe.CreatedLayerPhysical, error) {
					t.Fatal("unexpected physical call")
					return nil, nil
				}})
			}
			exit := runCreatedLayerDiagnostic([]string{commandCID}, "linux", arch, &out, &diag, collect)
			if exit != 1 || calls != 1 || diag.Len() != 0 {
				t.Fatal("nonzero blocked result lost")
			}
			var c capacityprobe.CreatedLayerCollection
			if e := json.Unmarshal(out.Bytes(), &c); e != nil {
				t.Fatal(e)
			}
			if c.Status != "preparation_blocked" || c.Trust != "none" || c.CapacityReady || c.ExecutionEnabled || strings.Contains(out.String(), "SECRET") {
				t.Fatal("unsafe output")
			}
			d := json.NewDecoder(&out)
			if d.Decode(&c) != nil {
				t.Fatal("missing JSON")
			}
			if d.Decode(&c) != io.EOF {
				t.Fatal("more than one JSON")
			}
			diag.Reset()
			if runCreatedLayerDiagnostic([]string{commandCID}, "linux", arch, badDiagnosticWriter{}, &diag, collect) != 2 || strings.Contains(diag.String(), "SECRET") {
				t.Fatal("writer failure leaked")
			}
		})
	}
	// The command remains nonzero even for a completed collector observation.
	var out, diag bytes.Buffer
	exit := runCreatedLayerDiagnostic([]string{commandCID}, "linux", "arm64", &out, &diag, func(context.Context, capacityprobe.CreatedLayerTarget) capacityprobe.CreatedLayerCollection {
		return capacityprobe.CreatedLayerCollection{Status: "preparation_blocked", Trust: "none", ObservationComplete: true}
	})
	if exit != 1 || !strings.Contains(out.String(), `"observation_complete":true`) {
		t.Fatal("complete observation admitted")
	}
}
