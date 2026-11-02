package application_test

import (
	"context"
	"encoding/json"
	"errors"
	app "github.com/abietic/switchback/internal/workerruntime/application"
	"reflect"
	"strings"
	"testing"
	"time"
)

func emptyPreparationBinding() app.RuntimeSourcePlanBinding {
	b, err := app.NewRuntimeSourcePlanBinding("sha256:"+strings.Repeat("a", 64), nil)
	if err != nil {
		panic(err)
	}
	return b
}

type sourcePlanEnvironment struct {
	plan              *sourcePreparationPlan
	journal           *[]string
	nilPlan           bool
	typedNil          bool
	err               error
	returnPlanOnError bool
}

func (e *sourcePlanEnvironment) PlanRuntime(context.Context, app.RuntimePreparationRequest) (app.RuntimePreparationPlan, error) {
	*e.journal = append(*e.journal, "plan")
	if e.err != nil {
		if e.returnPlanOnError {
			return e.plan, e.err
		}
		return nil, e.err
	}
	if e.nilPlan {
		return nil, nil
	}
	if e.typedNil {
		var p *sourcePreparationPlan
		return p, nil
	}
	return e.plan, nil
}

type sourcePreparationPlan struct {
	binding          app.RuntimeSourcePlanBinding
	request          app.RuntimeProvisionRequest
	journal          *[]string
	prepares, closes int
	bindingCalls     int
	err              error
	closeErr         error
}

func (p *sourcePreparationPlan) SourceBinding() app.RuntimeSourcePlanBinding {
	p.bindingCalls++
	return p.binding
}
func (p *sourcePreparationPlan) Prepare(ctx context.Context) (app.RuntimeProvisionRequest, error) {
	p.prepares++
	*p.journal = append(*p.journal, "prepare")
	return p.request, p.err
}
func (p *sourcePreparationPlan) Close() error {
	p.closes++
	*p.journal = append(*p.journal, "close")
	return p.closeErr
}

type sourceAuthorizer struct {
	journal         *[]string
	binding         app.RuntimeSourcePlanBinding
	err             error
	cancel          context.CancelFunc
	calls           int
	allowUnbound    bool
	cancelProvision bool
	provisionErr    error
}

func (a *sourceAuthorizer) AuthorizeInitialPreparation(ctx context.Context, r app.RuntimePreparationRequest) error {
	a.calls++
	*a.journal = append(*a.journal, "authorize-prepare")
	if !a.allowUnbound && r.SourcePlan != a.binding {
		return errors.New("readonly plan was not captured before authorization")
	}
	if a.cancel != nil && !a.cancelProvision {
		a.cancel()
	}
	return a.err
}
func (a *sourceAuthorizer) AuthorizeInitialProvision(ctx context.Context, r app.RuntimePreparationRequest, p app.RuntimeProvisionRequest) error {
	a.calls++
	*a.journal = append(*a.journal, "authorize-provision")
	if !a.allowUnbound && (r.SourcePlan != a.binding || p.SourcePlanDigest != a.binding.Digest) {
		return errors.New("provision plan drift")
	}
	if a.cancelProvision && a.cancel != nil {
		a.cancel()
	}
	if a.provisionErr != nil {
		return a.provisionErr
	}
	return a.err
}

type sourceProvisioner struct {
	journal *[]string
	calls   int
	err     error
}

