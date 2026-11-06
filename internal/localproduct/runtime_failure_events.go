package localproduct

import (
	"context"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	platformpostgres "github.com/abietic/switchback/internal/platform/postgres"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

// Domain consumption must commit before any page projection acknowledges the
// Worker event. A projection/response failure remains a durable Outbox retry.
type runtimeFailureEventPublisher struct {
	Consumer taskapp.RuntimeFailureConsumer
	Next     platformpostgres.EventPublisher
}

func (publisher runtimeFailureEventPublisher) Publish(ctx context.Context, event contractsv1.IntegrationEvent) error {
	if err := publisher.Consumer.Consume(ctx, event); err != nil {
		return err
	}
	return publisher.Next.Publish(ctx, event)
}

func (publisher runtimeFailureEventPublisher) SupportedIntegrationEventSchemaVersions() []string {
	return publisher.Next.SupportedIntegrationEventSchemaVersions()
}
