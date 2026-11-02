package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrScheduledCommandLeaseLost = errors.New("ScheduledCommand lease is no longer owned")

var ErrScheduledCommandFinalization = errors.New(
	"ScheduledCommand lease finalization failed",
)

const scheduledCommandFinalizationTimeout = 5 * time.Second

type ScheduledCommand struct {
	ID               string
	TenantID         string
	CommandType      string
	TargetType       string
	TargetID         string
	ExpectedRevision uint64
	Payload          json.RawMessage
	DueAt            time.Time
	FencingToken     uint64
}

type ScheduledCommandHandler interface {
	HandleScheduledCommand(context.Context, ScheduledCommand) error
}

type ScheduledCommandPoller struct {
	Pool          *pgxpool.Pool
	Handler       ScheduledCommandHandler
	LeaseOwner    string
	LeaseDuration time.Duration
	BatchSize     int
	Now           func() time.Time
	TenantID      string
	CommandTypes  []string
}

func (poller ScheduledCommandPoller) RunOnce(ctx context.Context) (int, error) {
	if poller.Pool == nil || poller.Handler == nil || poller.Now == nil || poller.LeaseOwner == "" ||
		poller.LeaseDuration <= 0 || poller.BatchSize <= 0 {
		return 0, fmt.Errorf("ScheduledCommand poller dependencies are required")
	}
	if err := poller.validateFilters(); err != nil {
		return 0, err
	}
	commands, err := poller.claim(ctx, poller.Now().UTC())
	if err != nil {
		return 0, err
	}
	applied := 0
	var foundErrors []error
	for _, command := range commands {
		if err := poller.Handler.HandleScheduledCommand(ctx, command); err != nil {
			finalizeCtx, cancelFinalize :=
				scheduledCommandFinalizationContext(ctx)
			releaseErr := poller.release(
				finalizeCtx,
				command,
				errorCode(err),
				poller.Now().UTC(),
			)
			cancelFinalize()
			if releaseErr != nil {
				foundErrors = append(
					foundErrors,
					errors.Join(
						err,
						ErrScheduledCommandFinalization,
						releaseErr,
					),
				)
			} else {
				foundErrors = append(foundErrors, fmt.Errorf("handle ScheduledCommand %s: %w", command.ID, err))
			}
			continue
		}
		finalizeCtx, cancelFinalize :=
			scheduledCommandFinalizationContext(ctx)
		completeErr := poller.complete(
			finalizeCtx,
			command,
			poller.Now().UTC(),
		)
		cancelFinalize()
		if completeErr != nil {
			foundErrors = append(
				foundErrors,
				errors.Join(
					ErrScheduledCommandFinalization,
					completeErr,
				),
			)
			continue
		}
		applied++
	}
	return applied, errors.Join(foundErrors...)
}

func (poller ScheduledCommandPoller) HasDueOutstanding(
	ctx context.Context,
) (bool, error) {
	if poller.Pool == nil || poller.Now == nil {
		return false, fmt.Errorf(
			"ScheduledCommand due-outstanding dependencies are required",
		)
	}
	if err := poller.validateFilters(); err != nil {
		return false, err
	}
	commandTypes := append([]string{}, poller.CommandTypes...)
	var outstanding bool
	if err := poller.Pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM scheduled_commands
  WHERE due_at <= $1
    AND status IN ('pending','leased')
    AND ($2='' OR tenant_id=$2)
    AND (cardinality($3::text[])=0 OR command_type=ANY($3::text[]))
)`,
		poller.Now().UTC(),
		poller.TenantID,
		commandTypes,
	).Scan(&outstanding); err != nil {
		return false, fmt.Errorf(
			"query due outstanding ScheduledCommands: %w",
			err,
		)
	}
	return outstanding, nil
}

func (poller ScheduledCommandPoller) validateFilters() error {
	if poller.TenantID != strings.TrimSpace(poller.TenantID) {
		return fmt.Errorf("ScheduledCommand tenant filter is invalid")
	}
	for _, commandType := range poller.CommandTypes {
		if commandType == "" ||
			commandType != strings.TrimSpace(commandType) {
			return fmt.Errorf("ScheduledCommand type filter is invalid")
		}
	}
	return nil
}

func scheduledCommandFinalizationContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		context.WithoutCancel(ctx),
		scheduledCommandFinalizationTimeout,
	)
}

func (poller ScheduledCommandPoller) claim(ctx context.Context, now time.Time) ([]ScheduledCommand, error) {
	var commands []ScheduledCommand
	commandTypes := append([]string{}, poller.CommandTypes...)
	err := InTransaction(ctx, poller.Pool, func(tx Tx) error {
		rows, err := tx.Query(ctx, `
