//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestTerminalApplyAndDomainReplayLockOrder(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().Add(-10 * time.Minute)
	prepared, stage := prepareTurnSettlementDomainCommit(t, pool, now)
	committedAt := now.Add(7*time.Minute + time.Second)
	domain := workerapp.TurnSettlementDomainService{Store: workerpostgres.NewTurnSettlementRepository(pool), Now: func() time.Time { return committedAt }}
	command := workerapp.CommitTurnSettlementDomainCommand{SettlementID: prepared.SettlementID, ExpectedRevision: prepared.Revision, LeaseOwnerID: prepared.LeaseOwnerID, LeaseFence: prepared.LeaseFence, Resident: stage}
	committed, err := domain.Commit(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Settlement.Phase != workerapp.TurnSettlementDomainCommitted {
		t.Fatalf("fixture phase=%s", committed.Settlement.Phase)
	}
	// This deliberately early Apply uses the public application service. It must
	// preserve the phase gate; a domain replay remains a valid competing request.
	authority := workerapp.RuntimeControlAuthority{WorkerSessionID: stage.WorkerSessionID, RuntimeInstanceID: stage.RuntimeInstanceID, RuntimeGeneration: stage.RuntimeGeneration}
	if err := pool.QueryRow(ctx, `SELECT channel_epoch,process_nonce,stream_attempt,owner_fence FROM runtime_control_channels WHERE runtime_instance_id=$1`, stage.RuntimeInstanceID).Scan(&authority.ChannelEpoch, &authority.ProcessNonce, &authority.StreamAttempt, &authority.OwnerFence); err != nil {
		t.Fatal(err)
	}
	apply := workerapp.ApplyRuntimeControlEventCommand{Authority: authority, Sequence: 1, MessageID: "domain-terminal-" + stage.AgentTurnID, IdempotencyKey: "domain-terminal-apply", Outcome: json.RawMessage(`{"status":"terminal_committed","effect_disposition":"commit"}`), TurnSettlement: turnSettlementApplyAuthority(committed.Settlement)}
	before := readTerminalSettlementApplySnapshot(t, pool, prepared.SettlementID, apply.MessageID)
	applyPool := lockOrderPool(t, pool, "terminal-domain-apply")
	replayPool := lockOrderPool(t, pool, "terminal-domain-replay")
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applyDone, replayDone := make(chan error, 1), make(chan error, 1)
	var applyStarted, replayStarted, drained bool
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !drained {
			if applyStarted {
				<-applyDone
			}
			if replayStarted {
				<-replayDone
			}
		}
	}()
	if _, err := blocker.Exec(ctx, `SELECT message_id FROM runtime_control_messages WHERE runtime_instance_id=$1 AND direction='event' AND sequence=$2 AND message_id=$3 FOR UPDATE`, stage.RuntimeInstanceID, apply.Sequence, apply.MessageID); err != nil {
		t.Fatal(err)
	}
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(applyPool), Now: func() time.Time { return committedAt.Add(time.Second) }}
	applyStarted = true
	go func() { _, err := journal.ApplyEvent(ctx, apply); applyDone <- err }()
	waitLockOrderQuery(t, pool, "terminal-domain-apply", "FROM runtime_control_messages")
	var applyPID int
	var messageBlocked bool
	if err := pool.QueryRow(ctx, `SELECT pid,$1=ANY(pg_blocking_pids(pid)) FROM pg_stat_activity WHERE application_name='terminal-domain-apply' AND wait_event_type='Lock'`, blocker.Conn().PgConn().PID()).Scan(&applyPID, &messageBlocked); err != nil || !messageBlocked {
		t.Fatalf("Apply exact message barrier=%t err=%v", messageBlocked, err)
	}
	t.Logf("catalog: Apply(pid=%d) -> exact terminal message owner(pid=%d)", applyPID, blocker.Conn().PgConn().PID())
	domain.Store = workerpostgres.NewTurnSettlementRepository(replayPool)
	var replay workerapp.CommitTurnSettlementDomainResult
	replayStarted = true
	go func() { var err error; replay, err = domain.Commit(ctx, command); replayDone <- err }()
	waitLockOrderQuery(t, pool, "terminal-domain-replay", "")
	var replayPID int
	var query string
	var replayBlocked bool
	if err := pool.QueryRow(ctx, `SELECT pid,query,$1=ANY(pg_blocking_pids(pid)) FROM pg_stat_activity WHERE application_name='terminal-domain-replay' AND wait_event_type='Lock'`, applyPID).Scan(&replayPID, &query, &replayBlocked); err != nil || !replayBlocked {
		t.Fatalf("domain replay blocked by Apply=%t err=%v", replayBlocked, err)
	}
	t.Logf("catalog: domain replay(pid=%d) -> Apply(pid=%d), query=%s", replayPID, applyPID, query)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	applyErr, replayErr := <-applyDone, <-replayDone
	drained = true
	for _, err := range []error{applyErr, replayErr} {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("terminal Apply/domain replay deadlock SQLSTATE=%s detail=%s apply=%v replay=%v", pgErr.Code, pgErr.Detail, applyErr, replayErr)
		}
	}
	if !errors.Is(applyErr, workerapp.ErrTurnSettlementPhaseConflict) || replayErr != nil {
		t.Fatalf("early Apply must reject phase and domain replay must succeed: apply=%v replay=%v", applyErr, replayErr)
	}
	if !strings.Contains(query, "SELECT settlement.settlement_id") {
		t.Fatalf("domain replay did not wait on settlement before graph: %s", query)
	}
	if !reflect.DeepEqual(replay, committed) {
		t.Fatalf("domain replay changed committed result: got=%+v want=%+v", replay, committed)
	}
	after := readTerminalSettlementApplySnapshot(t, pool, prepared.SettlementID, apply.MessageID)
	if after != before {
		t.Fatalf("phase rejection/replay mutated durable state: before=%+v after=%+v", before, after)
	}
}

