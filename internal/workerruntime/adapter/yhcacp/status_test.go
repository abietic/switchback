package yhcacp

import (
	"encoding/json"
	"testing"
)

func TestFailureSession(t *testing.T) {
	for _, test := range []struct {
		method, params, want string
	}{
		{"_session/status", `{"sessionId":"session-1","status":"error","message":"provider failed"}`, "session-1"},
		{"_session/status", `{"sessionId":"session-1","status":"completed","message":"error"}`, ""},
		{"_session/status", `{"sessionId":"session-1","status":{}}`, ""},
		{"_other/status", `{"sessionId":"session-1","status":"error"}`, ""},
	} {
		if got := FailureSession(test.method, json.RawMessage(test.params)); got != test.want {
			t.Errorf("FailureSession(%s, %s) = %q, want %q", test.method, test.params, got, test.want)
		}
	}
}
