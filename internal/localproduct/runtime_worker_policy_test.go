package localproduct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	workerpostgres "github.com/abietic/switchback/internal/workerruntime/adapter/postgres"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// Keep the real factory/startup seams under test: a correctly tested decorator
// that is never wired to the serving Hub would leave durable reconnect open.
func TestWorkerPolicyProductionWiring(t *testing.T) {
	check := func(path, function string, expressions ...string) {
		t.Helper()
		set := token.NewFileSet()
		f, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != function {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n.(type) {
				case *ast.KeyValueExpr, *ast.CallExpr, *ast.AssignStmt:
					var out bytes.Buffer
					if err := format.Node(&out, set, n); err != nil {
						t.Fatal(err)
					}
					found[out.String()] = true
				}
				return true
			})
		}
		for _, expression := range expressions {
			if !found[expression] {
				t.Errorf("%s lost production policy wiring: %s", function, expression)
			}
		}
	}
	check("runtime_runner.go", "newRuntimeRunner",
		"inspect: provisioner.VerifyWorkerPolicy",
		"handoff: provisioner.LookupWorkerPolicyHandoff",
		"loadWorkerPolicyOwner(ctx, pool, runtimeID)",
		"journal := workerapp.RuntimeControlJournalService{Store: workerPolicy.guardJournal(journalStore), Now: now}",
		"hub := controlgrpc.NewHubWithJournal(journal)",
		"workerPolicy: workerPolicy")
	check("runtime_startup.go", "restoreStartupRuntimeCandidate", "prepare: runner.restorePolicyVerifiedLiveRuntime")
	check("runtime_worker_policy.go", "restorePolicyVerifiedLiveRuntime", "runner.workerPolicy.prepare(ctx, request, runner.environment.RestoreLiveRuntime)")
}

