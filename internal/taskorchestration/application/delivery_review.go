package application

import (
	"context"
	"errors"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
)

var (
	ErrDeliveryReviewUnavailable = errors.New("delivery review Task is unavailable")
	ErrDeliveryReviewStale       = errors.New("delivery review target is stale")
)

type DeliveryReviewScope struct {
	Actor
	WorkspaceID string
	TaskID      string
}

type DeliveryReviewReceipt struct {
	contractsv1.DeliveryReview
	Reviewer   string    `json:"reviewer"`
	RecordedAt time.Time `json:"recorded_at"`
}

type CurrentDeliveryReview struct {
	TaskRevision uint64                 `json:"task_revision"`
	Review       *DeliveryReviewReceipt `json:"review"`
}

type DeliveryReviewStore interface {
	RecordDeliveryReview(context.Context, DeliveryReviewScope, contractsv1.DeliveryReview) (DeliveryReviewReceipt, error)
	ReadDeliveryReview(context.Context, DeliveryReviewScope) (CurrentDeliveryReview, error)
}
