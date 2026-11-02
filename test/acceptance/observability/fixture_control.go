package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type fixtureRecord struct {
	Instance string `json:"instance"`
	Key      string `json:"key"`
	Scope    struct {
		Nonce      string `json:"nonce"`
		Session    string `json:"worker_session_id"`
		Runtime    string `json:"runtime_id"`
		Generation uint32 `json:"runtime_generation"`
		Turn       string `json:"agent_turn_id"`
		Ordinal    int    `json:"call_ordinal"`
	} `json:"scope"`
	ExpectedID        string    `json:"expected_observation_id"`
	EnteredAt         time.Time `json:"entered_at"`
	RegisteredAt      time.Time `json:"registered_at"`
	ReleasedAt        time.Time `json:"released_at"`
	CompletedAt       time.Time `json:"completed_at"`
	ReaderCompletedAt time.Time `json:"reader_completed_at"`
	ReleaseDigest     string    `json:"release_digest"`
	Failure           string    `json:"failure"`
}

func validateCompletedReaders(ledger []fixtureRecord, expected []ExpectedModel) (bool, error) {
	ready := true
	for _, e := range expected {
		found := false
		for _, r := range ledger {
			if r.Key != e.FixtureKey {
				continue
			}
			if found || r.Instance != e.FixtureInstance || r.ExpectedID != e.ObservationID || r.Scope.Turn != e.TurnID || r.Failure != "" || !r.ReleasedAt.Equal(e.ReleasedAt) {
				return false, fmt.Errorf("released fixture reader identity or outcome invalid")
			}
			found = true
			if r.CompletedAt.IsZero() || r.ReaderCompletedAt.IsZero() {
				ready = false
			} else if r.CompletedAt.Before(r.ReleasedAt) || r.ReaderCompletedAt.Before(r.CompletedAt) {
				return false, fmt.Errorf("released fixture reader chronology invalid")
			}
		}
		if !found {
			return false, fmt.Errorf("released fixture reader missing")
		}
	}
	return ready, nil
}

