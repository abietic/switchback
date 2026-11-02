package acpclient_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type peerMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func TestCorePeerProcess(t *testing.T) {
	if os.Getenv("SWITCHBACK_CORE_PEER") != "1" {
		return
	}
	defer os.Exit(0)
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	scenario := os.Getenv("SWITCHBACK_CORE_SCENARIO")
	evidence, err := os.OpenFile(filepath.Join(os.Getenv("SWITCHBACK_CORE_ROOT"), "peer-evidence"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(2)
	}
	defer evidence.Close()
	tokenOK := false
	for {
		var m peerMessage
		if decoder.Decode(&m) != nil {
			return
		}
		if _, err := evidence.WriteString(m.Method + "\n"); err != nil {
			os.Exit(2)
		}
		switch m.Method {
		case "initialize":
			var buf bytes.Buffer
			version := 1
			auth := []any{map[string]any{"id": "fixture-auth", "name": "Fixture"}}
			if scenario == "protocol-two" {
				version = 2
			}
			if scenario == "no-auth" {
				auth = nil
			}
			json.NewEncoder(&buf).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": version, "agentCapabilities": map[string]any{"loadSession": true}, "authMethods": auth}})
			if err := os.WriteFile(filepath.Join(os.Getenv("SWITCHBACK_CORE_ROOT"), "initialize-frame"), buf.Bytes(), 0600); err != nil {
				os.Exit(2)
			}
			if os.Getenv("SWITCHBACK_CORE_SCENARIO") == "frames" {
				p := buf.Bytes()
				os.Stdout.Write(p[:9])
				os.Stdout.Write(p[9:21])
				os.Stdout.Write(p[21:])
			} else {
				os.Stdout.Write(buf.Bytes())
			}
		case "authenticate":
			var params struct {
				Method string `json:"methodId"`
				Meta   struct {
					Token string `json:"token"`
				} `json:"_meta"`
			}
			json.Unmarshal(m.Params, &params)
			tokenOK = params.Method == "fixture-auth" && params.Meta.Token == "fixture-proxy-token"
			encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "session/new":
			encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"sessionId": "fixture-session"}})
		case "session/prompt":
			if os.Getenv("SWITCHBACK_CORE_SCENARIO") == "frames" {
				p := unknownFrame + knownFrame
				os.Stdout.Write([]byte(p[:11]))
				os.Stdout.Write([]byte(p[11:]))
			} else {
				text := "peer-ok"
				if tokenOK {
					text = "token-ok"
				}
				if scenario == "callbacks" {
					text = peerCallbacks(decoder, encoder)
				}
				update := map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}
				if scenario == "system-error" {
					update = map[string]any{"sessionUpdate": "session_info_update", "_meta": map[string]any{"codex": map[string]any{"threadStatus": map[string]any{"type": "systemError"}}}}
				}
				encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture-session", "update": update}})
			}
			encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
		}
	}
}

func peerCallbacks(decoder *json.Decoder, encoder *json.Encoder) string {
	root := os.Getenv("SWITCHBACK_CORE_ROOT")
	sequence := 0
	call := func(method string, params map[string]any) (peerMessage, bool) {
		sequence++
		id := json.RawMessage([]byte(`"callback-` + fmt.Sprint(sequence) + `"`))
		params["sessionId"] = "fixture-session"
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}) != nil {
			os.Exit(3)
		}
		var response peerMessage
		if decoder.Decode(&response) != nil || !bytes.Equal(response.ID, id) {
			os.Exit(3)
		}
		if len(response.Error) == 0 && len(response.Result) == 0 {
			os.Exit(3)
		}
		return response, len(response.Error) == 0
	}
	notes := filepath.Join(root, "notes.txt")
	if _, ok := call("fs/write_text_file", map[string]any{"path": notes, "content": "line one\nline two\nline three"}); !ok {
		return "write-failed"
	}
	response, ok := call("fs/read_text_file", map[string]any{"path": notes, "line": 2, "limit": 1})
	var read struct {
		Content string `json:"content"`
	}
	json.Unmarshal(response.Result, &read)
	if !ok || read.Content != "line two" {
		return "read-failed"
	}
	outside, err := os.ReadFile(filepath.Join(root, "outside-path"))
	if err != nil {
		return "fixture-failed"
	}
	for _, path := range []string{string(outside), filepath.Join(root, "ancestor-link", "secret.txt"), filepath.Join(root, "final-link"), filepath.Join(root, "directory"), filepath.Join(root, "fifo")} {
		for _, method := range []string{"fs/read_text_file", "fs/write_text_file"} {
			if _, ok := call(method, map[string]any{"path": path, "content": "overwrite"}); ok {
				return "unsafe-file-accepted"
			}
		}
	}
	response, ok = call("session/request_permission", map[string]any{"toolCall": map[string]any{"toolCallId": "fixture-tool", "title": "unsafe"}, "options": []any{map[string]any{"optionId": "allow", "name": "Allow", "kind": "allow_once"}}})
	var permission struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	json.Unmarshal(response.Result, &permission)
	if !ok || permission.Outcome.Outcome != "cancelled" || permission.Outcome.OptionID != "" {
		return "permission-accepted"
	}
	for _, method := range []string{"terminal/create", "terminal/kill", "terminal/output", "terminal/release", "terminal/wait_for_exit"} {
		if _, ok := call(method, map[string]any{"terminalId": "fixture-terminal", "command": "true"}); ok {
			return "terminal-accepted"
		}
	}
	return "callbacks-ok"
}
