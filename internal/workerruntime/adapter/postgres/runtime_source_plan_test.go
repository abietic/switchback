package postgres

import (
	"encoding/json"
	"errors"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"strings"
	"testing"
	"time"
)

func sourcePlanPureFixture(t *testing.T) (app.RuntimePreparationRequest, app.RuntimeProvisionRequest, []byte, persistedRuntimeSourcePlan) {
	t.Helper()
	b, err := app.NewRuntimeSourcePlanBinding("sha256:"+strings.Repeat("a", 64), nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := b.LegacyFileDigest()
	snapshot, _ := json.Marshal(map[string]string{"working_copy_checkpoint_digest": "sha256:" + legacy})
	at := time.Now().UTC()
	digest := b.Digest
	return app.RuntimePreparationRequest{SourcePlan: b}, app.RuntimeProvisionRequest{SourcePlanDigest: b.Digest}, snapshot, persistedRuntimeSourcePlan{bytes: []byte(b.Manifest), digest: &digest, boundAt: &at}
}
func TestRuntimeSourcePlanOriginalPreparationAndPinnedProvision(t *testing.T) {
	r, p, s, pin := sourcePlanPureFixture(t)
	for _, tc := range []struct {
		name     string
		prepared *app.RuntimeProvisionRequest
		stored   persistedRuntimeSourcePlan
	}{
		{"first_preparation", nil, persistedRuntimeSourcePlan{}}, {"repeat_preparation", nil, pin}, {"pinned_provision", &p, pin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkInitialSourcePlan(r, tc.prepared, s, tc.stored); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestRuntimeSourcePlanRejectsMissingNoncanonicalAndDrift(t *testing.T) {
	for _, name := range []string{"request_missing", "request_noncanonical", "request_digest", "persisted_noncanonical", "persisted_digest", "persisted_different_plan", "partial_bytes", "partial_digest", "partial_time", "empty_bytes", "zero_time", "provision_without_pin", "prepared_missing", "prepared_drift", "snapshot_missing", "snapshot_malformed", "snapshot_drift"} {
		t.Run(name, func(t *testing.T) {
			r, p, s, pin := sourcePlanPureFixture(t)
			var prepared *app.RuntimeProvisionRequest
			switch name {
			case "request_missing":
				r.SourcePlan = app.RuntimeSourcePlanBinding{}
			case "request_noncanonical":
				r.SourcePlan.Manifest += " "
			case "request_digest":
				r.SourcePlan.Digest = "sha256:" + strings.Repeat("b", 64)
			case "persisted_noncanonical":
				pin.bytes = append(pin.bytes, ' ')
			case "persisted_digest":
				wrong := "sha256:" + strings.Repeat("b", 64)
				pin.digest = &wrong
			case "persisted_different_plan":
				other, err := app.NewRuntimeSourcePlanBinding("sha256:"+strings.Repeat("a", 64), []app.RuntimeSourcePlanEntry{{Path: "empty", Kind: "directory", Mode: 0o700}})
				if err != nil {
					t.Fatal(err)
				}
				pin.bytes = []byte(other.Manifest)
				pin.digest = &other.Digest
			case "partial_bytes":
				pin.bytes = nil
			case "partial_digest":
				pin.digest = nil
			case "partial_time":
				pin.boundAt = nil
			case "empty_bytes":
				pin.bytes = []byte{}
			case "zero_time":
				at := time.Time{}
				pin.boundAt = &at
			case "provision_without_pin":
				prepared = &p
				pin = persistedRuntimeSourcePlan{}
			case "prepared_missing":
				prepared = &p
				p.SourcePlanDigest = ""
			case "prepared_drift":
				prepared = &p
				p.SourcePlanDigest = "sha256:" + strings.Repeat("b", 64)
			case "snapshot_missing":
				s = []byte(`{}`)
			case "snapshot_malformed":
				s = []byte(`[]`)
			case "snapshot_drift":
				s = []byte(`{"working_copy_checkpoint_digest":"sha256:` + strings.Repeat("b", 64) + `"}`)
			}
			if err := checkInitialSourcePlan(r, prepared, s, pin); !errors.Is(err, app.CapacityBindingMismatch) {
				t.Fatalf("accepted %s: %v", name, err)
			}
		})
	}
}

func checkInitialSourcePlan(r app.RuntimePreparationRequest, p *app.RuntimeProvisionRequest, s []byte, pin persistedRuntimeSourcePlan) error {
	input, err := validateInitialSourcePlanInput(r, p)
	if err != nil {
		return err
	}
	return input.validate(s, pin)
}
