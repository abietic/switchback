package main

import (
	"context"
	"testing"
)

func TestMaterialRejectsExtraInput(t *testing.T) {
	calls := 0
	call := func(context.Context, string) error { calls++; return nil }
	if e := run(context.Background(), []string{"intent-input"}, call); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{nil, {"intent-input", "/tmp/input"}} {
		if e := run(context.Background(), args, call); e == nil {
			t.Fatal("arbitrary input accepted")
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
