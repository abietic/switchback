package secretcheck_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	artifactlangfuse "github.com/abietic/switchback/internal/artifacttrace/adapter/langfuse"
	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const knownValue = "known-value-never-persist-12345"

func TestScannerCoversObjectStoreLogsDiagnosticsAndLangfuseQuery(t *testing.T) {
	t.Parallel()
	scanner := newScanner(t)
	ctx := context.Background()

	t.Run("object store", func(t *testing.T) {
		store, err := artifactlocal.NewObjectStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		identity := workerapp.ObjectIdentity{
			TenantID: "tenant-local", UserID: "user-local", WorkspaceID: "workspace-demo",
			WorkerSessionID: "session-1", WorkerRunID: "run-1", AgentTurnID: "turn-1", Kind: "known-token-test",
		}
		temporary, err := store.PutTemporary(ctx, identity, strings.NewReader("payload "+knownValue))
		if err != nil {
			t.Fatal(err)
		}
		stored, err := store.Finalize(ctx, identity, temporary)
		if err != nil {
			t.Fatal(err)
		}
		assertSafeExposure(t, scanner.ScanObject(ctx, store, stored.Key), "objectstore:")
	})

	for _, test := range []struct {
		name   string
		source string
		scan   func() error
	}{
		{name: "application logs", source: "application.logs", scan: func() error {
			return scanner.ScanReader(ctx, "application.logs", strings.NewReader("request failed "+knownValue))
		}},
		{name: "sanitized diagnostics", source: "sanitized.diagnostics", scan: func() error {
			return scanner.ScanJSON(ctx, "sanitized.diagnostics", map[string]string{"message": "failed " + knownValue})
		}},
		{name: "Langfuse query", source: "langfuse.query.trace", scan: func() error {
			return scanner.ScanJSON(ctx, "langfuse.query.trace", artifactlangfuse.Trace{
				ID: "trace-1", Observations: []artifactlangfuse.Observation{{
					ID: "model-1", Metadata: map[string]any{"unsafe": knownValue},
				}},
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertSafeExposure(t, test.scan(), test.source)
		})
	}
}

func TestScannerFindsTokenAcrossReaderChunksAndAcceptsCleanSources(t *testing.T) {
	t.Parallel()
	scanner := newScanner(t)
	reader := &oneByteReader{content: []byte("prefix " + knownValue + " suffix")}
	err := scanner.ScanReader(context.Background(), "application.logs", reader)
	assertSafeExposure(t, err, "application.logs")
	if err := scanner.ScanBytes(context.Background(), "application.logs", []byte("clean content")); err != nil {
		t.Fatalf("clean scan error = %v", err)
	}
}

func TestScannerDiagnosticFormattingIsRedactedForPointerAndValue(t *testing.T) {
	t.Parallel()
	scanner := newScanner(t)

	diagnostic := fmt.Sprintf("%v %#v %v %#v", scanner, scanner, *scanner, *scanner)
	if strings.Contains(diagnostic, knownValue) {
		t.Fatalf("scanner diagnostic leaked token = %q", diagnostic)
	}
	if strings.Count(diagnostic, "[REDACTED]") != 4 {
		t.Fatalf("scanner diagnostic was not uniformly redacted = %q", diagnostic)
	}
}

func newScanner(t *testing.T) *secretcheck.Scanner {
	t.Helper()
	scanner, err := secretcheck.New([]secretcheck.KnownToken{{Label: "model-key", Value: knownValue}})
	if err != nil {
		t.Fatal(err)
	}
	return scanner
}

func assertSafeExposure(t *testing.T, err error, sourcePrefix string) {
	t.Helper()
	var exposure *secretcheck.ExposureError
	if !errors.As(err, &exposure) || exposure.TokenLabel != "model-key" ||
		!strings.HasPrefix(exposure.Source, sourcePrefix) {
		t.Fatalf("exposure = %#v error=%v", exposure, err)
	}
	diagnostic := fmt.Sprintf("%v %#v", err, exposure)
	if strings.Contains(diagnostic, knownValue) {
		t.Fatalf("exposure diagnostic leaked token = %q", diagnostic)
	}
}

type oneByteReader struct {
	content []byte
}

func (reader *oneByteReader) Read(target []byte) (int, error) {
	if len(reader.content) == 0 {
		return 0, io.EOF
	}
	target[0] = reader.content[0]
	reader.content = reader.content[1:]
	if len(reader.content) == 0 {
		return 1, io.EOF
	}
	return 1, nil
}