func workerPolicyTestTarget() workerapp.CreatedRuntimeIdentity {
	return workerapp.CreatedRuntimeIdentity{WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1", Generation: 1, ContainerID: "cid-1"}
}

func TestWorkerPolicyOwnerResolution(t *testing.T) {
	exact := workerPolicyTestTarget()
	pin := workerPolicyResident{Session: exact.WorkerSessionID, Runtime: exact.RuntimeInstanceID, Generation: exact.Generation, Container: exact.ContainerID, Origin: "reserved"}
	for _, tc := range []struct {
		name    string
		change  func(*workerPolicyOwner)
		allowed bool
	}{
		{"saved live", func(*workerPolicyOwner) {}, true},
		{"legacy idle no ledger", func(*workerPolicyOwner) {}, true},
		{"later committed pin before Activate", func(o *workerPolicyOwner) {
			o.status = "starting"
			o.identity.ContainerID = ""
			o.residents = []workerPolicyResident{pin}
		}, true},
		{"saved and pin match", func(o *workerPolicyOwner) { o.residents = []workerPolicyResident{pin} }, true},
		{"initial missing pin", func(o *workerPolicyOwner) { o.status = "starting"; o.identity.ContainerID = "" }, false},
		{"initial legacy pin", func(o *workerPolicyOwner) {
			o.status = "starting"
			o.identity.ContainerID = ""
			p := pin
			p.Origin = "legacy"
			o.residents = []workerPolicyResident{p}
		}, false},
		{"active no saved CID", func(o *workerPolicyOwner) { o.identity.ContainerID = ""; o.residents = []workerPolicyResident{pin} }, false},
		{"conflicting CID", func(o *workerPolicyOwner) { p := pin; p.Container = "other"; o.residents = []workerPolicyResident{p} }, false},
		{"empty pin", func(o *workerPolicyOwner) { p := pin; p.Container = ""; o.residents = []workerPolicyResident{p} }, false},
		{"removed pin", func(o *workerPolicyOwner) { p := pin; p.Removed = true; o.residents = []workerPolicyResident{p} }, false},
		{"pin generation mismatch", func(o *workerPolicyOwner) { p := pin; p.Generation++; o.residents = []workerPolicyResident{p} }, false},
		{"pin session mismatch", func(o *workerPolicyOwner) { p := pin; p.Session = "other"; o.residents = []workerPolicyResident{p} }, false},
		{"pin runtime mismatch", func(o *workerPolicyOwner) { p := pin; p.Runtime = "other"; o.residents = []workerPolicyResident{p} }, false},
		{"multiple pins", func(o *workerPolicyOwner) { o.residents = []workerPolicyResident{pin, pin} }, false},
		{"owner session mismatch", func(o *workerPolicyOwner) { o.identity.WorkerSessionID = "other" }, false},
		{"owner generation mismatch", func(o *workerPolicyOwner) { o.identity.Generation++ }, false},
		{"owner runtime mismatch", func(o *workerPolicyOwner) { o.identity.RuntimeInstanceID = "other" }, false},
		{"terminated owner", func(o *workerPolicyOwner) { o.status = "terminated" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := workerPolicyOwner{identity: exact, status: "active"}
			tc.change(&owner)
			request := exact
			request.ContainerID = ""
			got, err := owner.target(request, nil)
			if (err == nil) != tc.allowed {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if tc.allowed && got != exact {
				t.Fatalf("lost exact CID: %+v", got)
			}
		})
	}
	owner := workerPolicyOwner{identity: exact, status: "active"}
	wrong := exact
	wrong.ContainerID = "different"
	if _, err := owner.target(wrong, nil); err == nil {
		t.Fatal("caller expected CID ignored")
	}
}

// An embedded nil journal makes any unexpected mutating/replay method panic;
// the only supported operation is the original durable Claim authority.
type workerPolicyTestJournal struct {
	workerapp.RuntimeControlJournal
	calls       int
	record      workerapp.ClaimRuntimeControlStreamRecord
	err         error
	plan        workerapp.RuntimeControlReplayPlan
	successful  bool
	disconnects int
}

func (j *workerPolicyTestJournal) ClaimRuntimeControlStream(_ context.Context, r workerapp.ClaimRuntimeControlStreamRecord) (workerapp.RuntimeControlReplayPlan, error) {
	j.calls++
	j.record = r
	if j.successful {
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(strings.Repeat("a", 43))))
		if r.CredentialDigest != digest {
			return workerapp.RuntimeControlReplayPlan{}, workerapp.ErrRuntimeControlCredentialConflict
		}
		c := r.Command
		j.plan.Channel = workerapp.RuntimeControlChannelState{WorkerSessionID: c.WorkerSessionID, RuntimeInstanceID: c.RuntimeInstanceID, RuntimeGeneration: c.RuntimeGeneration, ChannelEpoch: c.ChannelEpoch, ProcessNonce: c.ProcessNonce, StreamAttempt: c.StreamAttempt, OwnerID: c.OwnerID, OwnerFence: 1, ResumeClaimRevision: 1, CommandCommittedDigest: contractsv1.RuntimeControlCursorGenesisDigest, EventCommittedDigest: contractsv1.RuntimeControlCursorGenesisDigest}
		if len(j.plan.PendingCommands) > 0 {
			j.plan.Channel.CommandCommittedSequence = 1
		}
	}
	return j.plan, j.err
}

