package postgres

import (
	"context"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

func TestRequirePublisherSchemaSupportFailsClosed(t *testing.T) {
	publisher := schemaAwarePublisherStub{
		supported: []string{contractsv1.IntegrationEventLegacySchemaVersion},
	}
	if err := requirePublisherSchemaSupport(publisher, contractsv1.IntegrationEventLegacySchemaVersion); err != nil {
		t.Fatalf("legacy consumer rejected event.v1: %v", err)
	}
	if err := requirePublisherSchemaSupport(publisher, contractsv1.IntegrationEventSchemaVersion); err == nil {
		t.Fatal("legacy consumer accepted event.v2")
	}
}

type schemaAwarePublisherStub struct {
	supported []string
}

func (publisher schemaAwarePublisherStub) SupportedIntegrationEventSchemaVersions() []string {
	return append([]string(nil), publisher.supported...)
}

func (schemaAwarePublisherStub) Publish(context.Context, contractsv1.IntegrationEvent) error {
	return nil
}
