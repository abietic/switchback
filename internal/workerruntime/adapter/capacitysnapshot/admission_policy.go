package capacitysnapshot

import (
	platformconfig "github.com/abietic/switchback/internal/platform/config"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

func validateTrustAdmissionPolicy(ref *workerapp.CapacityGuaranteeRef) error {
	if ref == nil {
		return nil // legacy, never fill a default policy
	}
	return (platformconfig.RuntimeCapacityGuarantee{ID: ref.ID, Revision: ref.Revision, Digest: ref.Digest}).Validate()
}