func TestWorkerPolicyJournalAndRestoreAdmission(t *testing.T) {
	target := workerPolicyTestTarget()
	originalDenial := errors.New("original capacity authorization denied")
	record := workerapp.ClaimRuntimeControlStreamRecord{Command: workerapp.ClaimRuntimeControlStreamCommand{WorkerSessionID: target.WorkerSessionID, RuntimeInstanceID: target.RuntimeInstanceID, RuntimeGeneration: target.Generation, Credential: "unchanged", OwnerID: "owner"}, CredentialDigest: "durable-digest", ClaimedAt: time.Now()}
	for _, cause := range []string{"missing", "unconfined", "tampered", "unknown", "lookup error", "nil admission", "nil inspector", "nil lookup", "match"} {
		t.Run(cause, func(t *testing.T) {
			denied := errors.New(cause)
			var order []string
			admission := &workerPolicyAdmission{lookup: func(_ context.Context, runtime string) (workerPolicyOwner, error) {
				order = append(order, "lookup")
				if runtime != target.RuntimeInstanceID {
					t.Fatal("wrong lookup")
				}
				if cause == "lookup error" {
					return workerPolicyOwner{}, denied
				}
				return workerPolicyOwner{identity: target, status: "active"}, nil
			}, inspect: func(_ context.Context, got workerapp.CreatedRuntimeIdentity) error {
				order = append(order, "policy")
				if got != target {
					t.Fatal("wrong physical identity")
				}
				if cause != "match" {
					return denied
				}
				return nil
			}}
			switch cause {
			case "nil admission":
				admission = nil
			case "nil inspector":
				admission.inspect = nil
			case "nil lookup":
				admission.lookup = nil
			}
			store := &workerPolicyTestJournal{err: originalDenial}
			_, err := admission.guardJournal(store).ClaimRuntimeControlStream(context.Background(), record)
			if cause == "match" {
				if err != originalDenial || store.calls != 1 || !reflect.DeepEqual(store.record, record) {
					t.Fatalf("original authority changed: %v calls=%d", err, store.calls)
				}
			} else if err == nil || store.calls != 0 {
				t.Fatalf("policy denial reached durable Claim: %v calls=%d", err, store.calls)
			}
			restored := false
			request := liveRuntimeRestoreRequest{WorkerSessionID: target.WorkerSessionID, RuntimeInstanceID: target.RuntimeInstanceID, RuntimeGeneration: target.Generation}
			_, err = admission.prepare(context.Background(), request, func(_ context.Context, got liveRuntimeRestoreRequest) (preparedRuntime, error) {
				restored = true
				order = append(order, "credential restore")
				if !reflect.DeepEqual(got, request) {
					t.Fatal("restore request changed")
				}
				return preparedRuntime{}, originalDenial
			})
			if cause == "match" {
				if err != originalDenial || !restored || !reflect.DeepEqual(order, []string{"lookup", "policy", "lookup", "policy", "credential restore"}) {
					t.Fatalf("restore order=%v err=%v", order, err)
				}
			} else if err == nil || restored {
				t.Fatal("policy denial restored credential")
			}
		})
	}
}

// Drives the real Server.Connect with a durable credential and no process-local
// RegisterCredential/RestoreLiveRuntime. Both an active replay graph and legacy
// idle plan must remain unreachable when physical policy is denied.
func TestWorkerPolicyServerConnectBeforeStartupRestore(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, policy := range []string{"missing", "unconfined", "tampered", "unknown", "match"} {
			t.Run(fmt.Sprintf("pending=%v/%s", pending, policy), func(t *testing.T) {
				target := workerPolicyTestTarget()
				var physical int
				admission := &workerPolicyAdmission{lookup: func(context.Context, string) (workerPolicyOwner, error) {
					return workerPolicyOwner{identity: target, status: "active"}, nil
				}, inspect: func(context.Context, workerapp.CreatedRuntimeIdentity) error {
					physical++
					if policy != "match" {
						return errors.New("unverified Worker policy")
					}
					return nil
				}}
				store := &workerPolicyTestJournal{successful: true}
				if pending {
					envelope := contractsv1.RuntimeControlEnvelope{SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: "pending-start", Kind: "command", WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, AgentSessionID: "agent-1", AgentTurnID: "turn-1", ChannelEpoch: 1, Sequence: 1, IdempotencyKey: "start-key", MessageType: "worker.start", Payload: json.RawMessage(`{}`), SentAt: time.Now().UTC()}
					raw, marshalErr := json.Marshal(envelope)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					store.plan.PendingCommands = []workerapp.RuntimeControlMessage{{MessageID: envelope.MessageID, IdempotencyKey: envelope.IdempotencyKey, Sequence: 1, Envelope: raw}}
				}
				journal := workerapp.RuntimeControlJournalService{Store: admission.guardJournal(store), Now: time.Now}
				hub := controlgrpc.NewHubWithJournal(journal)
				stream := &workerPolicyTestStream{ctx: metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+strings.Repeat("a", 43))), frame: &contractsv1.RuntimeControlFrame{
					FrameType: contractsv1.RuntimeControlFrameTypeResumeHello, ResumeHello: &contractsv1.RuntimeControlResumeHello{
						WorkerSessionID: contractsv1.WorkerSessionID(target.WorkerSessionID), RuntimeInstanceID: contractsv1.RuntimeInstanceID(target.RuntimeInstanceID), RuntimeGeneration: 1, ChannelEpoch: 1, ShimProcessNonce: "nonce", StreamAttempt: 1, WorkerTXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest, WorkerRXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest,
					},
				}}
				err := controlgrpc.NewServer(hub).Connect(stream)
				if physical != 1 {
					t.Fatalf("policy skipped: %v", err)
				}
				if policy == "match" {
					wantFrames := 1
					if pending {
						wantFrames = 2
					}
					if err != nil || store.calls != 1 || store.disconnects != 1 || len(stream.sent) != wantFrames || stream.sent[0].ResumeAck == nil || !stream.sent[0].ResumeAck.Accepted {
						t.Fatalf("valid original graph did not reach replay: err=%v claims=%d detach=%d frames=%+v", err, store.calls, store.disconnects, stream.sent)
					}
					if pending && stream.sent[1].Envelope.MessageID != "pending-start" {
						t.Fatal("pending Start not replayed")
					}
				} else if err == nil || store.calls != 0 || store.disconnects != 0 || len(stream.sent) != 0 {
					t.Fatalf("policy denial changed channel or emitted frames: err=%v claims=%d detach=%d frames=%d", err, store.calls, store.disconnects, len(stream.sent))
				}

			})
		}
	}
}

