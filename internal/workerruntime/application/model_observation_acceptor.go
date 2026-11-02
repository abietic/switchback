package application

import (
	"context"
	"encoding/json"
	"errors"
)

type ModelObservationDelivery interface {
	WorkerControlEventDelivery
	ApplyModelObservation(context.Context, json.RawMessage, RuntimeObservationCapture) error
}
type ModelObservationPreparer interface {
	PrepareModelObservation(context.Context, WorkerControlEvent) RuntimeObservationCapture
}
type ModelObservationAcceptor struct{ Preparer ModelObservationPreparer }

func (a ModelObservationAcceptor) Accept(ctx context.Context, event WorkerControlEvent) error {
	if event.Envelope.MessageType != "model.observation" {
		return errors.New("model_observation_type_invalid")
	}
	delivery, ok := event.Delivery.(ModelObservationDelivery)
	if !ok || a.Preparer == nil {
		return errors.New("model_observation_receiver_unavailable")
	}
	prepared := a.Preparer.PrepareModelObservation(ctx, event)
	return delivery.ApplyModelObservation(ctx, json.RawMessage(`{"status":"model_observation_recorded"}`), prepared.Clone())
}
