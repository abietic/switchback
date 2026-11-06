package postgres

import (
	"encoding/json"
	"fmt"
	"time"

	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

// Compare text without casting untrusted JSON fields. Read the diagnostic and
// its current owner binding in the same SELECT snapshot, never a later query.
const currentTaskRuntimeDispatchWaitSQL = `CASE WHEN status='queued' AND current_worker_run_id IS NULL
 AND runtime_dispatch_wait->>'task_revision'=revision::text
 AND COALESCE(runtime_dispatch_wait->>'worker_session_id','')=COALESCE(worker_session_id,'')
 AND ((worker_session_id IS NULL AND session_queue_sequence IS NULL
       AND NOT runtime_dispatch_wait ? 'queue_sequence' AND NOT runtime_dispatch_wait ? 'queue_revision')
      OR EXISTS (SELECT 1 FROM session_task_queue_entries q WHERE q.task_id=tasks.task_id
       AND q.worker_session_id=tasks.worker_session_id AND q.session_queue_sequence=tasks.session_queue_sequence
       AND q.status IN ('pending','permit_issued')
       AND runtime_dispatch_wait->>'queue_sequence'=q.session_queue_sequence::text
       AND runtime_dispatch_wait->>'queue_revision'=q.queue_revision::text))
 THEN runtime_dispatch_wait ELSE NULL END`

type publicRuntimeDispatchWait struct {
	Reason     string `json:"reason"`
	ObservedAt string `json:"observed_at"`
}

func decodeRuntimeDispatchWait(raw json.RawMessage) (*publicRuntimeDispatchWait, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wait taskapp.RuntimeDispatchWait
	if err := json.Unmarshal(raw, &wait); err != nil {
		return nil, err
	}
	if err := wait.ValidateBinding(); err != nil {
		return nil, err
	}
	if wait.ObservedAt.IsZero() {
		return nil, fmt.Errorf("runtime dispatch wait lacks server observation")
	}
	return &publicRuntimeDispatchWait{Reason: wait.Reason, ObservedAt: wait.ObservedAt.UTC().Format(time.RFC3339Nano)}, nil
}

type taskIndexItem struct {
	homeTaskItem
	RuntimeDispatchWait *publicRuntimeDispatchWait `json:"runtime_dispatch_wait,omitempty"`
}
