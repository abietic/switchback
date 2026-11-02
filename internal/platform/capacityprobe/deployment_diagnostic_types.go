package capacityprobe

const DeploymentFailureSchema = "switchback.capacity.deployment_failure.v1"

// DeploymentPrimitiveObservation describes the last returned backend call, not
// the root cause of a later parser, consistency, or cancellation failure.
type DeploymentPrimitiveObservation struct {
	Operation string  `json:"operation"`
	Target    string  `json:"target"`
	Outcome   string  `json:"outcome"`
	Errno     *uint32 `json:"errno,omitempty"`
}

// DeploymentFailureObservation contains bounded diagnostic context only. It
// grants no authority and contains neither raw errors nor partial proc facts.
type DeploymentFailureObservation struct {
	Schema        string                          `json:"schema"`
	Kind          string                          `json:"kind"`
	Stage         string                          `json:"stage"`
	ErrorClass    string                          `json:"error_class"`
	LastPrimitive *DeploymentPrimitiveObservation `json:"last_primitive,omitempty"`
}

func (v DeploymentFailureObservation) Validate() error {
	if v.Schema != DeploymentFailureSchema || v.Kind != "diagnostic_only" {
		return ErrDeploymentInvalid
	}
	valid := false
	switch v.Stage {
	case "entry":
		valid = v.ErrorClass == "invalid"
	case "observe":
		switch v.ErrorClass {
		case "invalid", "unavailable", "cancelled", "unsupported", "internal_invalid_result":
			valid = true
		}
	case "encode":
		valid = v.ErrorClass == "encoding_failed"
	case "size":
		valid = v.ErrorClass == "size_exceeded"
	case "write":
		valid = v.ErrorClass == "write_failed"
	}
	if !valid {
		return ErrDeploymentInvalid
	}
	if p := v.LastPrimitive; p != nil {
		if v.Stage != "observe" {
			return ErrDeploymentInvalid
		}
		switch p.Operation {
		case "now", "ids", "kernel", "open_global", "open_proc", "open_leaf", "open_namespace", "namespace_stat", "stat", "statfs", "read":
		default:
			return ErrDeploymentInvalid
		}
		switch p.Target {
		case "clock", "identity", "kernel", "boot", "cap_last_cap", "caller", "pid1", "unknown",
			"caller.status", "caller.stat", "caller.cgroup", "caller.ns.user", "caller.ns.pid", "caller.ns.mnt", "caller.ns.cgroup",
			"pid1.status", "pid1.stat", "pid1.cgroup", "pid1.ns.user", "pid1.ns.pid", "pid1.ns.mnt", "pid1.ns.cgroup":
		default:
			return ErrDeploymentInvalid
		}
		if p.Outcome != "returned" && p.Outcome != "failed" {
			return ErrDeploymentInvalid
		}
		if p.Errno != nil && (p.Outcome != "failed" || *p.Errno < 1 || *p.Errno > 4095) {
			return ErrDeploymentInvalid
		}
	}
	return nil
}
