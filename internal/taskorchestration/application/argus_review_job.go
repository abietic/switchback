package application

import contractsv1 "github.com/abietic/switchback/internal/contracts/v1"

// ArgusReviewJob freezes the eligible delivery when the review is first queued.
// Attempt fences the current claimant, while the delivery identity survives reclaim.
type ArgusReviewJob struct {
	DeliveryReviewScope
	TaskRevision   uint64
	WorkerRunID    contractsv1.WorkerRunID
	ArtifactID     string
	ArtifactSHA256 string
	ProfileSHA256  string
	Attempt        int
}
