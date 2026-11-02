package acpclient_test

import (
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreCallbacksConfineFilesAndRejectTerminalAuthority(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	for _, op := range []func() error{func() error { return os.WriteFile(secret, []byte("outside-secret"), 0600) }, func() error { return os.Symlink(outside, filepath.Join(root, "ancestor-link")) }, func() error { return os.Symlink(secret, filepath.Join(root, "final-link")) }, func() error { return os.Mkdir(filepath.Join(root, "directory"), 0700) }, func() error { return unix.Mkfifo(filepath.Join(root, "fifo"), 0600) }, func() error { return os.WriteFile(filepath.Join(root, "outside-path"), []byte(secret), 0600) }} {
		if err := op(); err != nil {
			t.Fatal(err)
		}
	}
	a, ctx := newPeerAdapter(t, root, "callbacks", io.Discard)
	openPeer(t, a, ctx, root)
	u := promptPeer(t, a, ctx)
	if len(u) != 2 || !strings.Contains(string(u[0].Payload), "callbacks-ok") {
		t.Fatalf("peer callback assertions: %+v", u)
	}
	for path, want := range map[string]string{secret: "outside-secret", filepath.Join(root, "notes.txt"): "line one\nline two\nline three"} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != want {
			t.Fatalf("file %s = %q %v", path, b, err)
		}
	}
}
