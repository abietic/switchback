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
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	artifactapp "github.com/abietic/switchback/internal/artifacttrace/application"
	artifactdomain "github.com/abietic/switchback/internal/artifacttrace/domain"
	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
	workerdomain "github.com/abietic/switchback/internal/workerruntime/domain"
)

const runtimeTurnSettlementCheckpointSchemaVersion = 1

type runtimeSettlementSourceCheckpoint struct {
	Kind       string    `json:"kind"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at,omitempty"`
	TimedOut   bool      `json:"timed_out,omitempty"`
	ForcedStop bool      `json:"forced_stop,omitempty"`
}

type runtimeSettlementEvidenceInput struct {
	TenantID                      string    `json:"tenant_id,omitempty"`
	UserID                        string    `json:"user_id,omitempty"`
	WorkspaceID                   string    `json:"workspace_id,omitempty"`
	TaskID                        string    `json:"task_id,omitempty"`
	WorkerSessionID               string    `json:"worker_session_id,omitempty"`
	WorkerRunID                   string    `json:"worker_run_id,omitempty"`
	AgentSessionID                string    `json:"agent_session_id,omitempty"`
	AgentTurnID                   string    `json:"agent_turn_id,omitempty"`
	RuntimeInstanceID             string    `json:"runtime_instance_id,omitempty"`
	RuntimeGeneration             uint64    `json:"runtime_generation,omitempty"`
	NetworkPolicyID               string    `json:"network_policy_id,omitempty"`
	NetworkPolicyRevision         uint64    `json:"network_policy_revision,omitempty"`
	NetworkPolicyDigest           string    `json:"network_policy_digest,omitempty"`
	NetworkEnforcementStatus      string    `json:"network_enforcement_status,omitempty"`
	TraceStartOffset              int64     `json:"trace_start_offset"`
	TraceEndOffset                int64     `json:"trace_end_offset"`
	TraceDigest                   string    `json:"trace_digest"`
	PatchSnapshotFile             string    `json:"patch_snapshot_file"`
	PatchSize                     int64     `json:"patch_size"`
	PatchDigest                   string    `json:"patch_digest"`
	ObservationSourceSnapshotFile string    `json:"observation_source_snapshot_file,omitempty"`
	ObservationSourceSize         int64     `json:"observation_source_size,omitempty"`
	ObservationSourceDigest       string    `json:"observation_source_digest,omitempty"`
	NoFilesChanged                bool      `json:"no_files_changed,omitempty"`
	ServerRecordedAt              time.Time `json:"server_recorded_at,omitempty"`
	WorkerRecordedAt              time.Time `json:"worker_recorded_at,omitempty"`
	ReportArtifactID              string    `json:"report_artifact_id,omitempty"`
	PatchArtifactID               string    `json:"patch_artifact_id,omitempty"`
	ArtifactCreatedAt             time.Time `json:"artifact_created_at,omitempty"`
	SourceCheckpointID            string    `json:"source_checkpoint_id,omitempty"`
	SourceCheckpointDigest        string    `json:"source_checkpoint_digest,omitempty"`
}

type runtimeSettlementDomainPlan struct {
	SessionTarget           string `json:"session_target"`
	RunTarget               string `json:"run_target"`
	TurnTarget              string `json:"turn_target"`
	TerminalReason          string `json:"terminal_reason,omitempty"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision,omitempty"`
	ExpectedRunRevision     uint64 `json:"expected_run_revision,omitempty"`
	ExpectedTurnRevision    uint64 `json:"expected_turn_revision,omitempty"`
	ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision,omitempty"`
}

type runtimeSettlementEvidenceCheckpoint struct {
	ManifestDigest string                            `json:"manifest_digest"`
	Finalized      artifactapp.FinalizedTurnEvidence `json:"finalized,omitempty"`
}

type runtimeSettlementDomainCheckpoint struct {
	SessionRevision uint64 `json:"session_revision"`
	RunRevision     uint64 `json:"run_revision"`
	TurnRevision    uint64 `json:"turn_revision"`
	RuntimeRevision uint64 `json:"runtime_revision"`
}

type runtimeSettlementPublicationCheckpoint struct {
	TraceStreamID string   `json:"trace_stream_id"`
	ArtifactIDs   []string `json:"artifact_ids"`
}

