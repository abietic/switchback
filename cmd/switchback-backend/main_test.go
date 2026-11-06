package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/abietic/switchback/internal/localproduct"
	"io"
	"reflect"
	"testing"
)

func TestBackendBootstrapBeforePGNewOrListeners(t *testing.T) {
	for _, failure := range []string{"pending", "invalid", "cancel", "timeout", "success"} {
		t.Run(failure, func(t *testing.T) {
			var calls []string
			business := map[string]int{}
			ops := backendOperations{load: func(func(string) (string, bool)) (localproduct.Config, error) {
				calls = append(calls, "config")
				return localproduct.Config{}, nil
			}, await: func(context.Context, localproduct.Config) error {
				calls = append(calls, "await")
				if failure != "success" {
					return errors.New(failure)
				}
				return nil
			}, start: func(context.Context, localproduct.Config) error {
				calls = append(calls, "start")
				for _, d := range []string{"New", "PG", "Engine", "provider", "HTTP", "gRPC"} {
					business[d]++
				}
				return nil
			}}
			err := runBackend(context.Background(), nil, &bytes.Buffer{}, ops)
			want := []string{"config", "await"}
			if failure == "success" {
				want = append(want, "start")
			}
			if !reflect.DeepEqual(calls, want) || (err == nil) != (failure == "success") {
				t.Fatalf("calls %v error %v", calls, err)
			}
			for d, n := range business {
				if failure != "success" || n != 1 {
					t.Fatalf("dependency %s %d", d, n)
				}
			}
		})
	}
}
func TestBackendQualificationBranchesBeforeBusinessConfig(t *testing.T) {
	calls := 0
	ops := backendOperations{qualify: func(context.Context, io.Writer) error { calls++; return nil }, load: func(func(string) (string, bool)) (localproduct.Config, error) {
		t.Fatal("loaded business config")
		return localproduct.Config{}, nil
	}}
	if err := runBackend(context.Background(), []string{"qualify-backend"}, &bytes.Buffer{}, ops); err != nil || calls != 1 {
		t.Fatalf("qualify %d %v", calls, err)
	}
	for _, args := range [][]string{{"qualify-backend", "extra"}, {"other"}} {
		if runBackend(context.Background(), args, &bytes.Buffer{}, ops) == nil {
			t.Fatal("accepted args")
		}
	}
}
