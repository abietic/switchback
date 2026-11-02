package main

import (
	"context"
	"testing"
)

func TestFixedNativePhases(t *testing.T) {
	var called []string
	makePhase := func(name string) func(context.Context) error {
		return func(context.Context) error { called = append(called, name); return nil }
	}
	c := commands{makePhase("intent"), makePhase("begin"), makePhase("prepare"), makePhase("collect"), makePhase("assemble")}
	for _, name := range []string{"intent", "begin", "prepare", "collect", "assemble"} {
		if err := run(context.Background(), []string{name}, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{nil, {"collect", "--skip"}, {"--root=/tmp/other"}, {"retry"}} {
		if err := run(context.Background(), args, c); err == nil {
			t.Fatal("invalid native phase accepted")
		}
	}
	if len(called) != 5 {
		t.Fatal("invalid command invoked adapter")
	}
}
