package capacitysnapshot

import "github.com/abietic/switchback/internal/platform/config"

type qualificationProfiles struct {
	resource, network config.RuntimeResourceProfileRef
}

func qualificationProfileRefs() (qualificationProfiles, error) {
	p, ok := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	if !ok {
		return qualificationProfiles{}, errQualification
	}
	n, e := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(config.SingleUserLocalProfileName)
	if e != nil {
		return qualificationProfiles{}, e
	}
	return qualificationProfiles{p.Ref(), config.RuntimeResourceProfileRef{ID: n.ID, Revision: n.Revision, Digest: n.Digest}}, nil
}
