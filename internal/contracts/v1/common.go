// Package contractsv1 defines the versioned published language shared by Switchback
// bounded contexts. These types carry contracts, not aggregate behavior.
package contractsv1

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type TaskID string
type WorkerSessionID string
type WorkerRunID string
type AgentSessionID string
type AgentTurnID string
type RuntimeInstanceID string

func decodeStrict[T any](data []byte, validate func(T) error) (T, error) {
	var result T
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode contract: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return result, fmt.Errorf("decode contract: multiple JSON values")
		}
		return result, fmt.Errorf("decode contract trailing content: %w", err)
	}
	if err := validate(result); err != nil {
		return result, err
	}
	return result, nil
}

func requireString(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be a nonempty canonical string", name)
	}
	return nil
}

func requireID(name, value string) error {
	text := value
	if err := requireString(name, text); err != nil {
		return err
	}
	if len(text) > 160 || strings.ContainsAny(text, " \t/\\") {
		return fmt.Errorf("%s contains invalid identity characters", name)
	}
	return nil
}

func requireSHA256(name, value string, prefixed bool) error {
	digest := value
	if prefixed {
		var ok bool
		digest, ok = strings.CutPrefix(value, "sha256:")
		if !ok {
			return fmt.Errorf("%s must use sha256 prefix", name)
		}
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
		return fmt.Errorf("%s must contain one SHA-256 digest", name)
	}
	return nil
}

func requireJSONObject(name string, value json.RawMessage) error {
	if !json.Valid(value) || len(bytes.TrimSpace(value)) < 2 || bytes.TrimSpace(value)[0] != '{' {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	return nil
}

func requireStrings(name string, values []string, nonempty bool) error {
	if values == nil || nonempty && len(values) == 0 {
		return fmt.Errorf("%s must be an explicit array", name)
	}
	for index, value := range values {
		if err := requireString(fmt.Sprintf("%s[%d]", name, index), value); err != nil {
			return err
		}
	}
	return nil
}
