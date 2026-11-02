package capacitysnapshot

import "testing"

// QualificationFixtureForMapperTest exists ONLY in the test build. It returns
// copies after complete private semantic validation, never a production handle,
// constructor, verifier seam, or file-loading API. The external composition test
// must call the unchanged publisher Mapper.Map with this exact decimal Trust.
func QualificationFixtureForMapperTest(t *testing.T) (Trust, DeploymentPublisherBinding) {
	t.Helper()
	tr, files := qualificationFixture(t)
	result, e := validateDeploymentQualification(tr, files)
	if e != nil {
		t.Fatal(e)
	}
	return qualificationClone(tr), qualificationClone(result.binding)
}