func (p *sourceProvisioner) Provision(ctx context.Context, r app.RuntimeProvisionRequest, check app.RuntimePreStartCheck) (app.ProvisionedRuntime, error) {
	p.calls++
	*p.journal = append(*p.journal, "create")
	if p.err != nil {
		return app.ProvisionedRuntime{}, p.err
	}
	out := app.ProvisionedRuntime{RuntimeInstanceID: r.RuntimeInstanceID, Generation: r.Generation, ContainerID: "CID-A"}
	if err := check(ctx, app.CreatedRuntimeIdentity{ContainerID: "CID-A", RuntimeInstanceID: r.RuntimeInstanceID, WorkerSessionID: r.WorkerSessionID, Generation: r.Generation}); err != nil {
		return out, err
	}
	*p.journal = append(*p.journal, "start")
	return out, nil
}
func (*sourceProvisioner) Inspect(context.Context, string) (app.RuntimeInspection, error) {
	return app.RuntimeInspection{}, nil
}
func sourceCoordinatorFixture(t *testing.T) (app.RuntimeCoordinator, app.StartRuntimeCommand, *sourcePlanEnvironment, *sourceAuthorizer, *sourceProvisioner, *[]string) {
	t.Helper()
	journal := []string{}
	b := emptyPreparationBinding()
	cmd := validStartWorkerRunCommand()
	legacy, _ := b.LegacyFileDigest()
	cmd.StartSnapshot = json.RawMessage(`{"working_copy_checkpoint_digest":"sha256:` + legacy + `"}`)
	p := &sourcePreparationPlan{binding: b, journal: &journal, request: app.RuntimeProvisionRequest{WorkerSessionID: cmd.WorkerSessionID, WorkerRunID: "run", RuntimeInstanceID: "runtime", Generation: 1, NetworkProfile: app.ProfileRef{ID: cmd.NetworkPolicyID, Revision: cmd.NetworkPolicyRevision, Digest: cmd.NetworkPolicyDigest}, NetworkEnforcementStatus: cmd.NetworkEnforcementStatus, SourcePlanDigest: b.Digest}}
	e := &sourcePlanEnvironment{plan: p, journal: &journal}
	a := &sourceAuthorizer{journal: &journal, binding: b}
	d := &sourceProvisioner{journal: &journal}
	c := app.RuntimeCoordinator{Starts: app.StartWorkerRunService{Store: &recordingStartStore{}, IDs: &sequenceIDs{values: []string{"run", "runtime", "agent", "turn", "deadline", "transition", "event"}}, Now: time.Now}, Environment: e, Authorizer: a, Provisioner: d, CreatedLayerChecker: &boundaryCreatedChecker{journal: &journal}}
	return c, app.StartRuntimeCommand{Run: cmd}, e, a, d, &journal
}
func TestRuntimePreparationPlanCoordinatorOrdersReadonlyPlanBeforeOriginalAuthority(t *testing.T) {
	c, cmd, e, _, d, j := sourceCoordinatorFixture(t)
	out, err := c.Start(context.Background(), cmd)
	want := []string{"plan", "authorize-prepare", "prepare", "authorize-provision", "create", "quota", "authorize-provision", "start", "close"}
	if err != nil || !reflect.DeepEqual(*j, want) || d.calls != 1 || e.plan.prepares != 1 || e.plan.closes != 1 || e.plan.bindingCalls != 1 || out.Ownership.WorkerRunID != "run" {
		t.Fatalf("order=%v want=%v out=%+v err=%v plan=%+v", *j, want, out, err, e.plan)
	}
}
func TestRuntimePreparationPlanCoordinatorRejectsSnapshotBeforeEffects(t *testing.T) {
	c, cmd, e, a, d, _ := sourceCoordinatorFixture(t)
	a.allowUnbound = true
	cmd.Run.StartSnapshot = json.RawMessage(`{"working_copy_checkpoint_digest":"sha256:` + strings.Repeat("b", 64) + `"}`)
	out, err := c.Start(context.Background(), cmd)
	if err == nil || a.calls != 0 || e.plan.prepares != 0 || d.calls != 0 || e.plan.closes != 1 || out.Ownership.WorkerRunID != "run" {
		t.Fatalf("err=%v auth=%d prepare=%d create=%d close=%d owner=%+v", err, a.calls, e.plan.prepares, d.calls, e.plan.closes, out.Ownership)
	}
}

