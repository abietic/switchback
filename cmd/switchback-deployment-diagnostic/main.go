package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeploymentDiagnostic))
}

// run's observer is a private test seam; main binds the fixed diagnostic API.
func run(args []string, stdout, stderr io.Writer, observe func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentFailureObservation)) int {
	if len(args) != 0 || observe == nil {
		return writeResult(stdout, stderr, failurePacket("entry", "invalid"), 1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, failure := observe(ctx)
	var output any = value
	exit := 0
	limit := 32768
	if failure != nil {
		if value != (capacityprobe.DeploymentObservation{}) || failure.Validate() != nil {
			return writeResult(stdout, stderr, failurePacket("observe", "internal_invalid_result"), 1)
		}
		output, exit = failure, 1
		limit = 4096
	} else {
		if value.Schema != "switchback.capacity.deployment_diagnostic.v1" || value.Kind != "diagnostic_only" {
			return writeResult(stdout, stderr, failurePacket("observe", "internal_invalid_result"), 1)
		}
		for _, ns := range value.Namespaces {
			if ns.Before.Errno == 1 || ns.Before.Errno == 13 {
				exit = 2
			}
		}
	}
	packet, err := json.Marshal(output)
	if err != nil {
		return writeResult(stdout, stderr, failurePacket("encode", "encoding_failed"), 1)
	}
	packet = append(packet, '\n')
	if len(packet) > limit {
		return writeResult(stdout, stderr, failurePacket("size", "size_exceeded"), 1)
	}
	return writeResult(stdout, stderr, packet, exit)
}

// All callers pass fixed stage/class pairs. These string-only packets cannot
// fail JSON encoding and are bounded by the closed failure vocabulary.
func failurePacket(stage, class string) []byte {
	packet, _ := json.Marshal(capacityprobe.DeploymentFailureObservation{Schema: capacityprobe.DeploymentFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class})
	return append(packet, '\n')
}

// Once stdout has been attempted, only stderr may receive an output failure.
// A failed stderr write ends handling; neither stream is retried.
func writeResult(stdout, stderr io.Writer, packet []byte, exit int) int {
	if !writePacket(stdout, packet) {
		writePacket(stderr, failurePacket("write", "write_failed"))
		return 1
	}
	return exit
}

func writePacket(w io.Writer, packet []byte) bool {
	if w == nil {
		return false
	}
	n, err := w.Write(packet)
	return err == nil && n == len(packet)
}
