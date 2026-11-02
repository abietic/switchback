package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathMapperSessionPathUsesOneRelativeNamespace(t *testing.T) {
	containerRoot := t.TempDir()
	hostRoot := t.TempDir()
	mapper := PathMapper{ContainerDataRoot: containerRoot, HostDataRoot: hostRoot}

	got, err := mapper.SessionPath("session-1", "working-copy")
	if err != nil {
		t.Fatalf("SessionPath() error = %v", err)
	}

	wantRelative := filepath.Join("sessions", "session-1", "working-copy")
	if got.ContainerPath != filepath.Join(containerRoot, wantRelative) {
		t.Fatalf("ContainerPath = %q, want %q", got.ContainerPath, filepath.Join(containerRoot, wantRelative))
	}
	if got.HostPath != filepath.Join(hostRoot, wantRelative) {
		t.Fatalf("HostPath = %q, want %q", got.HostPath, filepath.Join(hostRoot, wantRelative))
	}
}

func TestPathMapperRejectsUnsafeRootsAndElements(t *testing.T) {
	validRoot := t.TempDir()
	tests := []struct {
		name    string
		mapper  PathMapper
		session string
		elems   []string
	}{
		{
			name:    "empty session ID",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: validRoot},
			session: "",
		},
		{
			name:    "separator in session ID",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: validRoot},
			session: "session/one",
		},
		{
			name:    "dot dot session ID",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: validRoot},
			session: "..",
		},
		{
			name:    "absolute element",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: validRoot},
			session: "session-1",
			elems:   []string{filepath.Join(string(filepath.Separator), "outside")},
		},
		{
			name:    "lexical traversal element",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: validRoot},
			session: "session-1",
			elems:   []string{".."},
		},
		{
			name:    "empty container root",
			mapper:  PathMapper{HostDataRoot: validRoot},
			session: "session-1",
		},
		{
			name:    "relative host root",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: "relative-root"},
			session: "session-1",
		},
		{
			name:    "host root with lexical traversal",
			mapper:  PathMapper{ContainerDataRoot: validRoot, HostDataRoot: validRoot + "/../other"},
			session: "session-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.mapper.SessionPath(tt.session, tt.elems...); err == nil {
				t.Fatal("SessionPath() error = nil, want unsafe input rejection")
			}
		})
	}
}

func TestPathMapperRejectsExistingSymlinkEscape(t *testing.T) {
	containerRoot := t.TempDir()
	hostRoot := t.TempDir()
	outside := t.TempDir()
	sessionRoot := filepath.Join(containerRoot, "sessions", "session-1")
	if err := os.MkdirAll(sessionRoot, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(sessionRoot, "escape")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	mapper := PathMapper{ContainerDataRoot: containerRoot, HostDataRoot: hostRoot}
	_, err := mapper.SessionPath("session-1", "escape", "trace.jsonl")
	if err == nil {
		t.Fatal("SessionPath() error = nil, want symlink escape rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("SessionPath() error = %v, want symlink context", err)
	}
}

func TestPathMapperRejectsContainerRootSymlink(t *testing.T) {
	rootLink := filepath.Join(t.TempDir(), "data-root")
	if err := os.Symlink(t.TempDir(), rootLink); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	mapper := PathMapper{ContainerDataRoot: rootLink, HostDataRoot: t.TempDir()}
	if _, err := mapper.SessionPath("session-1"); err == nil {
		t.Fatal("SessionPath() error = nil, want container root symlink rejection")
	}
}
