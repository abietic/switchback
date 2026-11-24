package contractsv1

import (
	"fmt"
	"math"
	"unicode"
)

const DeliveryReviewSchemaVersion = "gateway.delivery_review.v1"

// DeliveryReview binds a reviewer result to one delivered report. Its verdict
// is evidence for the Task initiator and carries no Task command authority.
type DeliveryReview struct {
	SchemaVersion        string            `json:"schema_version"`
	ExpectedTaskRevision uint64            `json:"expected_task_revision"`
	WorkerRunID          WorkerRunID       `json:"worker_run_id"`
	ArtifactID           string            `json:"artifact_id"`
	ArtifactSHA256       string            `json:"artifact_sha256"`
	ReviewRunID          string            `json:"review_run_id"`
	ReviewResultSHA256   string            `json:"review_result_sha256"`
	Verdict              string            `json:"verdict"`
	Findings             []DeliveryFinding `json:"findings"`
}

type DeliveryFinding struct {
	Code    string `json:"code"`
	Summary string `json:"summary"`
}

func DecodeDeliveryReview(data []byte) (DeliveryReview, error) {
	return decodeStrict(data, DeliveryReview.Validate)
}

func (review DeliveryReview) Validate() error {
	if review.SchemaVersion != DeliveryReviewSchemaVersion || review.ExpectedTaskRevision == 0 ||
		review.ExpectedTaskRevision > math.MaxInt64 {
		return fmt.Errorf("delivery review schema or Task revision is invalid")
	}
	for name, value := range map[string]string{
		"worker_run_id": string(review.WorkerRunID), "artifact_id": review.ArtifactID, "review_run_id": review.ReviewRunID,
	} {
		if err := requireID(name, value); err != nil {
			return err
		}
	}
	if err := requireSHA256("artifact_sha256", review.ArtifactSHA256, false); err != nil {
		return err
	}
	if err := requireSHA256("review_result_sha256", review.ReviewResultSHA256, false); err != nil {
		return err
	}
	if review.Findings == nil || len(review.Findings) > 20 ||
		(review.Verdict != "passed" && review.Verdict != "rework_required") ||
		(review.Verdict == "passed") != (len(review.Findings) == 0) {
		return fmt.Errorf("delivery review verdict or findings are invalid")
	}
	for _, finding := range review.Findings {
		if err := requireID("finding code", finding.Code); err != nil {
			return err
		}
		if err := requireString("finding summary", finding.Summary); err != nil || len(finding.Summary) > 800 {
			return fmt.Errorf("finding summary must be a nonempty bounded line")
		}
		for _, char := range finding.Summary {
			if unicode.IsControl(char) {
				return fmt.Errorf("finding summary contains a control character")
			}
		}
	}
	return nil
}
