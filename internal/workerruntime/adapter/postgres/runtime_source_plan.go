package postgres

import (
	"bytes"
	"encoding/json"
	"time"

	app "github.com/abietic/switchback/internal/workerruntime/application"
)

type persistedRuntimeSourcePlan struct {
	bytes   []byte
	digest  *string
	boundAt *time.Time
}

type initialSourcePlanInput struct {
	binding   app.RuntimeSourcePlanBinding
	legacy    string
	provision bool
}

// Canonical parsing happens once before acquiring the capacity pool lock. The
// binding contains immutable strings; this supplies no business authority.
func validateInitialSourcePlanInput(request app.RuntimePreparationRequest, prepared *app.RuntimeProvisionRequest) (initialSourcePlanInput, error) {
	legacy, err := request.SourcePlan.LegacyFileDigest()
	if err != nil || prepared != nil && prepared.SourcePlanDigest != request.SourcePlan.Digest {
		return initialSourcePlanInput{}, app.CapacityBindingMismatch
	}
	return initialSourcePlanInput{binding: request.SourcePlan, legacy: legacy, provision: prepared != nil}, nil
}

// This pure comparison runs in the real original pool/business transaction.
// Exact equality with the validated canonical request proves persisted bytes
// canonical without rebuilding a potentially large manifest under the lock.
func (input initialSourcePlanInput) validate(snapshot []byte, stored persistedRuntimeSourcePlan) error {
	var start struct {
		Digest string `json:"working_copy_checkpoint_digest"`
	}
	if json.Unmarshal(snapshot, &start) != nil || start.Digest != "sha256:"+input.legacy {
		return app.CapacityBindingMismatch
	}
	empty := stored.bytes == nil && stored.digest == nil && stored.boundAt == nil
	if !empty {
		if len(stored.bytes) == 0 || stored.digest == nil || stored.boundAt == nil || stored.boundAt.IsZero() {
			return app.CapacityBindingMismatch
		}
		if !bytes.Equal(stored.bytes, []byte(input.binding.Manifest)) || *stored.digest != input.binding.Digest {
			return app.CapacityBindingMismatch
		}
	}
	if input.provision && empty {
		return app.CapacityBindingMismatch
	}
	return nil
}
