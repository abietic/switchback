package main

type registeredAddition struct {
	ID     string
	SHA256 string
	Groups []string
}

const registeredBaseline = "2ee4c24bfd43621dae7acc73445fece17e73f5ae"

// Reviewed normalized AST token snapshots. Update only through explicit review;
// these are not a runtime allowlist and do not protect a hostile host editor.
var registeredAdditions = []registeredAddition{
	{ID: "test/acceptance/observability/TestFullStackReadOnlyCLI", SHA256: "cfd93b28b32b84b5a83bb486eeeb0644dccc4d3288507746cd9a53ba3a129ebb", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyClaimPrecheck", SHA256: "bdb50de5ebe397303bf0851af70be480cc2ee08e2c7d0634b17abad5cc774e73", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyColdPreservesEvidence", SHA256: "e565c6b3d77a70523ccdcbf2ced4064f0484b0cc27e99d3299773f645edcdb6d", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyColdRejectsBrokenEvidence", SHA256: "cfbc456ca872452faba948660c575673012654f44c2f6c7b3673d28296a6d7fe", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyExistingPassAndRepeat", SHA256: "462ce127e8764498dccf2f6260843a75437fe5138fdead47d7695debe537cafa", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyMatrixPreservesEvidence", SHA256: "717653ced794e39c38577f967aeb0255e02889d1ef58e6257864a65de91c47de", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyMatrixRejectsBrokenEvidence", SHA256: "657fd1a1d3886c6675fc617055f3271282ab14770a1d993e64f0f215179d9cde", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyOutputFailure", SHA256: "b27b2803a27835f9b44d806a6f64104cb5bc6c4d0a86a12ec391b6ca73c051a0", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/TestFullStackReadOnlyRejectsRouteBeforeReading", SHA256: "4c2a906b03be714e763e37ab9807f5ad3d6e5f2363d0745e3d2ef0d079a8b6d2", Groups: []string{"", "integration"}},
	{ID: "test/acceptance/observability/readonlyDirectorySnapshot", SHA256: "bc03729ab19add34f519f7b7e0844ad56138a3c984766474553b64895a0fd9ca"},
	{ID: "test/acceptance/observability/readonlyFixture", SHA256: "a0ed97c371757a17042cb2e429fcde45a8ad9946cb813e75fbf2c86bf550be18"},
	{ID: "test/acceptance/observability/readonlyReject", SHA256: "073efa70f992273ce1c954e469b7b084425036f79a281a7a05eb155a11da4397"},
	{ID: "test/acceptance/observability/Write/readonlyOutputWriter", SHA256: "1df00f590b9e764239bb22a0dbb88f5853471655a5d7fbc574318f6f99bd2eeb"},
}
