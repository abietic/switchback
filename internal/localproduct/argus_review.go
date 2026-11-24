package localproduct

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	taskpostgres "github.com/abietic/switchback/internal/taskorchestration/adapter/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

const (
	argusReviewProfile   = "lets-quant-report.v1"
	argusReviewRequestV1 = "argus.report_review_request.v1alpha1"
	argusReviewResultV1  = "argus.report_review_result.v1alpha1"
	argusReviewTimeout   = 20 * time.Second
	argusReviewLease     = 25 * time.Second
	argusReviewMaxInput  = 16 << 20
	argusReviewMaxOutput = 64 << 10
	argusReviewMaxBinary = 128 << 20
)

var (
	argusReviewEvidenceNames = [...]string{"facts.json", "configured-costs.csv", "high-friction.csv", "delayed-execution.csv"}
	errArgusReviewIntegrity  = errors.New("Argus review input or result failed validation")
)

type argusReviewConfig struct {
	Executable       string            `json:"executable"`
	ExecutableSHA256 string            `json:"executable_sha256"`
	EvidenceDir      string            `json:"evidence_dir"`
	EvidenceSHA256   map[string]string `json:"evidence_sha256"`
}

type argusReviewAdapter interface {
	Review(context.Context, taskapp.ArgusReviewJob, []byte) (contractsv1.DeliveryReview, error)
}

type argusReviewRunner struct {
	scope         taskapp.DeliveryReviewScope
	store         *taskpostgres.DeliveryStore
	objects       *artifactlocal.ObjectStore
	profileSHA256 string
	adapter       argusReviewAdapter
}

func newArgusReviewRunner(configuration Config, pool *pgxpool.Pool, objects *artifactlocal.ObjectStore) (*argusReviewRunner, error) {
	adapter, err := loadArgusReviewAdapter(configuration.ArgusReviewConfig, configuration.DataRoot)
	if err != nil {
		return nil, err
	}
	return &argusReviewRunner{
		scope: taskapp.DeliveryReviewScope{
			Actor:       taskapp.Actor{TenantID: configuration.Profile.TenantID, UserID: configuration.Profile.ActorUserID},
			WorkspaceID: configuration.WorkspaceID,
		},
		store: taskpostgres.NewDeliveryStore(pool), objects: objects,
		profileSHA256: adapter.profileSHA256, adapter: adapter,
	}, nil
}

