package acpclient

import (
	"encoding/json"
	"testing"
)

func TestExtensionFailureRequiresActiveMatchingSession(t *testing.T) {
	adapter := &Adapter{
		options: Options{ExtensionFailureSession: func(method string, params json.RawMessage) string {
			if method != "_fixture/failure" {
				return ""
			}
			var session string
			if json.Unmarshal(params, &session) != nil {
				return ""
			}
			return session
		}},
		agentSessionID: "current-session",
	}
	const method = "_fixture/failure"
	matching := json.RawMessage(`"current-session"`)
	if adapter.observeExtension(method, matching) || adapter.extensionFailure {
		t.Fatal("a failure without an active turn changed the adapter")
	}
	adapter.activeTurnID = "current-turn"
	for _, params := range []json.RawMessage{json.RawMessage(`"old-session"`), json.RawMessage(`{}`)} {
		if adapter.observeExtension(method, params) || adapter.extensionFailure {
			t.Fatal("a different session or malformed notification changed the active turn")
		}
	}
	if adapter.observeExtension("_unrelated/status", matching) || adapter.extensionFailure {
		t.Fatal("an unrelated extension changed the active turn")
	}
	if !adapter.observeExtension(method, matching) || !adapter.extensionFailure {
		t.Fatal("the active session's error did not fence a successful terminal")
	}
}
