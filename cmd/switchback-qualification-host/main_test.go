package main

import (
	"context"
	"testing"
)

func TestFixedHostCommand(t *testing.T) {
	called := 0
	f := func(context.Context) error { called++; return nil }
	if err := run(context.Background(), nil, f); err != nil || called != 1 {
		t.Fatal("fixed command not dispatched")
	}
	for _, args := range [][]string{{"collect"}, {"--root=/tmp/other"}, {"collect", "--skip"}} {
		if err := run(context.Background(), args, f); err == nil {
			t.Fatal("extra host argument accepted")
		}
	}
	if called != 1 {
		t.Fatal("invalid command invoked adapter")
	}
}