func (runner *argusReviewRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := runner.runOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("local Argus review job could not advance", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (runner *argusReviewRunner) runOnce(ctx context.Context) error {
	if err := runner.store.QueueArgusReviews(ctx, runner.scope, runner.profileSHA256); err != nil {
		return fmt.Errorf("reconcile committed deliveries: %w", err)
	}
	job, found, err := runner.store.ClaimArgusReview(ctx, runner.scope, runner.profileSHA256, argusReviewLease)
	if err != nil || !found {
		return err
	}
	attempt, cancel := context.WithTimeout(ctx, argusReviewTimeout)
	defer cancel()
	err = runner.review(attempt, job)
	if err == nil || ctx.Err() != nil || errors.Is(err, taskpostgres.ErrArgusReviewLeaseLost) {
		return nil
	}
	code, terminal := "review_attempt_failed", false
	switch {
	case errors.Is(err, taskapp.ErrDeliveryReviewStale), errors.Is(err, taskapp.ErrDeliveryReviewUnavailable):
		code, terminal = "stale_target", true
	case errors.Is(err, taskapp.ErrIdempotencyConflict):
		code, terminal = "review_conflict", true
	case errors.Is(err, errArgusReviewIntegrity):
		code, terminal = "review_integrity_failed", true
	}
	if err = runner.store.ReleaseArgusReview(ctx, job, code, terminal); errors.Is(err, taskpostgres.ErrArgusReviewLeaseLost) {
		return nil
	}
	return err
}

func (runner *argusReviewRunner) review(ctx context.Context, job taskapp.ArgusReviewJob) error {
	key, err := runner.store.ArgusReviewObject(ctx, job)
	if err != nil {
		return err
	}
	f, err := runner.objects.Open(ctx, key)
	if err != nil {
		return err
	}
	defer f.Close()
	report, err := readReviewBytes(f, argusReviewMaxInput)
	if err != nil {
		return err
	}
	if reviewSHA256(report) != job.ArtifactSHA256 {
		return errArgusReviewIntegrity
	}
	receipt, err := runner.adapter.Review(ctx, job, report)
	if err != nil {
		return err
	}
	return runner.store.FinishArgusReview(ctx, job, receipt)
}

type argusSubprocess struct {
	executable    string
	profileDir    string
	profileSHA256 string
	evidence      map[string][]byte
	evidenceSHA   map[string]string
}

func loadArgusReviewAdapter(configPath, dataRoot string) (*argusSubprocess, error) {
	raw, err := readReviewFile(configPath, argusReviewMaxOutput)
	if err != nil {
		return nil, fmt.Errorf("read Argus review configuration: %w", err)
	}
	var cfg argusReviewConfig
	if err := decodeArgusJSON(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid Argus review configuration")
	}
	if !filepath.IsAbs(cfg.Executable) || !filepath.IsAbs(cfg.EvidenceDir) ||
		len(cfg.EvidenceSHA256) != len(argusReviewEvidenceNames) {
		return nil, fmt.Errorf("Argus executable and evidence directory must be absolute with four pinned evidence digests")
	}
	adapter := &argusSubprocess{evidence: make(map[string][]byte), evidenceSHA: cfg.EvidenceSHA256}
	for _, name := range argusReviewEvidenceNames {
		raw, err := readReviewFile(filepath.Join(cfg.EvidenceDir, name), argusReviewMaxInput)
		if err != nil || reviewSHA256(raw) != cfg.EvidenceSHA256[name] {
			return nil, fmt.Errorf("Argus review evidence %s differs from its approved digest", name)
		}
		adapter.evidence[name] = raw
	}
	binary, err := readReviewFile(cfg.Executable, argusReviewMaxBinary)
	if err != nil || reviewSHA256(binary) != cfg.ExecutableSHA256 {
		return nil, fmt.Errorf("Argus executable differs from its approved digest")
	}
	identity, err := json.Marshal(struct {
		Profile    string
		Executable string
		Evidence   map[string]string
	}{argusReviewProfile, cfg.ExecutableSHA256, cfg.EvidenceSHA256})
	if err != nil {
		return nil, err
	}
	adapter.profileSHA256 = reviewSHA256(identity)
	adapter.profileDir = filepath.Join(dataRoot, "argus-report-review", adapter.profileSHA256)
	if err := os.MkdirAll(adapter.profileDir, 0o700); err != nil {
		return nil, err
	}
	adapter.executable = filepath.Join(adapter.profileDir, "argus")
	if err := installArgusExecutable(adapter.executable, binary); err != nil {
		return nil, err
	}
	return adapter, nil
}

func installArgusExecutable(path string, binary []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".argus-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(binary)
	modeErr := f.Chmod(0o500)
	closeErr := f.Close()
	if err := errors.Join(writeErr, modeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Field order matches DigestReportReviewRequest in Argus. The JSON bytes
// written to request.json are also the bytes whose SHA-256 the child echoes.
type argusReviewRequest struct {
	SchemaVersion  string            `json:"schema_version"`
	IdempotencyKey string            `json:"idempotency_key"`
	TaskID         string            `json:"task_id"`
	TaskRevision   uint64            `json:"task_revision"`
	WorkerRunID    string            `json:"worker_run_id"`
	ArtifactID     string            `json:"artifact_id"`
	ArtifactSHA256 string            `json:"artifact_sha256"`
	Profile        string            `json:"profile"`
	EvidenceSHA256 map[string]string `json:"evidence_sha256"`
}

type argusReviewResult struct {
	SchemaVersion  string         `json:"schema_version"`
	RequestSHA256  string         `json:"request_sha256"`
	ReviewRunID    string         `json:"review_run_id"`
	TaskID         string         `json:"task_id"`
	TaskRevision   uint64         `json:"task_revision"`
	WorkerRunID    string         `json:"worker_run_id"`
	ArtifactID     string         `json:"artifact_id"`
	ArtifactSHA256 string         `json:"artifact_sha256"`
	Profile        string         `json:"profile"`
	Verdict        string         `json:"verdict"`
	Findings       []argusFinding `json:"findings"`
}

type argusFinding struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
	Line    uint64 `json:"line,omitempty"`
}

func (adapter *argusSubprocess) Review(ctx context.Context, job taskapp.ArgusReviewJob, report []byte) (contractsv1.DeliveryReview, error) {
	var receipt contractsv1.DeliveryReview
	request := argusReviewRequest{
		SchemaVersion: argusReviewRequestV1, TaskID: job.TaskID, TaskRevision: job.TaskRevision,
		WorkerRunID: string(job.WorkerRunID), ArtifactID: job.ArtifactID, ArtifactSHA256: job.ArtifactSHA256,
		Profile: argusReviewProfile, EvidenceSHA256: adapter.evidenceSHA,
	}
	identity, err := json.Marshal(request)
	if err != nil {
		return receipt, err
	}
	request.IdempotencyKey = reviewSHA256(append([]byte(adapter.profileSHA256), identity...))
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return receipt, err
	}
	jobDir := filepath.Join(adapter.profileDir, request.IdempotencyKey)
	storeDir := filepath.Join(jobDir, "store")
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return receipt, err
	}
	stage, err := os.MkdirTemp(jobDir, "attempt-")
	if err != nil {
		return receipt, err
	}
	defer os.RemoveAll(stage)
	inputDir := filepath.Join(stage, "input")
	if err := os.Mkdir(inputDir, 0o700); err != nil {
		return receipt, err
	}
	if err := os.WriteFile(filepath.Join(inputDir, "report.md"), report, 0o400); err != nil {
		return receipt, err
	}
	for name, contents := range adapter.evidence {
		if err := os.WriteFile(filepath.Join(inputDir, name), contents, 0o400); err != nil {
			return receipt, err
		}
	}
	requestPath := filepath.Join(stage, "request.json")
	if err := os.WriteFile(requestPath, requestBytes, 0o400); err != nil {
		return receipt, err
	}
	command := exec.CommandContext(ctx, adapter.executable,
		"report-review", "--store", storeDir, "--request", requestPath, "--input-dir", inputDir)
	command.Dir = stage
	command.Env = []string{"HOME=" + stage, "TMPDIR=" + stage, "LANG=C"}
	command.WaitDelay = time.Second
	prepareArgusProcess(command)
	stdout := boundedArgusStdout{}
	command.Stdout = &stdout
	if err := command.Run(); err != nil {
		if stdout.overflow {
			return receipt, errArgusReviewIntegrity
		}
		// Child diagnostics can contain private source content.
		return receipt, fmt.Errorf("Argus report-review did not return a result")
	}
	return validateArgusResult(stdout.buffer.Bytes(), request, reviewSHA256(requestBytes))
}

func validateArgusResult(raw []byte, request argusReviewRequest, requestSHA256 string) (contractsv1.DeliveryReview, error) {
	var got argusReviewResult
	if len(raw) == 0 || raw[len(raw)-1] != '\n' || bytes.Count(raw, []byte{'\n'}) != 1 ||
		decodeArgusJSON(raw, &got) != nil || got.SchemaVersion != argusReviewResultV1 ||
		got.RequestSHA256 != requestSHA256 || got.TaskID != request.TaskID ||
		got.TaskRevision != request.TaskRevision || got.WorkerRunID != request.WorkerRunID ||
		got.ArtifactID != request.ArtifactID || got.ArtifactSHA256 != request.ArtifactSHA256 || got.Profile != request.Profile ||
		got.Findings == nil {
		return contractsv1.DeliveryReview{}, errArgusReviewIntegrity
	}
	canonicalBytes, err := json.Marshal(got)
	if err != nil || !bytes.Equal(raw[:len(raw)-1], canonicalBytes) {
		return contractsv1.DeliveryReview{}, errArgusReviewIntegrity
	}
	receipt := contractsv1.DeliveryReview{
		SchemaVersion: contractsv1.DeliveryReviewSchemaVersion, ExpectedTaskRevision: got.TaskRevision,
		WorkerRunID: contractsv1.WorkerRunID(got.WorkerRunID), ArtifactID: got.ArtifactID,
		ArtifactSHA256: got.ArtifactSHA256, ReviewRunID: got.ReviewRunID, ReviewResultSHA256: reviewSHA256(raw),
		Verdict: got.Verdict, Findings: make([]contractsv1.DeliveryFinding, 0, len(got.Findings)),
	}
	for _, f := range got.Findings {
		receipt.Findings = append(receipt.Findings, contractsv1.DeliveryFinding{Code: f.Code, Summary: f.Summary})
	}
	if err := receipt.Validate(); err != nil {
		return contractsv1.DeliveryReview{}, errArgusReviewIntegrity
	}
	return receipt, nil
}

type boundedArgusStdout struct {
	buffer   bytes.Buffer
	overflow bool
}

func (stdout *boundedArgusStdout) Write(value []byte) (int, error) {
	if len(value) > argusReviewMaxOutput-stdout.buffer.Len() {
		stdout.overflow = true
		return 0, errArgusReviewIntegrity
	}
	return stdout.buffer.Write(value)
}

func decodeArgusJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errArgusReviewIntegrity
	}
	return nil
}

func readReviewFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errArgusReviewIntegrity
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readReviewBytes(file, limit)
}

func readReviewBytes(reader io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errArgusReviewIntegrity
	}
	return raw, nil
}

func reviewSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
