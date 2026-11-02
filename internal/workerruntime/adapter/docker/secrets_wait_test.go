package docker_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/application"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestProvisionRequiresSuccessfulSecretPopulatorWait(t *testing.T) {
	for _, outcome := range []string{"response error", "empty response error", "closed result", "closed errors", "nil error"} {
		t.Run(outcome, func(t *testing.T) {
			result := make(chan container.WaitResponse, 1)
			waitErrors := make(chan error, 1)
			switch outcome {
			case "response error":
				result <- container.WaitResponse{Error: &container.WaitExitError{Message: "sensitive-daemon-detail"}}
			case "empty response error":
				result <- container.WaitResponse{Error: &container.WaitExitError{}}
			case "closed result":
				close(result)
			case "closed errors":
				close(waitErrors)
			case "nil error":
				waitErrors <- nil
			}
			e := &populatorWaitEngine{
				prestartEngine: &prestartEngine{infoRecordingEngine: classicOverlay2Engine(&recordingEngine{inspectErr: cerrdefs.ErrNotFound})},
				wait:           client.ContainerWaitResult{Result: result, Error: waitErrors},
			}
			request := validRequest(t)
			checks := 0
			got, err := newProvisioner(t, e, &recordingVerifier{}).Provision(context.Background(), request, func(context.Context, application.CreatedRuntimeIdentity) error {
				checks++
				return nil
			})
			if err == nil || strings.Contains(err.Error(), "sensitive-daemon-detail") {
				t.Errorf("missing or unsanitized wait failure: %v", err)
			}
			if checks != 0 || len(e.creates) != 1 || !reflect.DeepEqual(e.started, []string{"container-1"}) {
				t.Errorf("invalid wait advanced Worker: checks=%d creates=%d starts=%q", checks, len(e.creates), e.started)
			}
			if !got.PhysicalEffectUnknown || got.RuntimeInstanceID != request.RuntimeInstanceID || got.Generation != request.Generation || got.ContainerID != "" || !got.StartedAt.IsZero() {
				t.Errorf("invalid wait lost partial ownership: %+v", got)
			}
			if !reflect.DeepEqual(e.removed, []string{"container-1"}) || !reflect.DeepEqual(e.volumeRemoved, []string{"switchback-runtime-secrets-runtime-1-g1"}) {
				t.Errorf("incorrect staging cleanup: containers=%q volumes=%q", e.removed, e.volumeRemoved)
			}
			for _, path := range []string{request.Secrets.Bootstrap.BackendPath, request.Secrets.ModelCredential.BackendPath} {
				if _, err := os.Lstat(path); err != nil {
					t.Errorf("failed staging removed transient input: %v", err)
				}
			}
		})
	}
}

type populatorWaitEngine struct {
	*prestartEngine
	wait client.ContainerWaitResult
}

func (e *populatorWaitEngine) ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult {
	return e.wait
}
