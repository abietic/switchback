// Command switchback-deployment-parse-diagnostic emits bounded diagnostic facts.
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
)

const (
	successLimit = 32768
	failureLimit = 4096
)

type observer func(context.Context) (capacityprobe.DeploymentObservation, *capacityprobe.DeploymentParseFailureObservation)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, capacityprobe.ObserveDeploymentParseDiagnostic))
}

// run is the only injection seam. Production has a fixed observer and no flags.
func run(args []string, stdout, stderr io.Writer, observe observer) int {
	packet, exit := diagnosticPacket("entry", "invalid"), 1
	if len(args) == 0 && observe != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		value, failure := observe(ctx)
		packet, exit = observationPacket(value, failure)
	}
	if !writeOnce(stdout, packet) {
		writeOnce(stderr, diagnosticPacket("write", "write_failed"))
		return 1
	}
	return exit
}

// observationPacket accepts one of the two result variants before encoding.
// The unchanged success v1 is intentionally not wrapped in the failure schema.
func observationPacket(value capacityprobe.DeploymentObservation, failure *capacityprobe.DeploymentParseFailureObservation) ([]byte, int) {
	if failure != nil {
		if value != (capacityprobe.DeploymentObservation{}) || failure.Validate() != nil {
			return diagnosticPacket("observe", "internal_invalid_result"), 1
		}
		return boundedJSON(failure, failureLimit, 1)
	}
	if value.Schema != "switchback.capacity.deployment_diagnostic.v1" || value.Kind != "diagnostic_only" {
		return diagnosticPacket("observe", "internal_invalid_result"), 1
	}
	exit := 0
	for _, namespace := range value.Namespaces {
		if namespace.Before.Errno == 1 || namespace.Before.Errno == 13 {
			exit = 2
		}
	}
	return boundedJSON(value, successLimit, exit)
}

func boundedJSON(value any, limit, exit int) ([]byte, int) {
	packet, err := json.Marshal(value)
	if err != nil {
		return diagnosticPacket("encode", "encoding_failed"), 1
	}
	if len(packet)+1 > limit {
		return diagnosticPacket("size", "size_exceeded"), 1
	}
	return append(packet, '\n'), exit
}

// Only fixed stage/class pairs reach this string-only DTO; encoding cannot fail.
func diagnosticPacket(stage, class string) []byte {
	packet, _ := json.Marshal(capacityprobe.DeploymentParseFailureObservation{
		Schema: capacityprobe.DeploymentParseFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class,
	})
	return append(packet, '\n')
}

// Never retry either stream, even when a writer reports full bytes plus error,
// impossible counts, or a partial write without an error.
func writeOnce(writer io.Writer, packet []byte) bool {
	if writer == nil {
		return false
	}
	n, err := writer.Write(packet)
	return err == nil && n == len(packet)
}
