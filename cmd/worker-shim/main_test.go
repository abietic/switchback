package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBuildInfoReportsExactWorkerStackPins(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--build-info"}, &output); err != nil {
		t.Fatalf("build info: %v", err)
	}
	var got buildInfo
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode build info: %v", err)
	}
	want := buildInfo{
		ShimVersion: shimVersion, ACPGoSDKVersion: "v0.13.5", ACPProtocol: 1,
		CodexACPVersion: "1.1.2", CodexVersion: "0.144.1", LiteLLMVersion: "1.93.0",
	}
	if got != want {
		t.Fatalf("build info = %+v, want %+v", got, want)
	}
}

func TestShimFailsClosedWithoutServerOwnedRuntimeInputs(t *testing.T) {
	if err := run(nil, &bytes.Buffer{}); err == nil {
		t.Fatal("Shim without an ACP runtime path reported success")
	}
}