type workerPolicyTestStream struct {
	grpc.ServerStream
	ctx   context.Context
	frame *contractsv1.RuntimeControlFrame
	sent  []*contractsv1.RuntimeControlFrame
}

func (s *workerPolicyTestStream) Context() context.Context { return s.ctx }
func (s *workerPolicyTestStream) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	if s.frame == nil {
		return nil, io.EOF
	}
	f := s.frame
	s.frame = nil
	return f, nil
}
func (s *workerPolicyTestStream) SendFrame(f *contractsv1.RuntimeControlFrame) error {
	s.sent = append(s.sent, f)
	return nil
}
func (s *workerPolicyTestStream) Recv() (*contractsv1.RuntimeControlEnvelope, error) {
	panic("unexpected legacy receive")
}
func (s *workerPolicyTestStream) Send(*contractsv1.RuntimeControlEnvelope) error {
	panic("unexpected envelope/replay")
}

// Exercise the actual read-only SQL lookup without opening a database.
type workerPolicyTestQuery struct {
	t   *testing.T
	row pgx.Row
}

func (q workerPolicyTestQuery) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	q.t.Helper()
	if !strings.HasPrefix(query, "SELECT ") || strings.Contains(query, "FOR UPDATE") || strings.Contains(query, "UPDATE ") || strings.Contains(query, "INSERT ") || !strings.Contains(query, "c.runtime_instance_id=i.runtime_instance_id") || !strings.Contains(query, "c.worker_session_id=i.worker_session_id AND c.generation=i.generation") || !reflect.DeepEqual(args, []any{"runtime-1"}) {
		q.t.Fatalf("owner lookup lost read-only conflict coverage: %s", query)
	}
	return q.row
}

type workerPolicyTestRow struct {
	raw []byte
	err error
}

func (r workerPolicyTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = "session-1"
	*dest[1].(*string) = "runtime-1"
	*dest[2].(*uint64) = 1
	*dest[3].(*string) = "starting"
	*dest[4].(*string) = ""
	*dest[5].(*[]byte) = r.raw
	return nil
}
func TestWorkerPolicyReadOnlyOwnerLookup(t *testing.T) {
	pin := workerPolicyResident{Session: "session-1", Runtime: "runtime-1", Generation: 1, Container: "cid-1", Origin: "reserved"}
	raw, _ := json.Marshal([]workerPolicyResident{pin})
	owner, err := loadWorkerPolicyOwner(context.Background(), workerPolicyTestQuery{t: t, row: workerPolicyTestRow{raw: raw}}, "runtime-1")
	if err != nil {
		t.Fatal(err)
	}
	request := workerPolicyTestTarget()
	request.ContainerID = ""
	if got, err := owner.target(request, nil); err != nil || got != workerPolicyTestTarget() {
		t.Fatalf("initial committed pin=%+v %v", got, err)
	}
	for _, row := range []workerPolicyTestRow{{err: pgx.ErrNoRows}, {err: errors.New("database unavailable")}, {raw: []byte("bad json")}} {
		if _, err := loadWorkerPolicyOwner(context.Background(), workerPolicyTestQuery{t: t, row: row}, "runtime-1"); err == nil {
			t.Fatal("unknown lookup allowed")
		}
	}
}

func (j *workerPolicyTestJournal) DisconnectRuntimeControl(context.Context, workerapp.RuntimeControlAuthority, time.Time) (workerapp.RuntimeControlChannelState, error) {
	j.disconnects++
	return workerapp.RuntimeControlChannelState{}, nil
}

