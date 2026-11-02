package evidence

const (
	TurnManifestSchemaVersion    = "switchback.spike.turn_manifest.v1"
	NormalizedTraceSchemaVersion = "switchback.spike.normalized_trace.v1"
	KindReport                   = "report"
	KindRawACP                   = "raw-acp"
	KindTrace                    = "trace"
	KindPatch                    = "patch"
	HeaderBeforeTree             = "X-Switchback-Before-Tree"
	HeaderAfterTree              = "X-Switchback-After-Tree"
)

// TurnIdentity is the complete identity set copied into every normalized record.
type TurnIdentity struct {
	WorkerSessionID   string `json:"worker_session_id"`
	WorkerRunID       string `json:"worker_run_id"`
	RuntimeInstanceID string `json:"runtime_instance_id"`
	RuntimeGeneration uint32 `json:"runtime_generation"`
	AgentSessionID    string `json:"agent_session_id"`
	AgentTurnID       string `json:"agent_turn_id"`
	TraceID           string `json:"trace_id"`
}

type ObjectRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type TurnManifest struct {
	SchemaVersion     string      `json:"schema_version"`
	WorkerSessionID   string      `json:"worker_session_id"`
	WorkerRunID       string      `json:"worker_run_id"`
	RuntimeInstanceID string      `json:"runtime_instance_id"`
	RuntimeGeneration uint32      `json:"runtime_generation"`
	AgentSessionID    string      `json:"agent_session_id"`
	AgentTurnID       string      `json:"agent_turn_id"`
	TraceID           string      `json:"trace_id"`
	BeforeTree        string      `json:"before_tree"`
	AfterTree         string      `json:"after_tree"`
	Objects           []ObjectRef `json:"objects"`
}

// TreePair accompanies each upload so the Backend can require all four
// objects to agree before writing the run-level latest manifest.
type TreePair struct {
	BeforeTree string `json:"before_tree"`
	AfterTree  string `json:"after_tree"`
}