func TestTerminalApplyPreservesSettlementAuthority(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	f := prepareTerminalSettlementApplyFixture(t, pool, time.Now().Add(-10*time.Minute), "apply-authority")
	now := f.Settlement.UpdatedAt.Add(time.Second)
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return now }}
	before := readTerminalSettlementApplySnapshot(t, pool, f.Settlement.SettlementID, f.MessageID)
	cases := []struct {
		name   string
		change func(*workerapp.ApplyRuntimeControlEventCommand)
		want   error
	}{
		{"different_settlement", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.TurnSettlement.SettlementID = "other-settlement" }, workerapp.ErrTurnSettlementLeaseStale},
		{"stale_revision", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.TurnSettlement.ExpectedRevision++ }, workerapp.ErrTurnSettlementLeaseStale},
		{"stale_settlement_owner", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.TurnSettlement.LeaseOwnerID = "other-owner" }, workerapp.ErrTurnSettlementLeaseStale},
		{"stale_settlement_fence", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.TurnSettlement.LeaseFence++ }, workerapp.ErrTurnSettlementLeaseStale},
		{"expired_settlement_lease", func(c *workerapp.ApplyRuntimeControlEventCommand) {
			now = f.Settlement.LeaseExpiresAt.Add(time.Microsecond)
		}, workerapp.ErrTurnSettlementLeaseStale},
		{"stale_channel_owner", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.Authority.OwnerFence++ }, workerapp.ErrRuntimeControlStaleOwner},
		{"wrong_session", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.Authority.WorkerSessionID = "other-session" }, workerapp.ErrRuntimeControlIdentityConflict},
		{"wrong_generation", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.Authority.RuntimeGeneration++ }, workerapp.ErrRuntimeControlIdentityConflict},
		{"wrong_epoch", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.Authority.ChannelEpoch++ }, workerapp.ErrRuntimeControlIdentityConflict},
		{"wrong_message", func(c *workerapp.ApplyRuntimeControlEventCommand) { c.MessageID = "other-terminal" }, workerapp.ErrRuntimeControlIdentityConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now = f.Settlement.UpdatedAt.Add(time.Second)
			command := f.Apply
			command.TurnSettlement = turnSettlementApplyAuthority(f.Settlement)
			tc.change(&command)
			if _, err := journal.ApplyEvent(ctx, command); !errors.Is(err, tc.want) {
				t.Fatalf("Apply error=%v want=%v", err, tc.want)
			}
			after := readTerminalSettlementApplySnapshot(t, pool, f.Settlement.SettlementID, f.MessageID)
			if after != before {
				t.Fatalf("rejected Apply mutated state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestTerminalApplyMissingSettlementFailsClosed(t *testing.T) {
	pool := postgrestest.Open(t)
	ctx := context.Background()
	f := prepareTerminalSettlementApplyFixture(t, pool, time.Now().Add(-10*time.Minute), "apply-missing")
	// Adversarial durable-state fixture: remove only the settlement. Keep the
	// original accepted journal envelope/fingerprint bytes intact.
	if _, err := pool.Exec(ctx, `DELETE FROM worker_turn_settlements WHERE settlement_id=$1`, f.Settlement.SettlementID); err != nil {
		t.Fatal(err)
	}
	var before, after string
	snapshot := func(target *string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('event',to_jsonb(message),'channel',to_jsonb(channel))::text FROM runtime_control_messages message JOIN runtime_control_channels channel USING(runtime_instance_id) WHERE message.message_id=$1 AND message.direction='event'`, f.MessageID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	snapshot(&before)
	f.Apply.TurnSettlement = turnSettlementApplyAuthority(f.Settlement)
	journal := workerapp.RuntimeControlJournalService{Store: workerpostgres.NewRuntimeControlJournal(pool), Now: func() time.Time { return f.Settlement.UpdatedAt.Add(time.Second) }}
	if _, err := journal.ApplyEvent(ctx, f.Apply); !errors.Is(err, workerapp.ErrTurnSettlementConflict) {
		t.Fatalf("missing settlement Apply error=%v", err)
	}
	snapshot(&after)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_turn_settlements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if before != after || count != 0 {
		t.Fatalf("missing settlement Apply mutated inbox/channel or created settlement: count=%d", count)
	}
}