type runtimeSettlementTaskPlan struct {
	Action               string `json:"action"`
	TaskID               string `json:"task_id,omitempty"`
	ExpectedTaskRevision uint64 `json:"expected_task_revision,omitempty"`
	IdempotencyKey       string `json:"idempotency_key,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

type runtimeSettlementTaskResult struct {
	Status   string `json:"status"`
	Revision uint64 `json:"revision,omitempty"`
	EventID  string `json:"event_id,omitempty"`
}

type runtimeSettlementContinuationPlan struct {
	Action                  string `json:"action"`
	Reason                  string `json:"reason,omitempty"`
	QueueCommandID          string `json:"queue_command_id,omitempty"`
	ExpectedSessionRevision uint64 `json:"expected_session_revision,omitempty"`
	ExpectedRunRevision     uint64 `json:"expected_run_revision,omitempty"`
	ExpectedTurnRevision    uint64 `json:"expected_turn_revision,omitempty"`
	ExpectedRuntimeRevision uint64 `json:"expected_runtime_revision,omitempty"`
}

type runtimeSettlementContinuationResult struct {
	Status      string `json:"status"`
	WorkerRunID string `json:"worker_run_id,omitempty"`
	AgentTurnID string `json:"agent_turn_id,omitempty"`
}

// runtimeTurnSettlementCheckpoint is cumulative. Once a field is durable, a
// later phase may append proof but must not rewrite or remove the frozen
// prefix. The PostgreSQL settlement owner enforces that monotonic property.
type runtimeTurnSettlementCheckpoint struct {
	ObservationCapture *workerapp.RuntimeObservationCapture `json:"-"`
	SchemaVersion      uint32                               `json:"schema_version"`

	FrozenArtifacts *workerapp.FrozenArtifactMetadata  `json:"frozen_artifacts,omitempty"`
	Source          *runtimeSettlementSourceCheckpoint `json:"source,omitempty"`
	EvidenceInput   *runtimeSettlementEvidenceInput    `json:"evidence_input,omitempty"`
	DomainPlan      *runtimeSettlementDomainPlan       `json:"domain_plan,omitempty"`

	Evidence    *runtimeSettlementEvidenceCheckpoint    `json:"evidence,omitempty"`
	Domain      *runtimeSettlementDomainCheckpoint      `json:"domain,omitempty"`
	Publication *runtimeSettlementPublicationCheckpoint `json:"publication,omitempty"`

	TaskPlan   *runtimeSettlementTaskPlan   `json:"task_plan,omitempty"`
	TaskResult *runtimeSettlementTaskResult `json:"task_result,omitempty"`

	TerminalSourceApplied        bool            `json:"terminal_source_applied,omitempty"`
	BackendDeadlineSourceApplied json.RawMessage `json:"backend_deadline_source_applied,omitempty"`

	ContinuationPlan   *runtimeSettlementContinuationPlan   `json:"continuation_plan,omitempty"`
	ContinuationResult *runtimeSettlementContinuationResult `json:"continuation_result,omitempty"`
}

type runtimeSettlementController interface {
	checkpoint(
		context.Context,
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
	) (workerapp.TurnSettlement, error)
	advance(
		context.Context,
		workerapp.TurnSettlement,
		workerapp.TurnSettlementPhase,
		runtimeTurnSettlementCheckpoint,
	) (workerapp.TurnSettlement, error)
	commitDomain(
		context.Context,
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
	) (
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
		error,
	)
	applySource(
		context.Context,
		workerapp.TurnSettlement,
	) (workerapp.TurnSettlement, error)
}

type runtimeSettlementEffects struct {
	prepareEvidence func(
		context.Context,
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
	finalizeEvidence func(
		context.Context,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
	publishEvidence func(
		context.Context,
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
	planTask func(
		context.Context,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
	settleTask func(
		context.Context,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
	planContinuation func(
		context.Context,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
	settleContinuation func(
		context.Context,
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
	) (runtimeTurnSettlementCheckpoint, error)
}

type runtimeTurnSettlementCoordinator struct {
	controller runtimeSettlementController
	effects    runtimeSettlementEffects
}

type runtimeSettlementPersistence struct {
	service workerapp.TurnSettlementService
	commit  func(
		context.Context,
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
	) (
		workerapp.TurnSettlement,
		runtimeTurnSettlementCheckpoint,
		error,
	)
	apply func(
		context.Context,
		workerapp.TurnSettlement,
	) (workerapp.TurnSettlement, error)
}

func (runner *runtimeRunner) reconcileRuntimeEventTurnSettlement(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	terminal turnTerminalResult,
) (runtimeTurnSettlementCheckpoint, bool, error) {
	settlementID, err := workerapp.TurnSettlementID(turn.AgentTurnID)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, false, err
	}
	current, err := runner.settlements.Load(ctx, settlementID)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, false, err
	}
	current, err = runner.settlements.Claim(
		ctx,
		workerapp.ClaimTurnSettlementCommand{
			SettlementID:     settlementID,
			ExpectedRevision: current.Revision,
			OwnerID:          runner.settlerID,
		},
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, false, err
	}
	return runner.reconcileClaimedRuntimeEventTurnSettlement(
		ctx,
		current,
		live,
		turn,
		terminal,
	)
}

func (runner *runtimeRunner) reconcileClaimedRuntimeEventTurnSettlement(
	ctx context.Context,
	current workerapp.TurnSettlement,
	live *liveRuntime,
	turn pendingTurn,
	terminal turnTerminalResult,
) (runtimeTurnSettlementCheckpoint, bool, error) {
	if current.AgentTurnID != turn.AgentTurnID {
		return runtimeTurnSettlementCheckpoint{}, false, fmt.Errorf(
			"claimed Turn settlement differs from its AgentTurn",
		)
	}

	sourceApplyAttempted := false
	persistence := runtimeSettlementPersistence{
		service: runner.settlements,
		commit:  runner.commitRuntimeSettlementDomain,
		apply: func(
			ctx context.Context,
			current workerapp.TurnSettlement,
		) (workerapp.TurnSettlement, error) {
			// The concrete durable delivery owns retry as soon as Apply is
			// attempted. Callers must not release it a second time when Apply
			// or the following settlement reload fails.
			sourceApplyAttempted = true
			applied, applyErr := runner.applyRuntimeSettlementSource(
				ctx,
				terminal.sourceEvent,
				current,
			)
			return applied, applyErr
		},
	}
	coordinator := runtimeTurnSettlementCoordinator{
		controller: persistence,
		effects: runtimeSettlementEffects{
			prepareEvidence: func(
				ctx context.Context,
				settlement workerapp.TurnSettlement,
				_ runtimeTurnSettlementCheckpoint,
			) (runtimeTurnSettlementCheckpoint, error) {
				return runner.prepareRuntimeSettlementEvidence(
					ctx,
					live,
					turn,
					settlement,
					terminal,
				)
			},
			finalizeEvidence: func(
				ctx context.Context,
				checkpoint runtimeTurnSettlementCheckpoint,
			) (runtimeTurnSettlementCheckpoint, error) {
				return runner.finalizeRuntimeSettlementEvidence(
					ctx,
					live,
					checkpoint,
				)
			},
			publishEvidence:  runner.publishRuntimeSettlementEvidence,
			planTask:         runner.planRuntimeSettlementTask,
			settleTask:       runner.settleRuntimeSettlementTask,
			planContinuation: runner.planRuntimeSettlementContinuation,
			settleContinuation: runner.
				settleRuntimeSettlementContinuation,
		},
	}
	settled, err := coordinator.reconcileClaimed(ctx, current)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{},
			sourceApplyAttempted,
			err
	}
	checkpoint, err := decodeRuntimeTurnSettlementCheckpoint(
		settled.Phase,
		settled.PhaseState,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{},
			sourceApplyAttempted,
			err
	}
	return checkpoint, sourceApplyAttempted, nil
}

func (persistence runtimeSettlementPersistence) checkpoint(
	ctx context.Context,
	current workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (workerapp.TurnSettlement, error) {
	typed, err := applicationTurnSettlementCheckpoint(checkpoint)
	if err != nil {
		return workerapp.TurnSettlement{}, err
	}
	return persistence.service.FreezeCheckpoint(
		ctx,
		workerapp.FreezeTurnSettlementCheckpointCommand{
			SettlementID:       current.SettlementID,
			ExpectedRevision:   current.Revision,
			LeaseOwnerID:       current.LeaseOwnerID,
			LeaseFence:         current.LeaseFence,
			Checkpoint:         typed,
			ObservationCapture: checkpoint.ObservationCapture,
		},
	)
}

func (persistence runtimeSettlementPersistence) advance(
	ctx context.Context,
	current workerapp.TurnSettlement,
	to workerapp.TurnSettlementPhase,
	checkpoint runtimeTurnSettlementCheckpoint,
) (workerapp.TurnSettlement, error) {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"encode Runtime Turn settlement checkpoint: %w",
			err,
		)
	}
	return persistence.service.Advance(
		ctx,
		workerapp.AdvanceTurnSettlementCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			FromPhase:        current.Phase,
			ToPhase:          to,
			Checkpoint:       encoded,
		},
	)
}

func (persistence runtimeSettlementPersistence) commitDomain(
	ctx context.Context,
	current workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (
	workerapp.TurnSettlement,
	runtimeTurnSettlementCheckpoint,
	error,
) {
	if persistence.commit == nil {
		return workerapp.TurnSettlement{},
			runtimeTurnSettlementCheckpoint{},
			fmt.Errorf("atomic Turn settlement domain commit is required")
	}
	return persistence.commit(ctx, current, checkpoint)
}

func (persistence runtimeSettlementPersistence) applySource(
	ctx context.Context,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	if persistence.apply == nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"atomic Turn settlement source Apply is required",
		)
	}
	return persistence.apply(ctx, current)
}

func (coordinator runtimeTurnSettlementCoordinator) reconcileClaimed(
	ctx context.Context,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	if coordinator.controller == nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"Runtime Turn settlement controller is required",
		)
	}
	for current.Phase != workerapp.TurnSettlementCompleted {
		checkpoint, err := decodeRuntimeTurnSettlementCheckpoint(
			current.Phase,
			current.PhaseState,
		)
		if err != nil {
			return workerapp.TurnSettlement{}, err
		}
		switch current.Phase {
		case workerapp.TurnSettlementAccepted:
			if checkpoint.EvidenceInput == nil {
				if coordinator.effects.prepareEvidence == nil {
					return workerapp.TurnSettlement{}, fmt.Errorf(
						"prepare Turn evidence settlement step is required",
					)
				}
				current, err = coordinator.runWithLease(
					ctx,
					current,
					func(
						effectCtx context.Context,
						leased workerapp.TurnSettlement,
					) error {
						checkpoint, err =
							coordinator.effects.prepareEvidence(
								effectCtx,
								leased,
								checkpoint,
							)
						return err
					},
				)
				if err != nil {
					return workerapp.TurnSettlement{}, err
				}
				if err := validateRuntimeTurnSettlementCheckpoint(
					workerapp.TurnSettlementAccepted,
					checkpoint,
				); err != nil {
					return workerapp.TurnSettlement{}, err
				}
				current, err = coordinator.controller.checkpoint(
					ctx,
					current,
					checkpoint,
				)
				if err != nil {
					return workerapp.TurnSettlement{}, err
				}
			}
			if coordinator.effects.finalizeEvidence == nil {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"finalize Turn evidence settlement step is required",
				)
			}
			current, err = coordinator.runWithLease(
				ctx,
				current,
				func(
					effectCtx context.Context,
					_ workerapp.TurnSettlement,
				) error {
					checkpoint, err =
						coordinator.effects.finalizeEvidence(
							effectCtx,
							checkpoint,
						)
					return err
				},
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}
			if err := validateRuntimeTurnSettlementCheckpoint(
				workerapp.TurnSettlementEvidencePrepared,
				checkpoint,
			); err != nil {
				return workerapp.TurnSettlement{}, err
			}
			current, err = coordinator.controller.advance(
				ctx,
				current,
				workerapp.TurnSettlementEvidencePrepared,
				checkpoint,
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}

		case workerapp.TurnSettlementEvidencePrepared:
			current, checkpoint, err = coordinator.controller.commitDomain(
				ctx,
				current,
				checkpoint,
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}
			if current.Phase != workerapp.TurnSettlementDomainCommitted {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"atomic Turn settlement domain commit did not advance phase",
				)
			}
			if err := validateRuntimeTurnSettlementCheckpoint(
				workerapp.TurnSettlementDomainCommitted,
				checkpoint,
			); err != nil {
				return workerapp.TurnSettlement{}, err
			}

		case workerapp.TurnSettlementDomainCommitted:
			if coordinator.effects.publishEvidence == nil {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"publish Turn evidence settlement step is required",
				)
			}
			current, err = coordinator.runWithLease(
				ctx,
				current,
				func(
					effectCtx context.Context,
					leased workerapp.TurnSettlement,
				) error {
					checkpoint, err =
						coordinator.effects.publishEvidence(
							effectCtx,
							leased,
							checkpoint,
						)
					return err
				},
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}
			if err := validateRuntimeTurnSettlementCheckpoint(
				workerapp.TurnSettlementEvidencePublished,
				checkpoint,
			); err != nil {
				return workerapp.TurnSettlement{}, err
			}
			current, err = coordinator.controller.advance(
				ctx,
				current,
				workerapp.TurnSettlementEvidencePublished,
				checkpoint,
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}

		case workerapp.TurnSettlementEvidencePublished:
			if checkpoint.TaskPlan == nil {
				if coordinator.effects.planTask == nil {
					return workerapp.TurnSettlement{}, fmt.Errorf(
						"plan Task settlement step is required",
					)
				}
				current, err = coordinator.runWithLease(
					ctx,
					current,
					func(
						effectCtx context.Context,
						_ workerapp.TurnSettlement,
					) error {
						checkpoint, err =
							coordinator.effects.planTask(
								effectCtx,
								checkpoint,
							)
						return err
					},
				)
				if err != nil {
					return workerapp.TurnSettlement{}, err
				}
				if err := validateRuntimeSettlementTaskPlan(
					checkpoint,
				); err != nil {
					return workerapp.TurnSettlement{}, err
				}
				current, err = coordinator.controller.checkpoint(
					ctx,
					current,
					checkpoint,
				)
				if err != nil {
					return workerapp.TurnSettlement{}, err
				}
			}
			if coordinator.effects.settleTask == nil {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"settle Task step is required",
				)
			}
			current, err = coordinator.runWithLease(
				ctx,
				current,
				func(
					effectCtx context.Context,
					_ workerapp.TurnSettlement,
				) error {
					checkpoint, err =
						coordinator.effects.settleTask(
							effectCtx,
							checkpoint,
						)
					return err
				},
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}
			if err := validateRuntimeTurnSettlementCheckpoint(
				workerapp.TurnSettlementTaskSettled,
				checkpoint,
			); err != nil {
				return workerapp.TurnSettlement{}, err
			}
			current, err = coordinator.controller.advance(
				ctx,
				current,
				workerapp.TurnSettlementTaskSettled,
				checkpoint,
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}

		case workerapp.TurnSettlementTaskSettled:
			current, err = coordinator.controller.applySource(ctx, current)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}
			if current.Phase !=
				workerapp.TurnSettlementTerminalEventApplied {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"atomic Turn settlement source Apply did not advance phase",
				)
			}

		case workerapp.TurnSettlementTerminalEventApplied:
			if checkpoint.ContinuationPlan == nil {
				if coordinator.effects.planContinuation == nil {
					return workerapp.TurnSettlement{}, fmt.Errorf(
						"plan queued continuation settlement step is required",
					)
				}
				current, err = coordinator.runWithLease(
					ctx,
					current,
					func(
						effectCtx context.Context,
						_ workerapp.TurnSettlement,
					) error {
						checkpoint, err =
							coordinator.effects.planContinuation(
								effectCtx,
								checkpoint,
							)
						return err
					},
				)
				if err != nil {
					return workerapp.TurnSettlement{}, err
				}
				if err := validateRuntimeSettlementContinuationPlan(
					checkpoint,
				); err != nil {
					return workerapp.TurnSettlement{}, err
				}
				current, err = coordinator.controller.checkpoint(
					ctx,
					current,
					checkpoint,
				)
				if err != nil {
					return workerapp.TurnSettlement{}, err
				}
			}
			if coordinator.effects.settleContinuation == nil {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"settle queued continuation step is required",
				)
			}
			current, err = coordinator.runWithLease(
				ctx,
				current,
				func(
					effectCtx context.Context,
					leased workerapp.TurnSettlement,
				) error {
					checkpoint, err =
						coordinator.effects.settleContinuation(
							effectCtx,
							leased,
							checkpoint,
						)
					return err
				},
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}
			if err := validateRuntimeTurnSettlementCheckpoint(
				workerapp.TurnSettlementContinuationSettled,
				checkpoint,
			); err != nil {
				return workerapp.TurnSettlement{}, err
			}
			current, err = coordinator.controller.advance(
				ctx,
				current,
				workerapp.TurnSettlementContinuationSettled,
				checkpoint,
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}

		case workerapp.TurnSettlementContinuationSettled:
			current, err = coordinator.controller.advance(
				ctx,
				current,
				workerapp.TurnSettlementCompleted,
				checkpoint,
			)
			if err != nil {
				return workerapp.TurnSettlement{}, err
			}

		default:
			return workerapp.TurnSettlement{}, fmt.Errorf(
				"unsupported Runtime Turn settlement phase %q",
				current.Phase,
			)
		}
	}
	return current, nil
}

func decodeRuntimeTurnSettlementCheckpoint(
	phase workerapp.TurnSettlementPhase,
	payload json.RawMessage,
) (runtimeTurnSettlementCheckpoint, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Runtime Turn settlement checkpoint is missing",
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var checkpoint runtimeTurnSettlementCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"decode Runtime Turn settlement checkpoint: %w",
			err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Runtime Turn settlement checkpoint must contain one object",
		)
	}
	if phase == workerapp.TurnSettlementAccepted &&
		checkpoint.SchemaVersion == 0 &&
		checkpoint.Source == nil &&
		checkpoint.EvidenceInput == nil &&
		checkpoint.DomainPlan == nil {
		return checkpoint, nil
	}
	if err := validateRuntimeTurnSettlementCheckpoint(
		phase,
		checkpoint,
	); err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	return checkpoint, nil
}

func validateRuntimeTurnSettlementCheckpoint(
	phase workerapp.TurnSettlementPhase,
	checkpoint runtimeTurnSettlementCheckpoint,
) error {
	if checkpoint.SchemaVersion !=
		runtimeTurnSettlementCheckpointSchemaVersion {
		return fmt.Errorf(
			"Runtime Turn settlement checkpoint schema version is unsupported",
		)
	}
	if checkpoint.Source == nil ||
		checkpoint.Source.Kind == "" ||
		checkpoint.Source.Reason == "" ||
		checkpoint.EvidenceInput == nil ||
		checkpoint.EvidenceInput.TraceEndOffset <=
			checkpoint.EvidenceInput.TraceStartOffset ||
		checkpoint.EvidenceInput.TraceDigest == "" ||
		checkpoint.EvidenceInput.PatchSnapshotFile == "" ||
		checkpoint.EvidenceInput.PatchDigest == "" ||
		checkpoint.DomainPlan == nil ||
		checkpoint.DomainPlan.SessionTarget == "" ||
		checkpoint.DomainPlan.RunTarget == "" ||
		checkpoint.DomainPlan.TurnTarget == "" {
		return fmt.Errorf(
			"Runtime Turn settlement checkpoint frozen input is incomplete",
		)
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementEvidencePrepared,
	) {
		if checkpoint.Evidence == nil ||
			checkpoint.Evidence.ManifestDigest == "" {
			return fmt.Errorf(
				"Runtime Turn settlement evidence proof is missing",
			)
		}
		if checkpoint.FrozenArtifacts == nil ||
			checkpoint.EvidenceInput.ReportArtifactID !=
				checkpoint.FrozenArtifacts.ReportArtifactID ||
			checkpoint.EvidenceInput.PatchArtifactID !=
				checkpoint.FrozenArtifacts.PatchArtifactID ||
			!checkpoint.EvidenceInput.ArtifactCreatedAt.Equal(
				checkpoint.FrozenArtifacts.CreatedAt,
			) ||
			checkpoint.FrozenArtifacts.ReportArtifactID ==
				checkpoint.FrozenArtifacts.PatchArtifactID {
			return fmt.Errorf(
				"Runtime Turn settlement frozen Artifact proof is inconsistent",
			)
		}
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementDomainCommitted,
	) && (checkpoint.Domain == nil ||
		checkpoint.Domain.SessionRevision == 0 ||
		checkpoint.Domain.RunRevision == 0 ||
		checkpoint.Domain.TurnRevision == 0 ||
		checkpoint.Domain.RuntimeRevision == 0) {
		return fmt.Errorf(
			"Runtime Turn settlement domain proof is missing",
		)
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementEvidencePublished,
	) && (checkpoint.Publication == nil ||
		checkpoint.Publication.TraceStreamID == "" ||
		len(checkpoint.Publication.ArtifactIDs) == 0) {
		return fmt.Errorf(
			"Runtime Turn settlement publication proof is missing",
		)
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementTaskSettled,
	) {
		if err := validateRuntimeSettlementTaskProof(
			checkpoint,
		); err != nil {
			return err
		}
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementTerminalEventApplied,
	) && !checkpoint.TerminalSourceApplied {
		return fmt.Errorf(
			"Runtime Turn settlement terminal source proof is missing",
		)
	}
	hasBackendDeadlineSourceProof :=
		len(bytes.TrimSpace(
			checkpoint.BackendDeadlineSourceApplied,
		)) > 0 &&
			string(bytes.TrimSpace(
				checkpoint.BackendDeadlineSourceApplied,
			)) != "null"
	requiresBackendDeadlineSourceProof :=
		runtimeSettlementPhaseAtLeast(
			phase,
			workerapp.TurnSettlementTerminalEventApplied,
		) &&
			checkpoint.Source.Kind ==
				string(workerapp.TurnSettlementSourceBackendDeadline) &&
			checkpoint.Source.ForcedStop
	if requiresBackendDeadlineSourceProof !=
		hasBackendDeadlineSourceProof {
		return fmt.Errorf(
			"Runtime Turn settlement Backend deadline source proof is inconsistent",
		)
	}
	if hasBackendDeadlineSourceProof &&
		!json.Valid(checkpoint.BackendDeadlineSourceApplied) {
		return fmt.Errorf(
			"Runtime Turn settlement Backend deadline source proof is invalid",
		)
	}
	if runtimeSettlementPhaseAtLeast(
		phase,
		workerapp.TurnSettlementContinuationSettled,
	) {
		if err := validateRuntimeSettlementContinuationProof(
			checkpoint,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateRuntimeSettlementTaskPlan(
	checkpoint runtimeTurnSettlementCheckpoint,
) error {
	if checkpoint.TaskPlan == nil {
		return fmt.Errorf("Runtime Turn settlement Task plan is incomplete")
	}
	plan := checkpoint.TaskPlan
	switch plan.Action {
	case "none":
		if plan.TaskID != "" ||
			plan.ExpectedTaskRevision != 0 ||
			plan.IdempotencyKey != "" ||
			!canonical(plan.Reason) {
			return fmt.Errorf(
				"no-op Runtime Turn settlement Task plan contains effect authority",
			)
		}
		return nil
	case "settle_cancelled", "record_stage_result":
		if !canonical(plan.TaskID) ||
			plan.ExpectedTaskRevision == 0 ||
			!canonical(plan.IdempotencyKey) ||
			plan.Reason != "" {
			return fmt.Errorf(
				"Runtime Turn settlement Task plan lacks exact effect authority",
			)
		}
		return nil
	case "pause":
		if !canonical(plan.TaskID) ||
			plan.ExpectedTaskRevision == 0 ||
			!canonical(plan.IdempotencyKey) ||
			!canonical(plan.Reason) {
			return fmt.Errorf(
				"Runtime Turn settlement Task pause plan lacks exact effect authority",
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"Runtime Turn settlement Task action %q is unsupported",
			plan.Action,
		)
	}
}

func validateRuntimeSettlementTaskProof(
	checkpoint runtimeTurnSettlementCheckpoint,
) error {
	if err := validateRuntimeSettlementTaskPlan(checkpoint); err != nil {
		return err
	}
	result := checkpoint.TaskResult
	if result == nil {
		return fmt.Errorf("Runtime Turn settlement Task result is missing")
	}
	if checkpoint.TaskPlan.Action == "none" {
		if result.Status != "not_applicable" ||
			result.Revision != 0 ||
			result.EventID != "" {
			return fmt.Errorf(
				"no-op Runtime Turn settlement Task result is invalid",
			)
		}
		return nil
	}
	if result.Status != "applied" ||
		result.Revision == 0 ||
		!canonical(result.EventID) {
		return fmt.Errorf(
			"Runtime Turn settlement Task effect proof is incomplete",
		)
	}
	return nil
}

func validateRuntimeSettlementContinuationPlan(
	checkpoint runtimeTurnSettlementCheckpoint,
) error {
	if checkpoint.ContinuationPlan == nil {
		return fmt.Errorf(
			"Runtime Turn settlement continuation plan is incomplete",
		)
	}
	plan := checkpoint.ContinuationPlan
	switch plan.Action {
	case "none":
		if !canonical(plan.Reason) ||
			plan.QueueCommandID != "" ||
			plan.ExpectedSessionRevision != 0 ||
			plan.ExpectedRunRevision != 0 ||
			plan.ExpectedTurnRevision != 0 ||
			plan.ExpectedRuntimeRevision != 0 {
			return fmt.Errorf(
				"no-op Runtime Turn settlement continuation plan contains effect authority",
			)
		}
		return nil
	case "create_queued_turn":
		if plan.Reason != "" ||
			!canonical(plan.QueueCommandID) ||
			plan.ExpectedSessionRevision == 0 ||
			plan.ExpectedRunRevision == 0 ||
			plan.ExpectedTurnRevision == 0 ||
			plan.ExpectedRuntimeRevision == 0 {
			return fmt.Errorf(
				"Runtime Turn settlement continuation plan lacks exact effect authority",
			)
		}
		return nil
	default:
		return fmt.Errorf(
			"Runtime Turn settlement continuation action %q is unsupported",
			plan.Action,
		)
	}
}

func validateRuntimeSettlementContinuationProof(
	checkpoint runtimeTurnSettlementCheckpoint,
) error {
	if err := validateRuntimeSettlementContinuationPlan(
		checkpoint,
	); err != nil {
		return err
	}
	result := checkpoint.ContinuationResult
	if result == nil {
		return fmt.Errorf(
			"Runtime Turn settlement continuation result is missing",
		)
	}
	if checkpoint.ContinuationPlan.Action == "none" {
		if result.Status != "not_applicable" ||
			result.WorkerRunID != "" ||
			result.AgentTurnID != "" {
			return fmt.Errorf(
				"no-op Runtime Turn settlement continuation result is invalid",
			)
		}
		return nil
	}
	if result.Status != "created" ||
		!canonical(result.WorkerRunID) ||
		!canonical(result.AgentTurnID) {
		return fmt.Errorf(
			"Runtime Turn settlement continuation effect proof is incomplete",
		)
	}
	return nil
}

func runtimeSettlementPhaseAtLeast(
	current workerapp.TurnSettlementPhase,
	required workerapp.TurnSettlementPhase,
) bool {
	return runtimeSettlementPhaseRank(current) >=
		runtimeSettlementPhaseRank(required)
}

func runtimeSettlementPhaseRank(
	phase workerapp.TurnSettlementPhase,
) int {
	switch phase {
	case workerapp.TurnSettlementAccepted:
		return 1
	case workerapp.TurnSettlementEvidencePrepared:
		return 2
	case workerapp.TurnSettlementDomainCommitted:
		return 3
	case workerapp.TurnSettlementEvidencePublished:
		return 4
	case workerapp.TurnSettlementTaskSettled:
		return 5
	case workerapp.TurnSettlementTerminalEventApplied:
		return 6
	case workerapp.TurnSettlementContinuationSettled:
		return 7
	case workerapp.TurnSettlementCompleted:
		return 8
	default:
		return 0
	}
}

func terminalResultFromRuntimeSettlementEvent(
	event *workerapp.WorkerControlEvent,
	settlement workerapp.TurnSettlement,
	turn pendingTurn,
) (turnTerminalResult, bool, error) {
	if event == nil ||
		event.Envelope.MessageType != "agent_turn.terminal" {
		return turnTerminalResult{}, false, fmt.Errorf(
			"Runtime Turn settlement terminal event is required",
		)
	}
	var payload struct {
		UpdateKind string          `json:"update_kind"`
		Update     json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return turnTerminalResult{}, false, fmt.Errorf(
			"decode Runtime Turn settlement terminal payload: %w",
			err,
		)
	}
	var terminal turnTerminalResult
	switch payload.UpdateKind {
	case "error":
		var failure struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(payload.Update, &failure); err != nil {
			return turnTerminalResult{}, false, fmt.Errorf(
				"decode Runtime Turn settlement Agent error: %w",
				err,
			)
		}
		if !canonical(failure.Code) {
			return turnTerminalResult{}, false, fmt.Errorf(
				"Runtime Turn settlement Agent error code is invalid",
			)
		}
		reason := "agent_error"
		recoverable := false
		if failure.Code == "acp_eof" {
			reason = failure.Code
			recoverable = true
		}
		classification, err := workerapp.ClassifyRuntimeTerminalOutcome(
			turn.DeadlineAt,
			workerapp.RuntimeTerminalOutcomeCandidate{
				AcceptedAt:  settlement.AcceptedAt,
				Reason:      reason,
				Recoverable: recoverable,
			},
		)
		if err != nil {
			return turnTerminalResult{}, false, err
		}
		if classification.RecoverySignal != nil {
			return turnTerminalResult{sourceEvent: event}, true, nil
		}
		if classification.Terminal == nil {
			return turnTerminalResult{}, false, fmt.Errorf(
				"Runtime Turn terminal classification lacks an outcome",
			)
		}
		authority := *classification.Terminal
		switch authority.Source {
		case workerapp.TurnOutcomeSourceBackendDeadline:
			terminal.reason = authority.Reason
			terminal.timedOut = true
		case workerapp.TurnOutcomeSourceRuntimeEvent:
			terminal.reason = authority.Reason
		default:
			return turnTerminalResult{}, false, fmt.Errorf(
				"Runtime Turn settlement authority %q is unsupported",
				authority.Source,
			)
		}
		terminal.sourceKind = authority.Source
		terminal.occurredAt = authority.OccurredAt
	case "terminal":
		var update struct {
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(payload.Update, &update); err != nil ||
			!canonical(update.StopReason) {
			return turnTerminalResult{}, false, fmt.Errorf(
				"Runtime Turn settlement stop reason is invalid",
			)
		}
		classification, err := workerapp.ClassifyRuntimeTerminalOutcome(
			turn.DeadlineAt,
			workerapp.RuntimeTerminalOutcomeCandidate{
				AcceptedAt: settlement.AcceptedAt,
				Reason:     update.StopReason,
			},
		)
		if err != nil {
			return turnTerminalResult{}, false, err
		}
		if classification.Terminal == nil ||
			classification.RecoverySignal != nil {
			return turnTerminalResult{}, false, fmt.Errorf(
				"Runtime terminal update did not classify as terminal",
			)
		}
		authority := *classification.Terminal
		terminal.reason = authority.Reason
		if authority.Source ==
			workerapp.TurnOutcomeSourceBackendDeadline {
			terminal.timedOut = true
		}
		terminal.sourceKind = authority.Source
		terminal.occurredAt = authority.OccurredAt
	default:
		return turnTerminalResult{}, false, fmt.Errorf(
			"Runtime Turn settlement terminal update kind %q is invalid",
			payload.UpdateKind,
		)
	}
	terminal.sourceEvent = event
	return terminal, false, nil
}

func validateRuntimeSettlementEventHeader(
	eventEnvelope contractsv1.RuntimeControlEnvelope,
	settlement workerapp.TurnSettlement,
) error {
	eventDigest, err := contractsv1.RuntimeControlEnvelopeDigest(
		eventEnvelope,
	)
	if err != nil {
		return fmt.Errorf(
			"digest terminal Turn settlement event: %w",
			err,
		)
	}
	if eventEnvelope.MessageType != "agent_turn.terminal" ||
		eventEnvelope.Sequence != settlement.TerminalEventSequence ||
		eventEnvelope.MessageID != settlement.TerminalMessageID ||
		eventEnvelope.IdempotencyKey !=
			settlement.TerminalIdempotencyKey ||
		eventDigest != settlement.TerminalEnvelopeDigest ||
		uint64(eventEnvelope.ChannelEpoch) != settlement.ChannelEpoch ||
		string(eventEnvelope.WorkerSessionID) !=
			settlement.WorkerSessionID ||
		string(eventEnvelope.WorkerRunID) != settlement.WorkerRunID ||
		string(eventEnvelope.AgentSessionID) !=
			settlement.AgentSessionID ||
		string(eventEnvelope.AgentTurnID) != settlement.AgentTurnID ||
		string(eventEnvelope.RuntimeInstanceID) !=
			settlement.RuntimeInstanceID ||
		uint64(eventEnvelope.RuntimeGeneration) !=
			settlement.RuntimeGeneration {
		return fmt.Errorf(
			"terminal event differs from its durable Turn settlement header",
		)
	}
	return nil
}

func (runner *runtimeRunner) prepareRuntimeSettlementEvidence(
	ctx context.Context,
	live *liveRuntime,
	turn pendingTurn,
	settlement workerapp.TurnSettlement,
	terminal turnTerminalResult,
) (runtimeTurnSettlementCheckpoint, error) {
	if live == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"live Runtime is required to prepare Turn settlement evidence",
		)
	}
	runtimeInstanceID, runtimeGeneration, _, err :=
		runtimeSettlementRuntimeAuthority(settlement)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	if settlement.WorkerSessionID != turn.WorkerSessionID ||
		settlement.WorkerRunID != turn.WorkerRunID ||
		settlement.AgentSessionID != turn.AgentSessionID ||
		settlement.AgentTurnID != turn.AgentTurnID ||
		runtimeInstanceID != turn.RuntimeInstanceID ||
		runtimeGeneration != turn.RuntimeGeneration {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Turn settlement evidence identity differs from its durable header",
		)
	}
	switch settlement.Source.Kind {
	case workerapp.TurnSettlementSourceRuntimeEvent:
		terminal, err = validateRuntimeEventSettlementTerminal(
			terminal,
			settlement,
			turn,
		)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
	case workerapp.TurnSettlementSourceBackendDeadline:
		state, stateErr := workerapp.DecodeBackendDeadlineSourceState(
			settlement.Source,
		)
		if stateErr != nil {
			return runtimeTurnSettlementCheckpoint{}, stateErr
		}
		switch state.Phase {
		case workerapp.BackendDeadlineSourceTerminalObserved:
			terminal, err = validateRuntimeEventSettlementTerminal(
				terminal,
				settlement,
				turn,
			)
			if err != nil {
				return runtimeTurnSettlementCheckpoint{}, err
			}
		case workerapp.BackendDeadlineSourceCleanupConfirmed:
			expected, terminalErr :=
				terminalResultFromBackendDeadlineCleanup(settlement)
			if terminalErr != nil ||
				terminal.sourceEvent != nil ||
				terminal.reason != expected.reason ||
				terminal.timedOut != expected.timedOut ||
				terminal.forcedStop != expected.forcedStop ||
				terminal.sourceKind != expected.sourceKind ||
				!terminal.occurredAt.Equal(expected.occurredAt) ||
				state.ForceStop.ExpectedSessionRevision !=
					turn.SessionRevision ||
				state.ForceStop.ExpectedRuntimeRevision !=
					turn.RuntimeRevision {
				if terminalErr != nil {
					return runtimeTurnSettlementCheckpoint{},
						terminalErr
				}
				return runtimeTurnSettlementCheckpoint{},
					workerapp.ErrTurnSettlementSourceConflict
			}
			terminal = expected
		default:
			return runtimeTurnSettlementCheckpoint{},
				workerapp.ErrTurnSettlementSourceConflict
		}
	default:
		return runtimeTurnSettlementCheckpoint{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	tracePath := filepath.Join(
		live.storage.TraceSpool.BackendPath,
		"acp.raw.jsonl",
	)
	traceStart, err := restoredTurnTraceOffset(tracePath, turn)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"locate terminal AgentTurn trace boundary: %w",
			err,
		)
	}
	traceEnd, err := fileSize(tracePath)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"read terminal AgentTurn trace size: %w",
			err,
		)
	}
	rawTrace, err := readSettlementFileRange(
		tracePath,
		traceStart,
		traceEnd,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	if len(rawTrace) == 0 {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"terminal AgentTurn trace range is empty",
		)
	}
	patch, err := buildWorkspacePatch(
		ctx,
		runner.configuration.WorkspaceSourceRoot,
		live.storage.WorkingCopy.BackendPath,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	patchSnapshotFile, err := freezeRuntimeSettlementPatchSnapshot(
		live.storage.TraceSpool.BackendPath,
		turn.AgentTurnID,
		patch,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	reportArtifactID, err := runner.ids.NewID("artifact-report")
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	patchArtifactID, err := runner.ids.NewID("artifact-patch")
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	recordedAt := normalizeRuntimeSettlementTime(runner.now())
	sourceKind := terminal.sourceKind
	if sourceKind != workerapp.TurnOutcomeSourceRuntimeEvent &&
		sourceKind != workerapp.TurnOutcomeSourceBackendDeadline {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Runtime Turn settlement source authority is invalid",
		)
	}
	if terminal.occurredAt.IsZero() {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Runtime Turn settlement source occurrence time is missing",
		)
	}
	domainPlan, err := runner.runtimeSettlementDomainPlan(
		ctx,
		settlement,
		turn,
		terminal,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	sourceBytes, err := runner.captureRuntimeObservationSource(ctx, live, turn, terminal.occurredAt, recordedAt, settlement.TerminalEventSequence, rawTrace, patch)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	var observationFile, observationDigest string
	if len(sourceBytes) > 0 {
		observationFile, err = freezeRuntimeSettlementSnapshot(live.storage.TraceSpool.BackendPath, turn.AgentTurnID, "observation", "jsonl", sourceBytes)
		if err != nil {
			slog.Warn("observation_source_omitted")
			observationFile = ""
			sourceBytes = nil
		} else {
			observationDigest = runtimeSettlementBytesDigest(sourceBytes)
		}
	}
	checkpoint := runtimeTurnSettlementCheckpoint{
		SchemaVersion: runtimeTurnSettlementCheckpointSchemaVersion,
		FrozenArtifacts: &workerapp.FrozenArtifactMetadata{
			ReportArtifactID: reportArtifactID,
			PatchArtifactID:  patchArtifactID,
			CreatedAt:        recordedAt,
		},
		Source: &runtimeSettlementSourceCheckpoint{
			Kind:       string(sourceKind),
			Reason:     terminal.reason,
			OccurredAt: terminal.occurredAt,
			TimedOut:   terminal.timedOut,
			ForcedStop: terminal.forcedStop,
		},
		EvidenceInput: &runtimeSettlementEvidenceInput{
			TenantID:                      turn.TenantID,
			UserID:                        turn.UserID,
			WorkspaceID:                   turn.WorkspaceID,
			TaskID:                        turn.TaskID,
			WorkerSessionID:               turn.WorkerSessionID,
			WorkerRunID:                   turn.WorkerRunID,
			AgentSessionID:                turn.AgentSessionID,
			AgentTurnID:                   turn.AgentTurnID,
			RuntimeInstanceID:             turn.RuntimeInstanceID,
			RuntimeGeneration:             turn.RuntimeGeneration,
			NetworkPolicyID:               turn.NetworkPolicyID,
			NetworkPolicyRevision:         turn.NetworkPolicyRevision,
			NetworkPolicyDigest:           turn.NetworkPolicyDigest,
			NetworkEnforcementStatus:      turn.NetworkEnforcementStatus,
			TraceStartOffset:              traceStart,
			TraceEndOffset:                traceEnd,
			TraceDigest:                   runtimeSettlementBytesDigest(rawTrace),
			PatchSnapshotFile:             patchSnapshotFile,
			PatchSize:                     int64(len(patch)),
			PatchDigest:                   runtimeSettlementBytesDigest(patch),
			ObservationSourceSnapshotFile: observationFile,
			ObservationSourceSize:         int64(len(sourceBytes)),
			ObservationSourceDigest:       observationDigest,
			NoFilesChanged:                len(patch) == 0,
			ServerRecordedAt:              recordedAt,
			WorkerRecordedAt:              recordedAt,
			ReportArtifactID:              reportArtifactID,
			PatchArtifactID:               patchArtifactID,
			ArtifactCreatedAt:             recordedAt,
			SourceCheckpointID:            "working-copy-start-" + turn.WorkerRunID,
			SourceCheckpointDigest:        "sha256:" + live.sourceDigest,
		},
		DomainPlan: domainPlan,
	}
	report, err := runtimeObservationReport(turn, recordedAt, rawTrace, patch)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	_, digest, err := workerapp.CanonicalizeTurnSettlementCheckpoint(encoded)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	capture, err := runner.prepareRuntimeObservationBasis(ctx, turn, terminal.occurredAt, report, digest)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	checkpoint.ObservationCapture = &capture
	return checkpoint, nil
}

func (runner *runtimeRunner) runtimeSettlementDomainPlan(
	ctx context.Context,
	settlement workerapp.TurnSettlement,
	_ pendingTurn,
	terminal turnTerminalResult,
) (*runtimeSettlementDomainPlan, error) {
	var sessionRevision, runRevision, turnRevision, runtimeRevision uint64
	err := runner.pool.QueryRow(ctx, `
SELECT session.session_revision,run.revision,turn.revision,runtime.revision
FROM worker_sessions session
JOIN worker_runs run
  ON run.worker_session_id=session.worker_session_id
 AND run.worker_run_id=$2
JOIN agent_turns turn
  ON turn.worker_session_id=session.worker_session_id
 AND turn.worker_run_id=run.worker_run_id
 AND turn.agent_turn_id=$3
JOIN runtime_instances runtime
  ON runtime.worker_session_id=session.worker_session_id
 AND runtime.worker_run_id=run.worker_run_id
 AND runtime.runtime_instance_id=$4
WHERE session.worker_session_id=$1
  AND session.tenant_id=$5
  AND session.active_worker_run_id=run.worker_run_id
  AND session.active_runtime_instance_id=runtime.runtime_instance_id
  AND turn.agent_session_id=$6
  AND runtime.generation=$7`,
		settlement.WorkerSessionID,
		settlement.WorkerRunID,
		settlement.AgentTurnID,
		settlement.RuntimeInstanceID,
		settlement.TenantID,
		settlement.AgentSessionID,
		settlement.RuntimeGeneration,
	).Scan(
		&sessionRevision,
		&runRevision,
		&turnRevision,
		&runtimeRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workerapp.ErrTurnSettlementConflict
	}
	if err != nil {
		return nil, fmt.Errorf(
			"load post-terminal Runtime settlement graph revisions: %w",
			err,
		)
	}
	sessionTarget, runTarget, turnTarget :=
		residentTargetsForTerminal(terminal)
	return &runtimeSettlementDomainPlan{
		SessionTarget:           string(sessionTarget),
		RunTarget:               string(runTarget),
		TurnTarget:              string(turnTarget),
		TerminalReason:          terminal.reason,
		ExpectedSessionRevision: sessionRevision,
		ExpectedRunRevision:     runRevision,
		ExpectedTurnRevision:    turnRevision,
		ExpectedRuntimeRevision: runtimeRevision,
	}, nil
}

func validateRuntimeEventSettlementTerminal(
	terminal turnTerminalResult,
	settlement workerapp.TurnSettlement,
	turn pendingTurn,
) (turnTerminalResult, error) {
	if terminal.sourceEvent == nil ||
		terminal.sourceEvent.Envelope.MessageType !=
			"agent_turn.terminal" {
		return turnTerminalResult{}, fmt.Errorf(
			"Runtime event Turn settlement requires its exact terminal event",
		)
	}
	classified, recoveryRequired, err :=
		terminalResultFromRuntimeSettlementEvent(
			terminal.sourceEvent,
			settlement,
			turn,
		)
	if err != nil {
		return turnTerminalResult{}, err
	}
	if recoveryRequired {
		return turnTerminalResult{}, fmt.Errorf(
			"Runtime recovery terminal cannot use normal Turn settlement",
		)
	}
	if terminal.reason != classified.reason ||
		terminal.timedOut != classified.timedOut ||
		terminal.forcedStop != classified.forcedStop {
		return turnTerminalResult{}, fmt.Errorf(
			"Runtime terminal classification differs from durable settlement time",
		)
	}
	if err := validateRuntimeSettlementEventHeader(
		classified.sourceEvent.Envelope,
		settlement,
	); err != nil {
		return turnTerminalResult{}, err
	}
	return classified, nil
}

func (runner *runtimeRunner) finalizeRuntimeSettlementEvidence(
	ctx context.Context,
	live *liveRuntime,
	checkpoint runtimeTurnSettlementCheckpoint,
) (runtimeTurnSettlementCheckpoint, error) {
	input := checkpoint.EvidenceInput
	if live == nil || input == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"frozen Turn settlement evidence input is required",
		)
	}
	tracePath := filepath.Join(
		live.storage.TraceSpool.BackendPath,
		"acp.raw.jsonl",
	)
	rawTrace, err := readSettlementFileRange(
		tracePath,
		input.TraceStartOffset,
		input.TraceEndOffset,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	if runtimeSettlementBytesDigest(rawTrace) != input.TraceDigest {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"frozen Turn settlement trace digest changed",
		)
	}
	patch, err := readRuntimeSettlementPatchSnapshot(
		live.storage.TraceSpool.BackendPath,
		input.PatchSnapshotFile,
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	if int64(len(patch)) != input.PatchSize ||
		runtimeSettlementBytesDigest(patch) != input.PatchDigest {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"frozen Turn settlement WorkingCopy patch changed",
		)
	}
	var observationSource []byte
	if input.ObservationSourceSnapshotFile != "" {
		observationSource, err = readRuntimeSettlementPatchSnapshot(live.storage.TraceSpool.BackendPath, input.ObservationSourceSnapshotFile)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
		if int64(len(observationSource)) != input.ObservationSourceSize || runtimeSettlementBytesDigest(observationSource) != input.ObservationSourceDigest {
			return runtimeTurnSettlementCheckpoint{}, fmt.Errorf("frozen Turn observation snapshot changed")
		}
	}
	finalized, err := runner.finalizer.Execute(
		ctx,
		artifactapp.FinalizeTurnEvidenceCommand{
			Identity: artifactdomain.TurnIdentity{
				TenantID:    input.TenantID,
				UserID:      input.UserID,
				WorkspaceID: input.WorkspaceID,
				TaskID:      contractsv1.TaskID(input.TaskID),
				WorkerSessionID: contractsv1.WorkerSessionID(
					input.WorkerSessionID,
				),
				WorkerRunID: contractsv1.WorkerRunID(input.WorkerRunID),
				AgentSessionID: contractsv1.AgentSessionID(
					input.AgentSessionID,
				),
				AgentTurnID: contractsv1.AgentTurnID(input.AgentTurnID),
				RuntimeInstanceID: contractsv1.RuntimeInstanceID(
					input.RuntimeInstanceID,
				),
				RuntimeGeneration: uint32(input.RuntimeGeneration),
				RuntimeNetwork: artifactdomain.RuntimeNetworkEvidence{
					ID:                input.NetworkPolicyID,
					Revision:          input.NetworkPolicyRevision,
					Digest:            input.NetworkPolicyDigest,
					EnforcementStatus: input.NetworkEnforcementStatus,
				},
				TraceStreamID:   "trace-" + input.AgentTurnID,
				LangfuseTraceID: "langfuse-" + input.AgentTurnID,
			},
			ServerRecordedAt:       input.ServerRecordedAt,
			WorkerRecordedAt:       input.WorkerRecordedAt,
			RawACPJSONL:            rawTrace,
			ObservationSourceJSONL: observationSource,
			Patch:                  patch,
			NoFilesChanged:         input.NoFilesChanged,
			SourceCheckpointRef: contractsv1.CheckpointRef{
				CheckpointID: input.SourceCheckpointID,
				Digest:       input.SourceCheckpointDigest,
			},
			FrozenArtifacts: &artifactapp.FrozenArtifactMetadata{
				ReportArtifactID: input.ReportArtifactID,
				PatchArtifactID:  input.PatchArtifactID,
				CreatedAt:        input.ArtifactCreatedAt,
			},
		},
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	checkpoint.Evidence = &runtimeSettlementEvidenceCheckpoint{
		ManifestDigest: finalized.ManifestObject.Checksum,
		Finalized:      finalized,
	}
	return checkpoint, nil
}

func (runner *runtimeRunner) commitRuntimeSettlementDomain(
	ctx context.Context,
	current workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (
	workerapp.TurnSettlement,
	runtimeTurnSettlementCheckpoint,
	error,
) {
	input := checkpoint.EvidenceInput
	plan := checkpoint.DomainPlan
	if input == nil || plan == nil {
		return workerapp.TurnSettlement{},
			runtimeTurnSettlementCheckpoint{},
			fmt.Errorf(
				"frozen Turn settlement domain input is required",
			)
	}
	runtimeInstanceID, runtimeGeneration, _, err :=
		runtimeSettlementRuntimeAuthority(current)
	if err != nil {
		return workerapp.TurnSettlement{},
			runtimeTurnSettlementCheckpoint{},
			err
	}
	result, err := runner.domainSettle.Commit(
		ctx,
		workerapp.CommitTurnSettlementDomainCommand{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
			Resident: workerapp.CompleteResidentStageCommand{
				TenantID:                input.TenantID,
				WorkerSessionID:         current.WorkerSessionID,
				WorkerRunID:             current.WorkerRunID,
				AgentTurnID:             current.AgentTurnID,
				RuntimeInstanceID:       runtimeInstanceID,
				RuntimeGeneration:       runtimeGeneration,
				ExpectedSessionRevision: plan.ExpectedSessionRevision,
				ExpectedRunRevision:     plan.ExpectedRunRevision,
				ExpectedTurnRevision:    plan.ExpectedTurnRevision,
				ExpectedRuntimeRevision: plan.ExpectedRuntimeRevision,
				SessionTarget: workerdomain.WorkerSessionStatus(
					plan.SessionTarget,
				),
				RunTarget: workerdomain.WorkerRunStatus(
					plan.RunTarget,
				),
				TurnTarget: workerdomain.AgentTurnStatus(
					plan.TurnTarget,
				),
				TerminalReason: plan.TerminalReason,
			},
		},
	)
	if err != nil {
		return workerapp.TurnSettlement{},
			runtimeTurnSettlementCheckpoint{},
			err
	}
	durable, err := decodeRuntimeTurnSettlementCheckpoint(
		result.Settlement.Phase,
		result.Settlement.PhaseState,
	)
	if err != nil {
		return workerapp.TurnSettlement{},
			runtimeTurnSettlementCheckpoint{},
			err
	}
	if durable.Domain == nil ||
		durable.Domain.SessionRevision !=
			result.Proof.SessionRevision ||
		durable.Domain.RunRevision != result.Proof.RunRevision ||
		durable.Domain.TurnRevision != result.Proof.TurnRevision ||
		durable.Domain.RuntimeRevision !=
			result.Proof.RuntimeRevision {
		return workerapp.TurnSettlement{},
			runtimeTurnSettlementCheckpoint{},
			fmt.Errorf(
				"durable Turn settlement domain proof differs from commit result",
			)
	}
	return result.Settlement, durable, nil
}

func (runner *runtimeRunner) publishRuntimeSettlementEvidence(
	ctx context.Context,
	settlement workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (runtimeTurnSettlementCheckpoint, error) {
	if checkpoint.Evidence == nil ||
		checkpoint.Domain == nil ||
		checkpoint.EvidenceInput == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Turn settlement publication input is incomplete",
		)
	}
	published, err := runner.publisher.ExecuteSettlement(
		ctx,
		settlement,
		artifactapp.PublishTurnEvidenceCommand{
			Evidence:                checkpoint.Evidence.Finalized,
			ExpectedSessionRevision: checkpoint.Domain.SessionRevision,
			ExpectedRunRevision:     checkpoint.Domain.RunRevision,
			ExpectedTurnRevision:    checkpoint.Domain.TurnRevision,
			ExpectedRuntimeRevision: checkpoint.Domain.RuntimeRevision,
			IdempotencyKey: "publish-turn/" +
				checkpoint.EvidenceInput.AgentTurnID,
		},
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	checkpoint.Publication = &runtimeSettlementPublicationCheckpoint{
		TraceStreamID: published.TraceStreamID,
		ArtifactIDs:   append([]string(nil), published.ArtifactIDs...),
	}
	return checkpoint, nil
}

func (runner *runtimeRunner) planRuntimeSettlementTask(
	ctx context.Context,
	checkpoint runtimeTurnSettlementCheckpoint,
) (runtimeTurnSettlementCheckpoint, error) {
	if checkpoint.EvidenceInput == nil ||
		checkpoint.Source == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Turn settlement Task planning input is incomplete",
		)
	}
	input := checkpoint.EvidenceInput
	if input.TaskID == "" {
		checkpoint.TaskPlan = &runtimeSettlementTaskPlan{
			Action: "none",
			Reason: "no_task",
		}
		return checkpoint, nil
	}
	var status string
	var revision uint64
	if err := runner.pool.QueryRow(
		ctx,
		`SELECT status,revision FROM tasks WHERE task_id=$1`,
		input.TaskID,
	).Scan(&status, &revision); err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	plan := &runtimeSettlementTaskPlan{
		TaskID:               input.TaskID,
		ExpectedTaskRevision: revision,
	}
	switch {
	case status == "cancelled":
		plan.Action = "settle_cancelled"
		plan.IdempotencyKey = "settle-cancel/" + input.AgentTurnID
	case checkpoint.Source.TimedOut:
		plan.Action = "pause"
		plan.Reason = "agent_turn_deadline"
		plan.IdempotencyKey = "pause-turn/" + input.AgentTurnID
	case checkpoint.Source.Reason == "agent_error":
		plan.Action = "pause"
		plan.Reason = "agent_error"
		plan.IdempotencyKey = "pause-turn/" + input.AgentTurnID
	case terminalReasonCancelled(checkpoint.Source.Reason):
		plan.Action = "pause"
		plan.Reason = "user_interrupt"
		plan.IdempotencyKey = "pause-turn/" + input.AgentTurnID
	default:
		plan.Action = "record_stage_result"
		plan.IdempotencyKey = "deliver-turn/" + input.AgentTurnID
	}
	checkpoint.TaskPlan = plan
	return checkpoint, nil
}

func (runner *runtimeRunner) settleRuntimeSettlementTask(
	ctx context.Context,
	checkpoint runtimeTurnSettlementCheckpoint,
) (runtimeTurnSettlementCheckpoint, error) {
	if err := validateRuntimeSettlementTaskPlan(checkpoint); err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	plan := checkpoint.TaskPlan
	if plan.Action == "none" {
		checkpoint.TaskResult = &runtimeSettlementTaskResult{
			Status: "not_applicable",
		}
		return checkpoint, nil
	}
	if checkpoint.EvidenceInput == nil ||
		checkpoint.Evidence == nil ||
		checkpoint.Domain == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Turn settlement Task effect input is incomplete",
		)
	}
	input := checkpoint.EvidenceInput
	actor := taskapp.Actor{
		TenantID: input.TenantID,
		UserID:   input.UserID,
	}
	switch plan.Action {
	case "settle_cancelled":
		settled, err := runner.taskActions.SettleCancelled(
			ctx,
			taskapp.SettleCancelledTaskCommand{
				Actor:  actor,
				TaskID: plan.TaskID, WorkerSessionID: input.WorkerSessionID,
				WorkerRunID:             input.WorkerRunID,
				ExpectedTaskRevision:    plan.ExpectedTaskRevision,
				ExpectedSessionRevision: checkpoint.Domain.SessionRevision,
				IdempotencyKey:          plan.IdempotencyKey,
			},
		)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
		checkpoint.TaskResult = &runtimeSettlementTaskResult{
			Status:   "applied",
			Revision: settled.SessionRevision,
			EventID:  settled.EventID,
		}
	case "pause":
		paused, err := runner.pauses.Execute(
			ctx,
			taskapp.PauseTaskCommand{
				Actor:  actor,
				TaskID: plan.TaskID, WorkerSessionID: input.WorkerSessionID,
				WorkerRunID:          input.WorkerRunID,
				ExpectedTaskRevision: plan.ExpectedTaskRevision,
				Reason:               plan.Reason,
				IdempotencyKey:       plan.IdempotencyKey,
			},
		)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
		checkpoint.TaskResult = &runtimeSettlementTaskResult{
			Status:   "applied",
			Revision: paused.Revision,
			EventID:  paused.EventID,
		}
	case "record_stage_result":
		delivered, err := runner.delivery.RecordStageResult(
			ctx,
			taskapp.RecordStageResultCommand{
				Actor:  actor,
				TaskID: plan.TaskID, WorkerSessionID: input.WorkerSessionID,
				WorkerRunID:           input.WorkerRunID,
				ExpectedTaskRevision:  plan.ExpectedTaskRevision,
				ArtifactRefs:          checkpoint.Evidence.Finalized.ArtifactRefs,
				DeterministicEvidence: false,
				IdempotencyKey:        plan.IdempotencyKey,
			},
		)
		if err != nil {
			return runtimeTurnSettlementCheckpoint{}, err
		}
		checkpoint.TaskResult = &runtimeSettlementTaskResult{
			Status:   "applied",
			Revision: delivered.Revision,
			EventID:  delivered.EventID,
		}
	default:
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"unsupported Turn settlement Task action %q",
			plan.Action,
		)
	}
	return checkpoint, nil
}

func (runner *runtimeRunner) planRuntimeSettlementContinuation(
	ctx context.Context,
	checkpoint runtimeTurnSettlementCheckpoint,
) (runtimeTurnSettlementCheckpoint, error) {
	if checkpoint.EvidenceInput == nil ||
		checkpoint.Domain == nil ||
		checkpoint.TaskPlan == nil {
		return runtimeTurnSettlementCheckpoint{}, fmt.Errorf(
			"Turn settlement continuation planning input is incomplete",
		)
	}
	if checkpoint.TaskPlan.Action == "settle_cancelled" {
		checkpoint.ContinuationPlan =
			&runtimeSettlementContinuationPlan{
				Action: "none",
				Reason: "task_cancelled",
			}
		return checkpoint, nil
	}
	if checkpoint.Source != nil && checkpoint.Source.ForcedStop {
		checkpoint.ContinuationPlan =
			&runtimeSettlementContinuationPlan{
				Action: "none",
				Reason: "runtime_force_stopped",
			}
		return checkpoint, nil
	}
	var commandID string
	err := runner.pool.QueryRow(ctx, `
SELECT command_id FROM worker_user_commands
WHERE worker_session_id=$1 AND command_type='queue' AND status='accepted'
ORDER BY queue_sequence LIMIT 1`,
		checkpoint.EvidenceInput.WorkerSessionID,
	).Scan(&commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		checkpoint.ContinuationPlan =
			&runtimeSettlementContinuationPlan{
				Action: "none",
				Reason: "no_queued_turn",
			}
		return checkpoint, nil
	}
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	checkpoint.ContinuationPlan =
		&runtimeSettlementContinuationPlan{
			Action:                  "create_queued_turn",
			QueueCommandID:          commandID,
			ExpectedSessionRevision: checkpoint.Domain.SessionRevision,
			ExpectedRunRevision:     checkpoint.Domain.RunRevision,
			ExpectedTurnRevision:    checkpoint.Domain.TurnRevision,
			ExpectedRuntimeRevision: checkpoint.Domain.RuntimeRevision,
		}
	return checkpoint, nil
}

func (runner *runtimeRunner) settleRuntimeSettlementContinuation(
	ctx context.Context,
	settlement workerapp.TurnSettlement,
	checkpoint runtimeTurnSettlementCheckpoint,
) (runtimeTurnSettlementCheckpoint, error) {
	if err := validateRuntimeSettlementContinuationPlan(
		checkpoint,
	); err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	plan := checkpoint.ContinuationPlan
	if plan.Action == "none" {
		checkpoint.ContinuationResult =
			&runtimeSettlementContinuationResult{
				Status: "not_applicable",
			}
		return checkpoint, nil
	}
	input := checkpoint.EvidenceInput
	continued, err := runner.queued.ExecuteSettlement(
		ctx,
		settlement,
		workerapp.ContinueQueuedTurnCommand{
			TenantID: input.TenantID, UserID: input.UserID,
			WorkerSessionID:         input.WorkerSessionID,
			PreviousWorkerRunID:     input.WorkerRunID,
			PreviousAgentTurnID:     input.AgentTurnID,
			RuntimeInstanceID:       input.RuntimeInstanceID,
			RuntimeGeneration:       input.RuntimeGeneration,
			QueueCommandID:          plan.QueueCommandID,
			ExpectedSessionRevision: plan.ExpectedSessionRevision,
			ExpectedRunRevision:     plan.ExpectedRunRevision,
			ExpectedTurnRevision:    plan.ExpectedTurnRevision,
			ExpectedRuntimeRevision: plan.ExpectedRuntimeRevision,
		},
	)
	if err != nil {
		return runtimeTurnSettlementCheckpoint{}, err
	}
	checkpoint.ContinuationResult =
		&runtimeSettlementContinuationResult{
			Status:      "created",
			WorkerRunID: continued.WorkerRunID,
			AgentTurnID: continued.AgentTurnID,
		}
	return checkpoint, nil
}

func (runner *runtimeRunner) applyRuntimeSettlementSource(
	ctx context.Context,
	event *workerapp.WorkerControlEvent,
	current workerapp.TurnSettlement,
) (workerapp.TurnSettlement, error) {
	if current.Source.Kind ==
		workerapp.TurnSettlementSourceBackendDeadline {
		state, err := workerapp.DecodeBackendDeadlineSourceState(
			current.Source,
		)
		if err != nil {
			return workerapp.TurnSettlement{}, err
		}
		if state.Phase ==
			workerapp.BackendDeadlineSourceCleanupConfirmed {
			hasTerminalWitness, witnessErr :=
				backendDeadlineTerminalWitnessAttached(
					current,
					state,
				)
			if witnessErr != nil {
				return workerapp.TurnSettlement{}, witnessErr
			}
			if hasTerminalWitness {
				if runner.applyDeadlineTerminalWitness != nil {
					return runner.applyDeadlineTerminalWitness(
						ctx,
						current,
					)
				}
				return runner.applyDurableBackendDeadlineTerminalWitness(
					ctx,
					current,
					state,
				)
			}
			sourceStore, ok := runner.settlements.Store.(workerapp.TurnSettlementSourceStore)
			if !ok || runner.now == nil {
				return workerapp.TurnSettlement{}, fmt.Errorf(
					"Backend deadline Turn settlement source store is unavailable",
				)
			}
			return (workerapp.TurnSettlementSourceService{
				Store: sourceStore,
				Now:   runner.now,
			}).ApplyBackendDeadline(
				ctx,
				workerapp.ApplyBackendDeadlineSourceCommand{
					SettlementID:     current.SettlementID,
					ExpectedRevision: current.Revision,
					ExpectedSourceDigest: current.Source.
						Digest,
					ExpectedSourceStateDigest: current.Source.
						StateDigest,
					LeaseOwnerID: current.LeaseOwnerID,
					LeaseFence:   current.LeaseFence,
				},
			)
		}
	}
	if event == nil ||
		event.Envelope.MessageType != "agent_turn.terminal" ||
		event.Delivery == nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"Runtime Turn settlement terminal delivery is required",
		)
	}
	delivery, ok := event.Delivery.(workerapp.TurnSettlementWorkerControlEventDelivery)
	if !ok {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"Runtime Turn settlement delivery lacks fenced Apply",
		)
	}
	outcome, err := json.Marshal(struct {
		Status            string `json:"status"`
		EffectDisposition string `json:"effect_disposition"`
	}{
		Status:            "agent_turn_terminal_committed",
		EffectDisposition: "commit",
	})
	if err != nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"encode Runtime Turn settlement terminal outcome: %w",
			err,
		)
	}
	if err := delivery.ApplyTurnSettlement(
		ctx,
		outcome,
		workerapp.TurnSettlementApplyAuthority{
			SettlementID:     current.SettlementID,
			ExpectedRevision: current.Revision,
			LeaseOwnerID:     current.LeaseOwnerID,
			LeaseFence:       current.LeaseFence,
		},
	); err != nil {
		return workerapp.TurnSettlement{}, err
	}
	return runner.settlements.Load(ctx, current.SettlementID)
}

func backendDeadlineTerminalWitnessAttached(
	settlement workerapp.TurnSettlement,
	state workerapp.BackendDeadlineSourceState,
) (bool, error) {
	hasNoWitness := settlement.RuntimeInstanceID == "" &&
		settlement.RuntimeGeneration == 0 &&
		settlement.ChannelEpoch == 0 &&
		settlement.TerminalEventSequence == 0 &&
		settlement.TerminalMessageID == "" &&
		settlement.TerminalIdempotencyKey == "" &&
		settlement.TerminalEnvelopeDigest == ""
	if hasNoWitness {
		return false, nil
	}
	hasExactWitness := settlement.RuntimeInstanceID ==
		state.RuntimeInstanceID &&
		settlement.RuntimeGeneration == state.RuntimeGeneration &&
		settlement.ChannelEpoch == state.ChannelEpoch &&
		settlement.TerminalEventSequence > 0 &&
		settlement.TerminalMessageID != "" &&
		settlement.TerminalIdempotencyKey != "" &&
		settlement.TerminalEnvelopeDigest != ""
	if !hasExactWitness {
		return false, workerapp.ErrTurnSettlementSourceConflict
	}
	return true, nil
}

func (runner *runtimeRunner) applyDurableBackendDeadlineTerminalWitness(
	ctx context.Context,
	current workerapp.TurnSettlement,
	state workerapp.BackendDeadlineSourceState,
) (workerapp.TurnSettlement, error) {
	if runner == nil || runner.pool == nil || runner.now == nil ||
		runner.settlements.Store == nil ||
		state.Phase !=
			workerapp.BackendDeadlineSourceCleanupConfirmed {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"durable Backend deadline terminal Apply is unavailable",
		)
	}
	var status, processNonce string
	var streamAttempt, fenceOwnerFence, fenceRevision uint64
	var fencedAt time.Time
	err := runner.pool.QueryRow(ctx, `
SELECT status,COALESCE(process_nonce,''),stream_attempt,
       fence_owner_fence,fence_revision,fenced_at
FROM runtime_control_channels
WHERE runtime_instance_id=$1
  AND worker_session_id=$2
  AND runtime_generation=$3
  AND channel_epoch=$4`,
		state.RuntimeInstanceID,
		current.WorkerSessionID,
		state.RuntimeGeneration,
		state.ChannelEpoch,
	).Scan(
		&status,
		&processNonce,
		&streamAttempt,
		&fenceOwnerFence,
		&fenceRevision,
		&fencedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return workerapp.TurnSettlement{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	if err != nil {
		return workerapp.TurnSettlement{}, fmt.Errorf(
			"read fenced terminal witness authority: %w",
			err,
		)
	}
	if status != "fenced" ||
		processNonce == "" ||
		streamAttempt == 0 ||
		fenceOwnerFence < 2 ||
		fenceRevision == 0 ||
		fencedAt.IsZero() {
		return workerapp.TurnSettlement{},
			workerapp.ErrTurnSettlementSourceConflict
	}
	_, err = newRuntimeControlJournalService(
		runner.pool,
		runner.now,
	).ApplyEvent(
		ctx,
		workerapp.ApplyRuntimeControlEventCommand{
			Authority: workerapp.RuntimeControlAuthority{
				WorkerSessionID:   current.WorkerSessionID,
				RuntimeInstanceID: state.RuntimeInstanceID,
				RuntimeGeneration: state.RuntimeGeneration,
				ChannelEpoch:      state.ChannelEpoch,
				ProcessNonce:      processNonce,
				StreamAttempt:     streamAttempt,
				OwnerFence:        fenceOwnerFence - 1,
			},
			Sequence:       current.TerminalEventSequence,
			MessageID:      current.TerminalMessageID,
			IdempotencyKey: current.TerminalIdempotencyKey,
			Outcome: json.RawMessage(
				`{"status":"forced_terminal_ignored","effect_disposition":"ignore"}`,
			),
			TurnSettlement: &workerapp.TurnSettlementApplyAuthority{
				SettlementID:     current.SettlementID,
				ExpectedRevision: current.Revision,
				LeaseOwnerID:     current.LeaseOwnerID,
				LeaseFence:       current.LeaseFence,
			},
		},
	)
	if err != nil {
		return workerapp.TurnSettlement{}, err
	}
	return runner.settlements.Load(ctx, current.SettlementID)
}

func readSettlementFileRange(
	path string,
	start int64,
	end int64,
) ([]byte, error) {
	if path == "" || start < 0 || end <= start {
		return nil, fmt.Errorf("Turn settlement trace range is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open frozen Turn settlement trace: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat frozen Turn settlement trace: %w", err)
	}
	if info.Size() < end {
		return nil, fmt.Errorf(
			"frozen Turn settlement trace range is incomplete",
		)
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek frozen Turn settlement trace: %w", err)
	}
	content := make([]byte, end-start)
	if _, err := io.ReadFull(file, content); err != nil {
		return nil, fmt.Errorf("read frozen Turn settlement trace: %w", err)
	}
	return content, nil
}

func freezeRuntimeSettlementPatchSnapshot(
	directory string,
	agentTurnID string,
	patch []byte,
) (string, error) {
	return freezeRuntimeSettlementSnapshot(directory, agentTurnID, "patch", "patch", patch)
}

func freezeRuntimeSettlementSnapshot(directory, agentTurnID, kind, extension string, patch []byte) (string, error) {
	if directory == "" || agentTurnID == "" {
		return "", fmt.Errorf(
			"Turn settlement patch snapshot identity is required",
		)
	}
	turnSum := sha256.Sum256([]byte(agentTurnID))
	patchSum := sha256.Sum256(patch)
	name := fmt.Sprintf(
		"turn-settlement-%s-%s-%s.%s",
		kind,
		hex.EncodeToString(turnSum[:8]),
		hex.EncodeToString(patchSum[:]),
		extension,
	)
	path := filepath.Join(directory, name)
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, patch) {
			return "", fmt.Errorf(
				"frozen Turn settlement patch snapshot conflicts",
			)
		}
		return name, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf(
			"read frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	file, err := os.CreateTemp(directory, ".turn-settlement-patch-*.tmp")
	if err != nil {
		return "", fmt.Errorf(
			"create frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return "", fmt.Errorf(
			"protect frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	if _, err := file.Write(patch); err != nil {
		file.Close()
		return "", fmt.Errorf(
			"write frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf(
			"sync frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf(
			"close frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	if err := os.Link(tempPath, path); err != nil {
		if !os.IsExist(err) {
			return "", fmt.Errorf(
				"publish frozen Turn settlement patch snapshot: %w",
				err,
			)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, patch) {
			return "", fmt.Errorf(
				"frozen Turn settlement patch snapshot conflicts",
			)
		}
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return "", fmt.Errorf(
			"open Turn settlement patch snapshot directory: %w",
			err,
		)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return "", fmt.Errorf(
			"sync Turn settlement patch snapshot directory: %w",
			err,
		)
	}
	return name, nil
}

func readRuntimeSettlementPatchSnapshot(
	directory string,
	name string,
) ([]byte, error) {
	if directory == "" ||
		name == "" ||
		filepath.Base(name) != name {
		return nil, fmt.Errorf(
			"Turn settlement patch snapshot reference is invalid",
		)
	}
	patch, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return nil, fmt.Errorf(
			"read frozen Turn settlement patch snapshot: %w",
			err,
		)
	}
	return patch, nil
}

func runtimeSettlementBytesDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizeRuntimeSettlementTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func terminalReasonCancelled(reason string) bool {
	return reason == "cancelled" ||
		reason == "user_interrupt" ||
		reason == "cancel"
}

func applicationTurnSettlementCheckpoint(
	checkpoint runtimeTurnSettlementCheckpoint,
) (workerapp.TurnSettlementCheckpoint, error) {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return workerapp.TurnSettlementCheckpoint{}, fmt.Errorf(
			"encode typed Runtime Turn settlement checkpoint: %w",
			err,
		)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return workerapp.TurnSettlementCheckpoint{}, fmt.Errorf(
			"decode typed Runtime Turn settlement checkpoint fields: %w",
			err,
		)
	}
	delete(fields, "schema_version")
	delete(fields, "frozen_artifacts")
	var frozen *workerapp.FrozenArtifactMetadata
	if checkpoint.FrozenArtifacts != nil {
		copy := *checkpoint.FrozenArtifacts
		frozen = &copy
	}
	return workerapp.TurnSettlementCheckpoint{
		SchemaVersion:    checkpoint.SchemaVersion,
		FrozenArtifacts:  frozen,
		AdditionalFields: fields,
	}, nil
}