func TestWorkerPolicyStartupDenialPreservesOwner(t *testing.T) {
	target := workerPolicyTestTarget()
	for _, reason := range []string{"missing", "unconfined", "tampered", "unknown"} {
		t.Run(reason, func(t *testing.T) {
			denied := errors.New("policy " + reason)
			admission := &workerPolicyAdmission{lookup: func(context.Context, string) (workerPolicyOwner, error) {
				return workerPolicyOwner{identity: target, status: "active"}, nil
			}, inspect: func(context.Context, workerapp.CreatedRuntimeIdentity) error { return denied }}
			candidate := startupRuntimeCandidate{ContainerID: target.ContainerID, Restore: liveRuntimeRestoreRequest{WorkerSessionID: target.WorkerSessionID, RuntimeInstanceID: target.RuntimeInstanceID, RuntimeGeneration: target.Generation}, RuntimeRevision: 7, SessionRevision: 8}
			before := candidate
			forbidden := func() { t.Fatal("policy denial attempted restore/observation/channel mutation/recovery") }
			result, err := restoreStartupRuntime(context.Background(), candidate, startupRestoreDependencies{
				prepare: func(ctx context.Context, r liveRuntimeRestoreRequest) (preparedRuntime, error) {
					return admission.prepare(ctx, r, func(context.Context, liveRuntimeRestoreRequest) (preparedRuntime, error) {
						forbidden()
						return preparedRuntime{}, nil
					})
				},
				inspect: func(context.Context, string) (workerapp.RuntimeInspection, error) {
					forbidden()
					return workerapp.RuntimeInspection{}, nil
				},
				waitEvents: func(context.Context, controlgrpc.RuntimeIdentity) (<-chan workerapp.WorkerControlEvent, error) {
					forbidden()
					return nil, nil
				},
				markSuspect:    func(context.Context, startupRuntimeCandidate) error { forbidden(); return nil },
				fence:          func(context.Context, string, uint64, string) error { forbidden(); return nil },
				fenceIfNotLive: func(context.Context, string, uint64, string, time.Time) (bool, error) { forbidden(); return false, nil },
				openFailure: func(context.Context, workerpostgres.DetectedRuntimeFailure) (bool, error) {
					forbidden()
					return false, nil
				},
				now: time.Now,
			}, defaultStartupReconnectPolicy())
			if !errors.Is(err, denied) || result.Reconnected || result.Deferred || !reflect.DeepEqual(candidate, before) {
				t.Fatalf("denial changed startup owner: %+v %v", result, err)
			}
		})
	}
	// Production wrapper must fail closed even before any environment exists.
	if _, err := (&runtimeRunner{}).restorePolicyVerifiedLiveRuntime(context.Background(), liveRuntimeRestoreRequest{}); err == nil {
		t.Fatal("nil admission restored environment")
	}
}

// Initial authorization requires both durable CIDs empty. A verified Create
// handoff is available before Start; no reconciliation has written a pin yet.
func TestWorkerPolicyInitialDoubleEmptyReachesOriginalJournal(t *testing.T) {
	exact := workerPolicyTestTarget()
	owner := workerPolicyOwner{identity: exact, status: "starting", residents: []workerPolicyResident{{Session: exact.WorkerSessionID, Runtime: exact.RuntimeInstanceID, Generation: exact.Generation, Origin: "reserved"}}}
	owner.identity.ContainerID = ""
	looked, verified := 0, 0
	admission := &workerPolicyAdmission{
		lookup: func(context.Context, string) (workerPolicyOwner, error) { return owner, nil },
		handoff: func(request workerapp.CreatedRuntimeIdentity) (workerapp.CreatedRuntimeIdentity, bool, error) {
			looked++
			return exact, true, nil
		},
		inspect: func(_ context.Context, target workerapp.CreatedRuntimeIdentity) error {
			verified++
			if target != exact {
				t.Fatalf("lost exact Create identity: %+v", target)
			}
			return nil
		},
	}
	original := errors.New("independent journal denial")
	journal := &workerPolicyTestJournal{err: original}
	record := workerapp.ClaimRuntimeControlStreamRecord{Command: workerapp.ClaimRuntimeControlStreamCommand{WorkerSessionID: exact.WorkerSessionID, RuntimeInstanceID: exact.RuntimeInstanceID, RuntimeGeneration: exact.Generation}}
	for retry := 0; retry < 2; retry++ {
		_, err := admission.guardJournal(journal).ClaimRuntimeControlStream(context.Background(), record)
		if err != original || journal.calls != retry+1 {
			t.Fatalf("double-empty first Claim blocked before original journal: err=%v claims=%d", err, journal.calls)
		}
	}
	if looked != 2 || verified != 2 || owner.identity.ContainerID != "" || owner.residents[0].Container != "" {
		t.Fatalf("handoff consumed or pin written: lookup=%d verify=%d owner=%+v", looked, verified, owner)
	}
}

