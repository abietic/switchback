package secretcheck_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/secretcheck"
)

func TestScannerJSONPreservesRawAndSemanticTokenValues(t *testing.T) {
	for name, token := range map[string]string{"quote": `key-"quote`, "slash": `key-\slash`, "control": "key-\n\x00end", "html": "key-<>&", "separators": "key-\u2028\u2029", "unicode": "key-中文", "invalid-utf8": "key-\xff"} {
		t.Run(name, func(t *testing.T) {
			s, err := secretcheck.New([]secretcheck.KnownToken{{Label: "synthetic", Value: token}})
			if err != nil {
				t.Fatal(err)
			}
			if !secretcheck.IsExposure(s.ScanBytes(context.Background(), "raw", []byte(token))) {
				t.Fatal("raw control did not detect")
			}
			type namedBytes []byte
			for _, value := range []any{token, map[string]any{"payload": token}, map[string]any{token: "clean"}, struct{ Payload namedBytes }{namedBytes(token)}} {
				err := s.ScanJSON(context.Background(), "json", value)
				if !secretcheck.IsExposure(err) {
					t.Fatalf("original token was not detected: %T error=%v", value, err)
				}
				if strings.Contains(fmt.Sprintf("%v", err), token) {
					t.Fatal("diagnostic leaked token")
				}
			}
		})
	}
}

func TestScannerJSONDecodesKeysValuesAndEmbeddedDocuments(t *testing.T) {
	s, _ := secretcheck.New([]secretcheck.KnownToken{{Label: "synthetic", Value: "key-\"<>&\\\n"}})
	for _, raw := range []string{`{"key-\u0022\u003c\u003e\u0026\u005c\u000a":"clean"}`, `{"payload":"key-\u0022\u003c\u003e\u0026\u005c\u000a"}`} {
		for _, value := range []any{json.RawMessage(raw), map[string]any{"json": raw}} {
			if err := s.ScanJSON(context.Background(), "json", value); !secretcheck.IsExposure(err) {
				t.Fatalf("escaped token not detected: %v", err)
			}
		}
	}
	numbers, _ := secretcheck.New([]secretcheck.KnownToken{{Label: "number", Value: "9007199254740993"}})
	if err := numbers.ScanJSON(context.Background(), "json", json.RawMessage(`{"n":9007199254740993}`)); !secretcheck.IsExposure(err) {
		t.Fatal("number lexeme was lost", err)
	}
}

func TestScannerJSONKeepsLegacySerializersAndChecksContext(t *testing.T) {
	s := newScanner(t)
	for _, value := range []any{time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), legacyCleanSerializer{}, map[string]any{"bytes": []byte("clean")}} {
		if err := s.ScanJSON(context.Background(), "json", value); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ScanJSON(context.Background(), "json", legacySecretSerializer{}); !secretcheck.IsExposure(err) {
		t.Fatal("custom emitted JSON missed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.ScanJSON(ctx, "json", map[string]any{"payload": "clean"}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

type proofValueZero struct{ Calls *int }

func (v proofValueZero) IsZero() bool {
	*v.Calls++
	return false
}

type proofPointerZero struct{ Calls *int }

func (v *proofPointerZero) IsZero() bool {
	*v.Calls++
	return false
}

type proofZeroer interface {
	IsZero() bool
}

func TestValidateProofJSONRejectsOmitZeroHooksBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value func(*int) any
	}{
		{"value receiver", func(calls *int) any {
			return struct {
				Data proofValueZero `json:"data,omitzero"`
			}{Data: proofValueZero{Calls: calls}}
		}},
		{"pointer field", func(calls *int) any {
			return struct {
				Data *proofPointerZero `json:"data,omitzero"`
			}{Data: &proofPointerZero{Calls: calls}}
		}},
		{"pointer receiver on non-addressable value", func(calls *int) any {
			return struct {
				Data proofPointerZero `json:"data,omitzero"`
			}{Data: proofPointerZero{Calls: calls}}
		}},
		{"interface field", func(calls *int) any {
			return struct {
				Data proofZeroer `json:"data,omitzero"`
			}{Data: proofValueZero{Calls: calls}}
		}},
		{"nested field", func(calls *int) any {
			return struct {
				Nested struct {
					Data proofValueZero `json:"data,omitzero"`
				} `json:"nested"`
			}{Nested: struct {
				Data proofValueZero `json:"data,omitzero"`
			}{Data: proofValueZero{Calls: calls}}}
		}},
		{"dash name with option", func(calls *int) any {
			return struct {
				Data proofValueZero `json:"-,omitzero"`
			}{Data: proofValueZero{Calls: calls}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			if err := secretcheck.ValidateProofJSON(context.Background(), tc.value(&calls), 1<<20); err == nil {
				t.Fatal("executable omitzero evidence accepted")
			}
			if calls != 0 {
				t.Fatalf("IsZero executed %d times", calls)
			}
		})
	}
}

func TestValidateProofJSONAllowsNonExecutableZeroMethods(t *testing.T) {
	calls := 0
	value := struct {
		Ordinary proofValueZero  `json:"ordinary"`
		Ignored  proofValueZero  `json:"-"`
		Dynamic  any             `json:"dynamic,omitzero"`
		At       time.Time       `json:"at,omitzero"`
		AtPtr    *time.Time      `json:"at_ptr,omitzero"`
		Raw      json.RawMessage `json:"raw,omitzero"`
	}{
		Ordinary: proofValueZero{Calls: &calls},
		Ignored:  proofValueZero{Calls: &calls},
		Dynamic:  proofValueZero{Calls: &calls},
		At:       time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC),
		Raw:      json.RawMessage(`{"safe":true}`),
	}
	value.AtPtr = &value.At
	if err := secretcheck.ValidateProofJSON(context.Background(), value, 1<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(value); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("irrelevant IsZero executed %d times", calls)
	}
}

type legacyCleanSerializer struct{}

func (legacyCleanSerializer) MarshalJSON() ([]byte, error) { return []byte(`{"safe":true}`), nil }

type legacySecretSerializer struct{}

func (legacySecretSerializer) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"payload": knownValue})
}
