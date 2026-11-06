package capacityprobe

const DeploymentParseFailureSchema = "switchback.capacity.deployment_failure.v2"

// DeploymentProcessParseChecks reports bounded parser results, never a root cause
// or authority to execute. Validate checks consistency, not provenance.
type DeploymentProcessParseChecks struct {
	Owner        string `json:"owner"`
	StatRead     string `json:"stat_read"`
	CgroupRead   string `json:"cgroup_read"`
	CgroupCheck  string `json:"cgroup_check"`
	ProcessCheck string `json:"process_check"`
}

type DeploymentParseFailureObservation struct {
	Schema        string                          `json:"schema"`
	Kind          string                          `json:"kind"`
	Stage         string                          `json:"stage"`
	ErrorClass    string                          `json:"error_class"`
	LastPrimitive *DeploymentPrimitiveObservation `json:"last_primitive,omitempty"`
	ParseChecks   *DeploymentProcessParseChecks   `json:"parse_checks,omitempty"`
}

func (v DeploymentParseFailureObservation) Validate() error {
	if v.Schema != DeploymentParseFailureSchema || v.Kind != "diagnostic_only" {
		return ErrDeploymentInvalid
	}
	if (DeploymentFailureObservation{Schema: DeploymentFailureSchema, Kind: v.Kind, Stage: v.Stage, ErrorClass: v.ErrorClass, LastPrimitive: v.LastPrimitive}).Validate() != nil {
		return ErrDeploymentInvalid
	}
	c := v.ParseChecks
	if c == nil {
		return nil
	}
	p := v.LastPrimitive
	if v.Stage != "observe" || v.ErrorClass != "invalid" || p == nil || p.Operation != "read" || p.Outcome != "returned" || p.Errno != nil || (c.Owner != "caller" && c.Owner != "pid1") || p.Target != c.Owner+".cgroup" || c.StatRead != "bounded" {
		return ErrDeploymentInvalid
	}
	switch c.CgroupRead {
	case "empty", "oversize":
		if c.CgroupCheck == "not_checked" && c.ProcessCheck == "not_checked" {
			return nil
		}
	case "bounded":
		if (c.CgroupCheck == "accepted" && (c.ProcessCheck == "accepted" || c.ProcessCheck == "rejected")) || (c.CgroupCheck == "rejected" && c.ProcessCheck == "rejected") {
			return nil
		}
	}
	return ErrDeploymentInvalid
}
