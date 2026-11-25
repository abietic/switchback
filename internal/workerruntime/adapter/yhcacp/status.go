package yhcacp

import "encoding/json"

// FailureSession identifies a YHC runtime error without interpreting its
// diagnostic message or any assistant content.
func FailureSession(method string, params json.RawMessage) string {
	if method != "_session/status" {
		return ""
	}
	var status struct {
		SessionID string `json:"sessionId"`
		Status    string `json:"status"`
	}
	if json.Unmarshal(params, &status) != nil || status.Status != "error" {
		return ""
	}
	return status.SessionID
}
