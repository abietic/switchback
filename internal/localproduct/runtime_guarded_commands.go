package localproduct

import (
	"context"
	"log/slog"

	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
)

type runtimeGuardedCommands struct {
	commands gatewayhttp.CommandService
	policy   runtimeStartPolicy
}

var _ gatewayhttp.CommandService = runtimeGuardedCommands{}

func (commands runtimeGuardedCommands) CreateTask(ctx context.Context, actor gatewayhttp.FixedActor, input gatewayhttp.DirectTaskInput) (gatewayhttp.CommandResult, error) {
	return commands.commands.CreateTask(ctx, actor, input)
}

func (commands runtimeGuardedCommands) ExecuteTaskCommand(ctx context.Context, command gatewayhttp.TaskCommand) (gatewayhttp.CommandResult, error) {
	if command.Type == "continue" || command.Type == "rework" {
		if err := commands.rejectStart(ctx); err != nil {
			return gatewayhttp.CommandResult{}, err
		}
	}
	return commands.commands.ExecuteTaskCommand(ctx, command)
}

func (commands runtimeGuardedCommands) ExecuteSessionCommand(ctx context.Context, command gatewayhttp.SessionCommand) (gatewayhttp.CommandResult, error) {
	if command.Type == "follow-up" {
		if err := commands.rejectStart(ctx); err != nil {
			return gatewayhttp.CommandResult{}, err
		}
	}
	return commands.commands.ExecuteSessionCommand(ctx, command)
}

func (commands runtimeGuardedCommands) rejectStart(ctx context.Context) error {
	if err := checkRuntimeStart(ctx, commands.policy); err != nil {
		slog.WarnContext(ctx, "reject new Runtime execution", "error", err)
		return gatewayhttp.ErrCommandConflict
	}
	return nil
}
