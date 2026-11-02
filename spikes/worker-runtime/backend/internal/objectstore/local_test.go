package objectstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"switchback.local/spikes/worker-runtime/internal/redact"
)

func TestParseUploadEvidenceKindAcceptsOnlyUploadVocabulary(t *testing.T) {
	tests := []struct {
		input string
		want  EvidenceKind
		ok    bool
	}{
		{input: "raw-acp", want: EvidenceRawACP, ok: true},
		{input: "trace", want: EvidenceTrace, ok: true},
		{input: "report", want: EvidenceReport, ok: true},
		{input: "patch", want: EvidencePatch, ok: true},
		{input: "manifest"},
		{input: "acceptance"},
		{input: "raw_acp"},
		{input: "unknown"},
		{input: ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseUploadEvidenceKind(tt.input)
			if tt.ok {
				if err != nil {
					t.Fatalf("ParseUploadEvidenceKind(%q) error = %v", tt.input, err)
				}
				if got != tt.want {
					t.Fatalf("ParseUploadEvidenceKind(%q) = %q, want %q", tt.input, got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseUploadEvidenceKind(%q) error = nil, want rejection", tt.input)
			}
		})
	}
}

func TestLocalFileObjectStoreWritesRedactedObjectAtomically(t *testing.T) {
	root := t.TempDir()
	store := LocalFileObjectStore{
		Root: root,
		Redactor: redact.New(redact.Secrets{
			UpstreamModelKey: "upstream-secret",
		}),
	}
	identity := ObjectIdentity{
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentTurnID:     "turn-1",
		Kind:            EvidenceTrace,
	}
	target := filepath.Join(root, "session-1", "run-1", "turns", "turn-1", "trace.jsonl")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("WriteFile(old target) error = %v", err)
	}

	seenTemporary := false
	store.rename = func(source, destination string) error {
		if destination != target {
			t.Fatalf("rename destination = %q, want %q", destination, target)
		}
		if filepath.Dir(source) != filepath.Dir(destination) {
			t.Fatalf("temporary file directory = %q, want %q", filepath.Dir(source), filepath.Dir(destination))
		}
		if got, err := os.ReadFile(destination); err != nil || string(got) != "old" {
			t.Fatalf("target before rename = %q, err = %v, want old", got, err)
		}
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("temporary file stat error = %v", err)
		}
		seenTemporary = true
		return os.Rename(source, destination)
	}

	stored, err := store.Put(identity, []byte("line with upstream-secret\n"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if !seenTemporary {
		t.Fatal("Put() did not rename a same-directory temporary file")
	}
	wantPayload := "line with [REDACTED:upstream_model_key]\n"
	wantDigest := sha256.Sum256([]byte(wantPayload))
	if stored.Path != target {
		t.Fatalf("stored path = %q, want %q", stored.Path, target)
	}
	if stored.SHA256 != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("stored sha256 = %q, want %q", stored.SHA256, hex.EncodeToString(wantDigest[:]))
	}
	if stored.Bytes != int64(len(wantPayload)) {
		t.Fatalf("stored bytes = %d, want %d", stored.Bytes, len(wantPayload))
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != wantPayload {
		t.Fatalf("stored payload = %q, want %q", got, wantPayload)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".trace.jsonl-") {
			t.Fatalf("temporary file %q was not cleaned", entry.Name())
		}
	}
}

func TestLocalFileObjectStoreRejectsUnsafeIdentitiesAndKinds(t *testing.T) {
	store := LocalFileObjectStore{Root: t.TempDir()}
	valid := ObjectIdentity{
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentTurnID:     "turn-1",
		Kind:            EvidenceReport,
	}
	tests := []struct {
		name   string
		mutate func(*ObjectIdentity)
	}{
		{name: "empty session ID", mutate: func(identity *ObjectIdentity) { identity.WorkerSessionID = "" }},
		{name: "separator in run ID", mutate: func(identity *ObjectIdentity) { identity.WorkerRunID = "run/one" }},
		{name: "traversal turn ID", mutate: func(identity *ObjectIdentity) { identity.AgentTurnID = ".." }},
		{name: "unsupported evidence kind", mutate: func(identity *ObjectIdentity) { identity.Kind = EvidenceKind("arbitrary-name") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity := valid
			tt.mutate(&identity)
			if _, err := store.Put(identity, []byte("payload")); err == nil {
				t.Fatal("Put() error = nil, want identity rejection")
			}
		})
	}
}

func TestLocalFileObjectStoreRejectsSymlinkEscapeAndCleansTemporaryOnRenameFailure(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	turns := filepath.Join(root, "session-1", "run-1", "turns")
	if err := os.MkdirAll(filepath.Dir(turns), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(outside, turns); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	store := LocalFileObjectStore{Root: root}
	identity := ObjectIdentity{
		WorkerSessionID: "session-1",
		WorkerRunID:     "run-1",
		AgentTurnID:     "turn-1",
		Kind:            EvidenceTrace,
	}
	if _, err := store.Put(identity, []byte("payload")); err == nil {
		t.Fatal("Put() error = nil, want symlink rejection")
	}
	if _, err := os.Stat(filepath.Join(outside, "turn-1")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was mutated: stat error = %v", err)
	}

	cleanRoot := t.TempDir()
	store = LocalFileObjectStore{
		Root: cleanRoot,
		rename: func(_, _ string) error {
			return errors.New("injected rename failure")
		},
	}
	if _, err := store.Put(identity, []byte("payload")); err == nil {
		t.Fatal("Put() error = nil, want rename failure")
	}
	directory := filepath.Join(cleanRoot, "session-1", "run-1", "turns", "turn-1")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files remain after rename failure: %+v", entries)
	}
}
