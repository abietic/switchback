package createdlayeripc_test

import (
	"context"
	ipc "github.com/abietic/switchback/internal/platform/createdlayeripc"
	"strings"
	"testing"
)

func TestPublicInvalidInputHasNoEffects(t *testing.T) {
	if _, err := ipc.NewClient([]byte("secret-canary"), "bad"); err == nil {
		t.Fatal("invalid key accepted")
	}
	key := []byte(strings.Repeat("k", 32))
	digest := "sha256:" + strings.Repeat("a", 64)
	c, err := ipc.NewClient(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Observe(nil, ipc.Binding{}); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Observe(ctx, ipc.Binding{}); err == nil {
		t.Fatal("cancelled context accepted")
	}
	if _, err = ipc.NewServer(key, digest); err != nil {
		t.Fatal(err)
	}
}