func TestWorkerPolicyInitialHandoffConflictsFailClosed(t *testing.T) {
	exact := workerPolicyTestTarget()
	for _, tc := range []struct {
		name   string
		mutate func(*workerPolicyOwner, *workerapp.CreatedRuntimeIdentity)
		found  bool
	}{
		{"fresh backend missing handoff", func(*workerPolicyOwner, *workerapp.CreatedRuntimeIdentity) {}, false},
		{"empty Create CID", func(_ *workerPolicyOwner, h *workerapp.CreatedRuntimeIdentity) { h.ContainerID = "" }, true},
		{"handoff wrong session", func(_ *workerPolicyOwner, h *workerapp.CreatedRuntimeIdentity) { h.WorkerSessionID = "other" }, true},
		{"handoff wrong runtime", func(_ *workerPolicyOwner, h *workerapp.CreatedRuntimeIdentity) { h.RuntimeInstanceID = "other" }, true},
		{"handoff wrong generation", func(_ *workerPolicyOwner, h *workerapp.CreatedRuntimeIdentity) { h.Generation++ }, true},
		{"missing resident association", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents = nil }, true},
		{"resident not reserved", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents[0].Origin = "legacy" }, true},
		{"resident removed", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents[0].Removed = true }, true},
		{"resident wrong session", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents[0].Session = "other" }, true},
		{"resident wrong runtime", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents[0].Runtime = "other" }, true},
		{"resident wrong generation", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents[0].Generation++ }, true},
		{"owner not starting", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.status = "active" }, true},
		{"durable CID conflict", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) {
			o.identity.ContainerID = "other"
			o.residents[0].Container = "other"
		}, true},
		{"resident CID conflict", func(o *workerPolicyOwner, _ *workerapp.CreatedRuntimeIdentity) { o.residents[0].Container = "other" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := workerPolicyOwner{identity: exact, status: "starting", residents: []workerPolicyResident{{Session: exact.WorkerSessionID, Runtime: exact.RuntimeInstanceID, Generation: exact.Generation, Origin: "reserved"}}}
			owner.identity.ContainerID = ""
			handoff := exact
			tc.mutate(&owner, &handoff)
			admission := &workerPolicyAdmission{lookup: func(context.Context, string) (workerPolicyOwner, error) { return owner, nil }, handoff: func(workerapp.CreatedRuntimeIdentity) (workerapp.CreatedRuntimeIdentity, bool, error) {
				return handoff, tc.found, nil
			}, inspect: func(context.Context, workerapp.CreatedRuntimeIdentity) error {
				t.Fatal("conflicting owner reached physical inspect")
				return nil
			}}
			journal := &workerPolicyTestJournal{}
			_, err := admission.guardJournal(journal).ClaimRuntimeControlStream(context.Background(), workerapp.ClaimRuntimeControlStreamRecord{Command: workerapp.ClaimRuntimeControlStreamCommand{WorkerSessionID: exact.WorkerSessionID, RuntimeInstanceID: exact.RuntimeInstanceID, RuntimeGeneration: exact.Generation}})
			if err == nil || journal.calls != 0 {
				t.Fatalf("unknown/conflicting identity reached journal: %v calls=%d", err, journal.calls)
			}
		})
	}
}

