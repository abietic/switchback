package application

import (
	"context"
	"errors"
	"fmt"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

var ErrRuntimeFailureRevisionConflict = errors.New("Runtime failure Task binding revision conflict")

// The reader resolves the failed incident's original Run, never the Session's
// most recent Run. A false result is a proven Worker-only or superseded outcome.
type RuntimeFailureTaskReader interface {
	ReadRuntimeFailureTask(context.Context, contractsv1.IntegrationEvent) (PauseTaskCommand, bool, error)
}

type RuntimeFailureConsumer struct {
	Reader RuntimeFailureTaskReader
	Pause  TaskPauseService
}

func (consumer RuntimeFailureConsumer) Consume(ctx context.Context, event contractsv1.IntegrationEvent) error {
	if event.EventType != "RuntimeRecoveryFailed.v1" {
		return nil
	}
	if consumer.Reader == nil {
		return fmt.Errorf("Runtime failure Task reader is required")
	}
	// One bounded reread handles a competing Task revision. Further contention
	// returns to the durable dispatcher instead of spinning or swallowing errors.
	for attempt := 0; attempt < 2; attempt++ {
		command, found, err := consumer.Reader.ReadRuntimeFailureTask(ctx, event)
		if err != nil || !found {
			return err
		}
		_, err = consumer.Pause.Execute(ctx, command)
		if !errors.Is(err, ErrRuntimeFailureRevisionConflict) || attempt == 1 {
			return err
		}
	}
	return ErrRuntimeFailureRevisionConflict
}
