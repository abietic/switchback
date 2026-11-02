package application_test

import (
	app "github.com/abietic/switchback/internal/artifacttrace/application"
	"testing"
)

func TestObservationBundleRejectsDuplicateKeys(t *testing.T) {
	raw := []byte(`{"schema_version":"observation.capture.bundle.v1","schema_version":"observation.capture.bundle.v1","observations":[{"Frame":null,"Receipt":null,"ErrorCode":"scanner_unavailable"}]}`)
	if _, err := app.DecodeSealedObservationBundle(raw); err == nil {
		t.Fatal("duplicate bundle keys accepted")
	}
}

func TestObservationBundleNoContentIsStrictAndCopied(t *testing.T) {
	b := app.SealedObservationBundle{SchemaVersion: "observation.capture.bundle.v1", Observations: []app.PreparedObservation{{ErrorCode: "scanner_unavailable"}}}
	raw, err := app.EncodeSealedObservationBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := app.DecodeSealedObservationBundle(raw)
	if err != nil || decoded.Observations[0].ErrorCode != "scanner_unavailable" {
		t.Fatalf("safe bundle: %+v %v", decoded, err)
	}
	b.Observations[0].ErrorCode = "provider text containing a credential"
	if _, err := app.EncodeSealedObservationBundle(b); err == nil {
		t.Fatal("arbitrary error leaked into bundle")
	}
	b.Observations[0] = app.PreparedObservation{ErrorCode: "scanner_unavailable", Frame: []byte("unscanned")}
	if _, err := app.EncodeSealedObservationBundle(b); err == nil {
		t.Fatal("no-content bundle retained content")
	}
}
