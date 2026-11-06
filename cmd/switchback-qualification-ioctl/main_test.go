package main

import (
	"bytes"
	"context"
	"testing"
)

func TestRejectArgumentsBeforeAnyOSWork(t *testing.T) {
	for _, args := range [][]string{{"native"}, {"/tmp/target"}, {"--command", "anything"}} {
		if run(context.Background(), args, &bytes.Buffer{}) == nil {
			t.Fatal("arguments accepted")
		}
	}
}
