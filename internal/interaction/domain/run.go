package domain

import (
	"fmt"
	"time"
)

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunClaimed   RunStatus = "claimed"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type TerminalOutcome struct {
	Status     RunStatus
	ErrorCode  string
	Retryable  bool
	Decision   []byte
	OccurredAt time.Time
}

type AssistantRun struct {
	ID                  string
	ConversationID      string
	SourceTurnID        string
	Sequence            uint64
	Status              RunStatus
	ModelProfile        string
	PolicyRevision      string
	ContextBudgetTokens int
	TimeoutAt           time.Time
	LeaseOwner          string
	LeaseExpiresAt      time.Time
	FencingToken        uint64
	AttemptCount        uint64
	Decision            []byte
	ErrorCode           string
	Retryable           bool
	Revision            uint64
	CreatedAt           time.Time
	StartedAt           time.Time
	CompletedAt         time.Time
	UpdatedAt           time.Time
}

func (run *AssistantRun) Claim(expectedRevision uint64, leaseOwner string, leaseUntil, now time.Time) error {
	if run.Revision != expectedRevision {
		return ErrStaleRevision
	}
	if run.Status != RunQueued || !canonical(leaseOwner) || !leaseUntil.After(now) ||
		now.IsZero() || now.Before(run.UpdatedAt) || !now.Before(run.TimeoutAt) {
		return ErrInvalidState
	}
	run.Status = RunClaimed
	run.LeaseOwner = leaseOwner
	run.LeaseExpiresAt = leaseUntil.UTC()
	run.FencingToken++
	run.AttemptCount++
	run.StartedAt = now.UTC()
	run.advance(now)
	return nil
}

func (run *AssistantRun) RequeueExpired(expectedRevision, fencingToken uint64, now time.Time) error {
	if run.Revision != expectedRevision {
		return ErrStaleRevision
	}
	if run.Status != RunClaimed || run.FencingToken != fencingToken ||
		now.Before(run.LeaseExpiresAt) || !now.Before(run.TimeoutAt) {
		return ErrInvalidState
	}
	run.Status = RunQueued
	run.LeaseOwner = ""
	run.LeaseExpiresAt = time.Time{}
	run.advance(now)
	return nil
}

func (run *AssistantRun) Finish(expectedRevision, fencingToken uint64, outcome TerminalOutcome) error {
	if run.Revision != expectedRevision {
		return ErrStaleRevision
	}
	if run.Status != RunClaimed || run.FencingToken != fencingToken || outcome.OccurredAt.IsZero() ||
		outcome.OccurredAt.Before(run.UpdatedAt) {
		return ErrInvalidState
	}
	switch outcome.Status {
	case RunCompleted:
		if len(outcome.Decision) == 0 || outcome.ErrorCode != "" || outcome.Retryable {
			return fmt.Errorf("completed AssistantRun requires one decision")
		}
	case RunFailed:
		if !canonical(outcome.ErrorCode) || len(outcome.Decision) != 0 {
			return fmt.Errorf("failed AssistantRun requires one sanitized error")
		}
	case RunCancelled:
		if !canonical(outcome.ErrorCode) || outcome.Retryable || len(outcome.Decision) != 0 {
			return fmt.Errorf("cancelled AssistantRun outcome is invalid")
		}
	default:
		return ErrInvalidState
	}
	run.Status = outcome.Status
	run.Decision = append([]byte(nil), outcome.Decision...)
	run.ErrorCode = outcome.ErrorCode
	run.Retryable = outcome.Retryable
	run.LeaseOwner = ""
	run.LeaseExpiresAt = time.Time{}
	run.CompletedAt = outcome.OccurredAt.UTC()
	run.advance(outcome.OccurredAt)
	return nil
}

func (run AssistantRun) Terminal() bool {
	return run.Status == RunCompleted || run.Status == RunFailed || run.Status == RunCancelled
}

func (run *AssistantRun) advance(now time.Time) {
	run.Revision++
	run.UpdatedAt = now.UTC()
}