// No Reconcile/ObservePool seam exists in this fixture. The first normal
// double-empty owner uses its verified Create CID, finishes ResumeReady, and
// delivers runtime.ready to the original journal before any activation step.
func TestWorkerPolicyInitialServerConnectReachesRuntimeReadyWithoutReconcile(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(fmt.Sprintf("handoff=%v", available), func(t *testing.T) {
			exact := workerPolicyTestTarget()
			owner := workerPolicyOwner{identity: exact, status: "starting", residents: []workerPolicyResident{{Session: exact.WorkerSessionID, Runtime: exact.RuntimeInstanceID, Generation: exact.Generation, Origin: "reserved"}}}
			owner.identity.ContainerID = ""
			handoffs, physical := 0, 0
			admission := &workerPolicyAdmission{lookup: func(context.Context, string) (workerPolicyOwner, error) { return owner, nil }, handoff: func(target workerapp.CreatedRuntimeIdentity) (workerapp.CreatedRuntimeIdentity, bool, error) {
				handoffs++
				if target != owner.identity {
					t.Fatal("handoff lookup did not use trusted owner")
				}
				return exact, available, nil
			}, inspect: func(_ context.Context, target workerapp.CreatedRuntimeIdentity) error {
				physical++
				if target != exact {
					t.Fatal("physical verifier lost actual Create CID")
				}
				return nil
			}}
			store := &workerPolicyInitialJournal{workerPolicyTestJournal: workerPolicyTestJournal{successful: true}}
			hub := controlgrpc.NewHubWithJournal(workerapp.RuntimeControlJournalService{Store: admission.guardJournal(store), Now: time.Now})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			stream := &workerPolicyInitialStream{workerPolicyTestStream: workerPolicyTestStream{ctx: metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+strings.Repeat("a", 43)))}, incoming: make(chan *contractsv1.RuntimeControlFrame, 4)}
			stream.incoming <- &contractsv1.RuntimeControlFrame{FrameType: contractsv1.RuntimeControlFrameTypeResumeHello, ResumeHello: &contractsv1.RuntimeControlResumeHello{WorkerSessionID: "session-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, ChannelEpoch: 1, ShimProcessNonce: "nonce", StreamAttempt: 1, WorkerTXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest, WorkerRXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest}}
			err := controlgrpc.NewServer(hub).Connect(stream)
			if !available {
				if err == nil || handoffs != 1 || physical != 0 || store.calls != 0 || store.completed != 0 || store.disconnects != 0 || len(stream.sent) != 0 {
					t.Fatalf("fresh backend changed owner/control without CID: err=%v handoff=%d physical=%d claims=%d", err, handoffs, physical, store.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if handoffs != 1 || physical != 1 || store.calls != 1 || store.completed != 1 || store.ready.MessageType != "runtime.ready" || store.acks != 1 || len(stream.sent) != 2 || stream.sent[0].ResumeAck == nil || !stream.sent[0].ResumeAck.Accepted || stream.sent[1].ResumeComplete == nil {
				t.Fatalf("first Claim/ready flow incomplete: handoff=%d physical=%d claim=%d complete=%d ready=%+v ack=%d frames=%d", handoffs, physical, store.calls, store.completed, store.ready, store.acks, len(stream.sent))
			}
			if owner.identity.ContainerID != "" || owner.residents[0].Container != "" {
				t.Fatal("policy admission wrote a durable pin")
			}
		})
	}
}

type workerPolicyInitialJournal struct {
	workerPolicyTestJournal
	completed int
	ready     contractsv1.RuntimeControlEnvelope
	acks      int
}

func (j *workerPolicyInitialJournal) CompleteRuntimeControlResume(_ context.Context, record workerapp.CompleteRuntimeControlResumeRecord) (workerapp.RuntimeControlChannelState, error) {
	j.completed++
	state := j.plan.Channel
	if record.Command.Ready.ServerConnectionID != state.OwnerID {
		return state, workerapp.ErrRuntimeControlIdentityConflict
	}
	state.ResumeCompletedRevision = 2
	return state, nil
}
func (j *workerPolicyInitialJournal) AcceptRuntimeControlEvent(_ context.Context, record workerapp.AcceptRuntimeControlEventRecord) (workerapp.RuntimeControlEventAcceptance, error) {
	j.ready = record.DecodedEnvelope
	// This fixture proves admission/transport order, not durable event application.
	// Avoid a fake consumer: duplicate acceptance needs only the transport ACK.
	return workerapp.RuntimeControlEventAcceptance{Message: workerapp.RuntimeControlMessage{Duplicate: true}}, nil
}
func (j *workerPolicyInitialJournal) ReserveRuntimeControlCommand(context.Context, workerapp.ReserveRuntimeControlCommandRecord) (workerapp.RuntimeControlMessage, error) {
	j.acks++
	return workerapp.RuntimeControlMessage{}, nil
}
func (j *workerPolicyInitialJournal) LoadRuntimeControlReplayPlan(context.Context, workerapp.RuntimeControlAuthority) (workerapp.RuntimeControlReplayPlan, error) {
	return workerapp.RuntimeControlReplayPlan{}, nil
}

type workerPolicyInitialStream struct {
	workerPolicyTestStream
	incoming chan *contractsv1.RuntimeControlFrame
}

func (s *workerPolicyInitialStream) RecvFrame() (*contractsv1.RuntimeControlFrame, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case f, ok := <-s.incoming:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	}
}
func (s *workerPolicyInitialStream) SendFrame(frame *contractsv1.RuntimeControlFrame) error {
	s.sent = append(s.sent, frame)
	if ack := frame.ResumeAck; ack != nil {
		s.incoming <- &contractsv1.RuntimeControlFrame{FrameType: contractsv1.RuntimeControlFrameTypeResumeReady, ResumeReady: &contractsv1.RuntimeControlResumeReady{ServerConnectionID: ack.ServerConnectionID, AcknowledgedJournalRevision: ack.JournalRevision, WorkerTXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest, WorkerRXCursorDigest: contractsv1.RuntimeControlCursorGenesisDigest}}
	}
	if frame.ResumeComplete != nil {
		s.incoming <- &contractsv1.RuntimeControlFrame{FrameType: contractsv1.RuntimeControlFrameTypeEnvelope, Envelope: &contractsv1.RuntimeControlEnvelope{SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: "runtime-ready", Kind: "event", WorkerSessionID: "session-1", WorkerRunID: "run-1", RuntimeInstanceID: "runtime-1", RuntimeGeneration: 1, AgentSessionID: "agent-1", AgentTurnID: "turn-1", ChannelEpoch: 1, Sequence: 1, IdempotencyKey: "runtime-ready", MessageType: "runtime.ready", Payload: json.RawMessage(`{"ready":true}`), SentAt: time.Now().UTC()}}
		close(s.incoming)
	}
	return nil
}

func TestWorkerPolicyInitialHandoffStillRequiresPhysicalPolicy(t *testing.T) {
	exact := workerPolicyTestTarget()
	owner := workerPolicyOwner{identity: exact, status: "starting", residents: []workerPolicyResident{{Session: exact.WorkerSessionID, Runtime: exact.RuntimeInstanceID, Generation: exact.Generation, Origin: "reserved"}}}
	owner.identity.ContainerID = ""
	for _, failure := range []string{"handoff lookup error", "missing physical policy", "unconfined", "tampered"} {
		t.Run(failure, func(t *testing.T) {
			denied := errors.New(failure)
			inspected := 0
			admission := &workerPolicyAdmission{lookup: func(context.Context, string) (workerPolicyOwner, error) { return owner, nil }, handoff: func(workerapp.CreatedRuntimeIdentity) (workerapp.CreatedRuntimeIdentity, bool, error) {
				if failure == "handoff lookup error" {
					return workerapp.CreatedRuntimeIdentity{}, false, denied
				}
				return exact, true, nil
			}, inspect: func(context.Context, workerapp.CreatedRuntimeIdentity) error { inspected++; return denied }}
			journal := &workerPolicyTestJournal{}
			_, err := admission.guardJournal(journal).ClaimRuntimeControlStream(context.Background(), workerapp.ClaimRuntimeControlStreamRecord{Command: workerapp.ClaimRuntimeControlStreamCommand{WorkerSessionID: exact.WorkerSessionID, RuntimeInstanceID: exact.RuntimeInstanceID, RuntimeGeneration: exact.Generation}})
			if err != denied || journal.calls != 0 {
				t.Fatalf("handoff bypassed policy: %v claims=%d", err, journal.calls)
			}
			want := 1
			if failure == "handoff lookup error" {
				want = 0
			}
			if inspected != want {
				t.Fatalf("physical inspections=%d", inspected)
			}
		})
	}
	request := exact
	request.ContainerID = "wrong-request-CID"
	if _, err := owner.target(request, &exact); err == nil {
		t.Fatal("handoff overrode conflicting requested CID")
	}
}
