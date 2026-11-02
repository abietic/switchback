package main

import (
	"context"
	"testing"
)

func TestFixedInstallPhases(t *testing.T) {
	calls := 0
	call := func(context.Context) error { calls++; return nil }
	for _, phase := range []string{"preflight", "bootstrap-publication", "install"} {
		if e := run(context.Background(), []string{phase}, commands{call, call, call}); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	for _, args := range [][]string{nil, {"install", "/tmp"}, {"skip"}, {"--timeout=999"}} {
		if e := run(context.Background(), args, commands{call, call, call}); e == nil {
			t.Fatal("arbitrary inputs accepted")
		}
	}
}