WITH claimable AS (
  SELECT scheduled_command_id
  FROM scheduled_commands
  WHERE due_at <= $1
    AND (status='pending' OR (status='leased' AND lease_expires_at <= $1))
    AND ($5='' OR tenant_id=$5)
    AND (cardinality($6::text[])=0 OR command_type=ANY($6::text[]))
  ORDER BY due_at,scheduled_command_id
  FOR UPDATE SKIP LOCKED
  LIMIT $2
), claimed AS (
  UPDATE scheduled_commands AS command
  SET status='leased',lease_owner=$3,lease_expires_at=$4,
      fencing_token=command.fencing_token+1,attempt_count=command.attempt_count+1,updated_at=$1
  FROM claimable
  WHERE command.scheduled_command_id=claimable.scheduled_command_id
  RETURNING command.scheduled_command_id,command.tenant_id,command.command_type,command.target_type,
    command.target_id,command.expected_revision,command.payload,command.due_at,command.fencing_token
)
SELECT * FROM claimed ORDER BY due_at,scheduled_command_id`,
			now,
			poller.BatchSize,
			poller.LeaseOwner,
			now.Add(poller.LeaseDuration),
			poller.TenantID,
			commandTypes,
		)
		if err != nil {
			return fmt.Errorf("claim ScheduledCommands: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var command ScheduledCommand
			if err := rows.Scan(
				&command.ID, &command.TenantID, &command.CommandType, &command.TargetType,
				&command.TargetID, &command.ExpectedRevision, &command.Payload,
				&command.DueAt, &command.FencingToken,
			); err != nil {
				return fmt.Errorf("scan ScheduledCommand: %w", err)
			}
			commands = append(commands, command)
		}
		return rows.Err()
	})
	return commands, err
}

func (poller ScheduledCommandPoller) complete(ctx context.Context, command ScheduledCommand, now time.Time) error {
	tag, err := poller.Pool.Exec(ctx, `
UPDATE scheduled_commands
SET status='applied',lease_owner=NULL,lease_expires_at=NULL,last_error_code=NULL,updated_at=$5
WHERE scheduled_command_id=$1 AND status='leased' AND lease_owner=$2 AND fencing_token=$3
  AND lease_expires_at > $4`, command.ID, poller.LeaseOwner, command.FencingToken, now, now)
	if err != nil {
		return fmt.Errorf("complete ScheduledCommand %s: %w", command.ID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: command=%s token=%d", ErrScheduledCommandLeaseLost, command.ID, command.FencingToken)
	}
	return nil
}

func (poller ScheduledCommandPoller) release(
	ctx context.Context,
	command ScheduledCommand,
	code string,
	now time.Time,
) error {
	tag, err := poller.Pool.Exec(ctx, `
UPDATE scheduled_commands
SET status='pending',lease_owner=NULL,lease_expires_at=NULL,last_error_code=$4,updated_at=$5
WHERE scheduled_command_id=$1 AND status='leased' AND lease_owner=$2 AND fencing_token=$3`,
		command.ID, poller.LeaseOwner, command.FencingToken, code, now)
	if err != nil {
		return fmt.Errorf("release ScheduledCommand %s: %w", command.ID, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: command=%s token=%d", ErrScheduledCommandLeaseLost, command.ID, command.FencingToken)
	}
	return nil
}
