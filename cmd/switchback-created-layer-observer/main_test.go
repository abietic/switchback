package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInvocationGateAndSafeErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"generate"}, {"serve"}, {"serve", "bad"}, {"serve", "sha256:" + strings.Repeat("a", 64), "extra"}} {
		var out bytes.Buffer
		ops := operations{true, func() ([]byte, error) { t.Fatal("key IO"); return nil, nil }, nil, nil}
		if run(context.Background(), args, &out, ops) != 2 {
			t.Fatal("accepted args")
		}
	}
	args := []string{"serve", "sha256:" + strings.Repeat("a", 64)}
	var out bytes.Buffer
	if run(context.Background(), args, &out, operations{}) != 2 {
		t.Fatal("unsupported accepted")
	}
	for _, kind := range []string{"key", "serve"} {
		ops := operations{true, func() ([]byte, error) {
			if kind == "key" {
				return nil, errors.New("secret-canary")
			}
			return []byte("secret-canary"), nil
		}, func(context.Context, []byte, string) error { return errors.New("secret-canary") }, nil}
		if run(context.Background(), args, &out, ops) != 1 || strings.Contains(out.String(), "secret-canary") {
			t.Fatal("unsafe error")
		}
	}
}
func TestSignalContextCleanExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ops := operations{true, func() ([]byte, error) { return make([]byte, 32), nil }, func(c context.Context, _ []byte, _ string) error { cancel(); <-c.Done(); return nil }, nil}
	var out bytes.Buffer
	if run(ctx, []string{"serve", "sha256:" + strings.Repeat("a", 64)}, &out, ops) != 0 {
		t.Fatal("cancel not clean")
	}
}
