package acpclient_test

import (
	"context"
	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	sdk "github.com/coder/acp-go-sdk"
	"io"
	"os/exec"
	"testing"
)

func TestNewRequiresExplicitAuthentication(t *testing.T) {
	_, err := acpclient.New(acpclient.Options{Command: func() *exec.Cmd { return exec.Command("not-started") }, ProcessName: "fixture-acp", WorkspaceRoot: "/workspace", Trace: io.Discard})
	if err == nil || err.Error() != "ACP authentication policy is required" {
		t.Fatalf("missing authentication = %v", err)
	}
}

func TestNewRejectsIncompleteOptions(t *testing.T) {
	valid := func() acpclient.Options {
		return acpclient.Options{
			Command:     func() *exec.Cmd { t.Fatal("constructor started command"); return nil },
			ProcessName: "fixture-acp", WorkspaceRoot: "/workspace", Trace: io.Discard,
			Authentication: acpclient.Authentication{MethodID: "fixture", Request: func(string) (sdk.AuthenticateRequest, error) {
				return sdk.AuthenticateRequest{MethodId: "fixture"}, nil
			}},
		}
	}
	cases := []struct {
		name   string
		change func(*acpclient.Options)
	}{
		{"zero", func(o *acpclient.Options) { *o = acpclient.Options{} }},
		{"nil command", func(o *acpclient.Options) { o.Command = nil }},
		{"nil trace", func(o *acpclient.Options) { o.Trace = nil }},
		{"empty workspace", func(o *acpclient.Options) { o.WorkspaceRoot = "" }},
		{"empty process", func(o *acpclient.Options) { o.ProcessName = "" }},
		{"relative workspace", func(o *acpclient.Options) { o.WorkspaceRoot = "workspace" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := valid()
			tc.change(&o)
			if _, err := acpclient.New(o); err == nil {
				t.Fatal("incomplete options accepted")
			}
		})
	}
	t.Run("absolute workspace without process start", func(t *testing.T) {
		a, err := acpclient.New(valid())
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestNewRejectsInvalidAuthentication(t *testing.T) {
	for _, method := range []string{"", " fixture", "fixture ", "fixture\r", "fixture\n", "fixture\x00"} {
		t.Run(method, func(t *testing.T) {
			_, err := acpclient.New(acpclient.Options{Command: func() *exec.Cmd { return exec.Command("not-started") }, ProcessName: "fixture-acp", WorkspaceRoot: "/workspace", Trace: io.Discard, Authentication: acpclient.Authentication{MethodID: method, Request: func(string) (sdk.AuthenticateRequest, error) {
				return sdk.AuthenticateRequest{MethodId: "fixture"}, nil
			}}})
			if err == nil || err.Error() != "ACP authentication policy is required" {
				t.Fatalf("invalid authentication = %v", err)
			}
		})
	}
	t.Run("nil request", func(t *testing.T) {
		_, err := acpclient.New(acpclient.Options{Command: func() *exec.Cmd { return exec.Command("not-started") }, ProcessName: "fixture-acp", WorkspaceRoot: "/workspace", Trace: io.Discard, Authentication: acpclient.Authentication{MethodID: "fixture"}})
		if err == nil || err.Error() != "ACP authentication policy is required" {
			t.Fatalf("nil authentication request = %v", err)
		}
	})
}