func TestRuntimePreparationPlanCoordinatorDenials(t *testing.T) {
	for _, name := range []string{"nil", "typed_nil", "plan_error", "invalid", "missing_snapshot", "malformed_snapshot", "denied", "cancelled_after_auth", "prepare_error", "digest_drift", "missing_prepared_digest", "replay", "missing_checker", "typed_nil_checker", "plan_error_with_handle", "provision_denied", "cancelled_after_provision_auth"} {
		t.Run(name, func(t *testing.T) {
			c, cmd, e, a, d, j := sourceCoordinatorFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantAuth, wantPrepare, wantClose := 0, 0, 1
			switch name {
			case "nil":
				e.nilPlan = true
				wantClose = 0
			case "typed_nil":
				e.typedNil = true
				wantClose = 0
			case "plan_error":
				e.err = errors.New("plan denied")
				wantClose = 0
			case "plan_error_with_handle":
				e.err = errors.New("plan failed with handle")
				e.returnPlanOnError = true
			case "provision_denied":
				a.provisionErr = errors.New("provision authority denied")
				wantAuth = 2
				wantPrepare = 1
			case "cancelled_after_provision_auth":
				a.cancel = cancel
				a.cancelProvision = true
				wantAuth = 2
				wantPrepare = 1
			case "invalid":
				e.plan.binding.Manifest += " "
			case "missing_snapshot":
				cmd.Run.StartSnapshot = json.RawMessage(`{}`)
			case "malformed_snapshot":
				cmd.Run.StartSnapshot = json.RawMessage(`{"working_copy_checkpoint_digest":1}`)
			case "denied":
				a.err = errors.New("authority denied")
				wantAuth = 1
			case "cancelled_after_auth":
				a.cancel = cancel
				wantAuth = 1
			case "prepare_error":
				e.plan.err = errors.New("prepare failed")
				wantAuth = 1
				wantPrepare = 1
			case "digest_drift":
				e.plan.request.SourcePlanDigest = "sha256:" + strings.Repeat("b", 64)
				wantAuth = 1
				wantPrepare = 1
			case "missing_prepared_digest":
				e.plan.request.SourcePlanDigest = ""
				wantAuth = 1
				wantPrepare = 1
			case "replay":
				c.Starts.Store = &recordingStartStore{replay: true}
				wantClose = 0
			case "missing_checker":
				c.CreatedLayerChecker = nil
				wantClose = 0
			case "typed_nil_checker":
				var checker *boundaryCreatedChecker
				c.CreatedLayerChecker = checker
				wantClose = 0
			}
			out, err := c.Start(ctx, cmd)
			if err == nil || a.calls != wantAuth || e.plan.prepares != wantPrepare || e.plan.closes != wantClose || d.calls != 0 || out.Ownership.WorkerRunID != "run" {
				t.Fatalf("err=%v auth=%d prepare=%d close=%d create=%d order=%v owner=%+v", err, a.calls, e.plan.prepares, e.plan.closes, d.calls, *j, out.Ownership)
			}
			if name == "replay" && (!errors.Is(err, app.InitialRuntimeStartReplayPending) || len(*j) != 0) {
				t.Fatalf("replay read source: %v %v", *j, err)
			}
			if (name == "missing_checker" || name == "typed_nil_checker") && len(*j) != 0 {
				t.Fatalf("missing physical checker read source: %v", *j)
			}
			if (name == "cancelled_after_auth" || name == "cancelled_after_provision_auth") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestRuntimePreparationPlanCoordinatorCloseErrorPreservesCommittedResult(t *testing.T) {
	c, cmd, e, _, d, _ := sourceCoordinatorFixture(t)
	sentinel := errors.New("source handle close failed")
	e.plan.closeErr = sentinel
	out, err := c.Start(context.Background(), cmd)
	if !errors.Is(err, sentinel) || out.Ownership.WorkerRunID != "run" || out.Runtime.ContainerID != "CID-A" || d.calls != 1 || e.plan.closes != 1 {
		t.Fatalf("result=%+v err=%v closes=%d creates=%d", out, err, e.plan.closes, d.calls)
	}
}

func TestRuntimePreparationPlanCoordinatorPreservesPendingCauseWithoutCloseFailure(t *testing.T) {
	for _, closeFails := range []bool{false, true} {
		name := "close_succeeds"
		if closeFails {
			name = "close_fails"
		}
		t.Run(name, func(t *testing.T) {
			c, cmd, e, _, d, _ := sourceCoordinatorFixture(t)
			d.err = app.InitialRuntimeStartReplayPending
			closeErr := errors.New("source handle close failed")
			if closeFails {
				e.plan.closeErr = closeErr
			}
			out, err := c.Start(context.Background(), cmd)
			var joined interface{ Unwrap() []error }
			if !errors.Is(err, app.InitialRuntimeStartReplayPending) || errors.As(err, &joined) != closeFails ||
				errors.Is(err, closeErr) != closeFails || e.plan.closes != 1 || d.calls != 1 || out.Ownership.RuntimeInstanceID != "runtime" {
				t.Fatalf("owner=%+v err=%v multi_cause=%t close_failed=%t closes=%d creates=%d", out.Ownership, err, joined != nil, closeFails, e.plan.closes, d.calls)
			}
		})
	}
}