func awaitCompletedReaders(ctx context.Context, project string, expected []ExpectedModel) error {
	if len(expected) == 0 {
		return fmt.Errorf("expected fixture readers missing")
	}
	for {
		raw, err := fixtureControl(ctx, project, expected[0].FixtureContainerID, "status", "", "")
		if err != nil {
			return err
		}
		var ledger []fixtureRecord
		if err = json.Unmarshal(raw, &ledger); err != nil {
			return err
		}
		ready, err := validateCompletedReaders(ledger, expected)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("released fixture readers not complete: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func exactServiceContainer(ctx context.Context, project, service string) (CapturedContainer, error) {
	var result CapturedContainer
	ids, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=com.docker.compose.service="+service, "--format", "{{.ID}}").Output()
	if err != nil || len(strings.Fields(string(ids))) != 1 {
		return result, fmt.Errorf("exact %s container not found", service)
	}
	id := strings.TrimSpace(string(ids))
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", inspectTemplate, id).Output()
	if err != nil {
		return result, fmt.Errorf("owned container inspect failed")
	}
	if json.Unmarshal(raw, &result) != nil || result.ID != id || result.Project != project || result.Service != service {
		return result, fmt.Errorf("owned container identity changed")
	}
	return result, nil
}

func fixtureControl(ctx context.Context, project, container, mode, key, digest string) ([]byte, error) {
	c, err := exactServiceContainer(ctx, project, "observation-fake-model")
	if err != nil {
		return nil, err
	}
	if c.ID != container || !c.Running {
		return nil, fmt.Errorf("fixture container replaced or stopped")
	}
	args := []string{"exec", container, "/usr/local/bin/fake-openai", "--control=" + mode}
	if key != "" {
		args = append(args, "--fixture-key", key)
	}
	if digest != "" {
		args = append(args, "--evidence-digest", digest)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(commandCtx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("fixture %s failed", mode)
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("fixture ledger too large")
	}
	return raw, nil
}

// releaseExpected freezes independent server-generated IDs and exact Backend
// terminal/settlement/idle facts before allowing any completed SSE event.
func releaseExpected(ctx context.Context, pool *pgxpool.Pool, project, output, nonce, container string, turns []string) ([]ExpectedModel, error) {
	raw, err := fixtureControl(ctx, project, container, "status", "", "")
	if err != nil {
		return nil, err
	}
	var ledger []fixtureRecord
	if err = json.Unmarshal(raw, &ledger); err != nil {
		return nil, err
	}
	selected := map[string]fixtureRecord{}
	for _, record := range ledger {
		if record.Scope.Nonce == nonce {
			if record.Scope.Ordinal != 1 || record.Failure != "" || record.ExpectedID == "" || !record.ReleasedAt.IsZero() {
				return nil, fmt.Errorf("fixture expected ledger invalid")
			}
			if _, ok := selected[record.Scope.Turn]; ok {
				return nil, fmt.Errorf("unexpected extra model call")
			}
			selected[record.Scope.Turn] = record
		}
	}
	if len(selected) != len(turns) {
		return nil, fmt.Errorf("expected model set missing or expanded")
	}
	proofs := make([]ExpectedModel, 0, len(turns))
	for _, turn := range turns {
		r, ok := selected[turn]
		if !ok {
			return nil, fmt.Errorf("fixture turn identity missing")
		}
		var terminal, settled time.Time
		var session, runtime, status string
		var generation uint32
		var idle bool
		err = pool.QueryRow(ctx, `SELECT t.terminal_at,st.completed_at,t.worker_session_id,st.runtime_instance_id,st.runtime_generation,t.status,s.active_worker_run_id IS NULL FROM agent_turns t JOIN worker_turn_settlements st USING(agent_turn_id) JOIN worker_sessions s ON s.worker_session_id=t.worker_session_id WHERE t.agent_turn_id=$1`, turn).Scan(&terminal, &settled, &session, &runtime, &generation, &status, &idle)
		if err != nil {
			return nil, fmt.Errorf("exact terminal/settlement proof: %w", err)
		}
		if status != "completed" || !idle || session != r.Scope.Session || runtime != r.Scope.Runtime || generation != r.Scope.Generation || terminal.Before(r.RegisteredAt) || settled.Before(terminal) {
			return nil, fmt.Errorf("late release requires exact completed terminal and resident idle scope")
		}
		var accepted int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM observation_candidates WHERE kind='model' AND convert_from(frame,'UTF8')::jsonb->>'observation_id'=$1`, r.ExpectedID).Scan(&accepted); err != nil {
			return nil, err
		}
		if accepted != 0 {
			return nil, fmt.Errorf("model accepted before controlled release")
		}
		proofs = append(proofs, ExpectedModel{FixtureContainerID: container, FixtureInstance: r.Instance, ObservationID: r.ExpectedID, TurnID: turn, FixtureKey: r.Key, RegisteredAt: r.RegisteredAt, TerminalAt: terminal, IdleOrNextAt: time.Now().UTC()})
	}
	frozen := time.Now().UTC()
	for i := range proofs {
		proofs[i].FrozenAt = frozen
	}
	// Immutable O_EXCL evidence write is a prerequisite to every release.
	frozenFile := struct {
		Project, Container, Nonce string
		Models                    []ExpectedModel
		Ledger                    []fixtureRecord
	}{project, container, nonce, proofs, ledger}
	digest := evidenceDigest(frozenFile)
	if err = writeEvidence(output, "expected-"+nonce+".json", frozenFile); err != nil {
		return nil, err
	}
	for i := range proofs {
		raw, err = fixtureControl(ctx, project, container, "release", proofs[i].FixtureKey, digest)
		if err != nil {
			return nil, err
		}
		var released fixtureRecord
		if json.Unmarshal(raw, &released) != nil || released.Instance != proofs[i].FixtureInstance || released.Key != proofs[i].FixtureKey || released.ExpectedID != proofs[i].ObservationID || released.ReleaseDigest != digest || released.ReleasedAt.IsZero() {
			return nil, fmt.Errorf("release acknowledgment identity invalid")
		}
		proofs[i].ReleasedAt = released.ReleasedAt
	}
	if err = writeEvidence(output, "released-"+nonce+".json", proofs); err != nil {
		return nil, err
	}
	fmt.Println(filepath.Join(output, "expected-"+nonce+".json"))
	return proofs, nil
}
