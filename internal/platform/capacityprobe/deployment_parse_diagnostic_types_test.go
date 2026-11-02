package capacityprobe

import (
	"encoding/json"
	"strings"
	"testing"
)

func deploymentParseTestFailure(owner string) DeploymentParseFailureObservation {
	return DeploymentParseFailureObservation{Schema: DeploymentParseFailureSchema, Kind: "diagnostic_only", Stage: "observe", ErrorClass: "invalid", LastPrimitive: &DeploymentPrimitiveObservation{Operation: "read", Target: owner + ".cgroup", Outcome: "returned"}, ParseChecks: &DeploymentProcessParseChecks{Owner: owner, StatRead: "bounded", CgroupRead: "bounded", CgroupCheck: "accepted", ProcessCheck: "rejected"}}
}
func TestDeploymentParseValidateTuples(t *testing.T) {
	for _, owner := range []string{"caller", "pid1"} {
		for _, stat := range []string{"bounded", "empty", "oversize", "unknown", ""} {
			for _, group := range []string{"empty", "bounded", "oversize", "unknown", ""} {
				for _, cg := range []string{"not_checked", "accepted", "rejected", "unknown", ""} {
					for _, proc := range []string{"not_checked", "accepted", "rejected", "unknown", ""} {
						name := strings.Join([]string{owner, stat, group, cg, proc}, "/")
						t.Run(name, func(t *testing.T) {
							v := deploymentParseTestFailure(owner)
							v.ParseChecks = &DeploymentProcessParseChecks{owner, stat, group, cg, proc}
							want := stat == "bounded" && (((group == "empty" || group == "oversize") && cg == "not_checked" && proc == "not_checked") || (group == "bounded" && ((cg == "accepted" && (proc == "accepted" || proc == "rejected")) || (cg == "rejected" && proc == "rejected"))))
							if (v.Validate() == nil) != want {
								t.Fatalf("unexpected validation: %#v", v.ParseChecks)
							}
						})
					}
				}
			}
		}
	}
}
func TestDeploymentParseValidateQualification(t *testing.T) {
	for name, change := range map[string]func(*DeploymentParseFailureObservation){
		"schema":        func(v *DeploymentParseFailureObservation) { v.Schema = DeploymentFailureSchema },
		"kind":          func(v *DeploymentParseFailureObservation) { v.Kind = "authority" },
		"stage":         func(v *DeploymentParseFailureObservation) { v.Stage = "entry" },
		"class":         func(v *DeploymentParseFailureObservation) { v.ErrorClass = "cancelled" },
		"nil-primitive": func(v *DeploymentParseFailureObservation) { v.LastPrimitive = nil },
		"operation":     func(v *DeploymentParseFailureObservation) { v.LastPrimitive.Operation = "stat" },
		"target":        func(v *DeploymentParseFailureObservation) { v.LastPrimitive.Target = "pid1.cgroup" },
		"outcome":       func(v *DeploymentParseFailureObservation) { v.LastPrimitive.Outcome = "failed" },
		"errno":         func(v *DeploymentParseFailureObservation) { n := uint32(5); v.LastPrimitive.Errno = &n },
		"owner":         func(v *DeploymentParseFailureObservation) { v.ParseChecks.Owner = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			v := deploymentParseTestFailure("caller")
			change(&v)
			if v.Validate() != ErrDeploymentInvalid {
				t.Fatalf("accepted %#v", v)
			}
		})
	}
}
func TestDeploymentParseValidateV1Delegation(t *testing.T) {
	for _, stage := range []string{"entry", "observe", "encode", "size", "write", "unknown"} {
		for _, class := range []string{"invalid", "unavailable", "cancelled", "unsupported", "internal_invalid_result", "encoding_failed", "size_exceeded", "write_failed", "unknown"} {
			for _, primitive := range []*DeploymentPrimitiveObservation{nil, {Operation: "read", Target: "pid1.cgroup", Outcome: "returned"}, {Operation: "raw-canary", Target: "secret-proc", Outcome: "unknown"}} {
				old := DeploymentFailureObservation{Schema: DeploymentFailureSchema, Kind: "diagnostic_only", Stage: stage, ErrorClass: class, LastPrimitive: primitive}
				v := deploymentParseFailure(&old)
				if (old.Validate() == nil) != (v.Validate() == nil) {
					t.Fatalf("validation drift: %#v", old)
				}
			}
		}
	}
}
func TestDeploymentParseFailureCopyAndBounds(t *testing.T) {
	n := uint32(4095)
	old := &DeploymentFailureObservation{Schema: DeploymentFailureSchema, Kind: "diagnostic_only", Stage: "observe", ErrorClass: "unavailable", LastPrimitive: &DeploymentPrimitiveObservation{Operation: "read", Target: "pid1.cgroup", Outcome: "failed", Errno: &n}}
	v := deploymentParseFailure(old)
	old.LastPrimitive.Target = "secret"
	n = 1
	if v.LastPrimitive.Target != "pid1.cgroup" || *v.LastPrimitive.Errno != 4095 {
		t.Fatal("aliased primitive")
	}
	if deploymentParseFailure(nil) != nil {
		t.Fatal("nil conversion")
	}
	longest := deploymentParseTestFailure("caller")
	longest.ParseChecks.CgroupRead = "oversize"
	longest.ParseChecks.CgroupCheck = "not_checked"
	longest.ParseChecks.ProcessCheck = "not_checked"
	raw, e := json.Marshal(longest)
	if e != nil || len(raw)+1 > 4096 || longest.Validate() != nil {
		t.Fatal(len(raw), e)
	}
}
