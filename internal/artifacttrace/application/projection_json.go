package application

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

const maxProjectionJSONExponent = 10000

// ProjectionJSONEqual compares JSON structurally while preserving decimal number
// precision. Object key order is ignored; array order remains significant.
func ProjectionJSONEqual(left, right json.RawMessage) bool {
	a, ok := decodeProjectionJSON(left)
	if !ok {
		return false
	}
	b, ok := decodeProjectionJSON(right)
	return ok && projectionJSONValueEqual(a, b)
}

func decodeProjectionJSON(data []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var trailing any
	return value, decoder.Decode(&trailing) == io.EOF
}

func projectionJSONValueEqual(left, right any) bool {
	switch a := left.(type) {
	case nil:
		return right == nil
	case bool:
		b, ok := right.(bool)
		return ok && a == b
	case string:
		b, ok := right.(string)
		return ok && a == b
	case json.Number:
		b, ok := right.(json.Number)
		if !ok {
			return false
		}
		as, ac, ae, aok := normalizeJSONNumber(string(a))
		bs, bc, be, bok := normalizeJSONNumber(string(b))
		return aok && bok && as == bs && ac == bc && ae == be
	case []any:
		b, ok := right.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i := range a {
			if !projectionJSONValueEqual(a[i], b[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		b, ok := right.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for key, av := range a {
			bv, exists := b[key]
			if !exists || !projectionJSONValueEqual(av, bv) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func normalizeJSONNumber(raw string) (negative bool, coefficient string, exponent int, ok bool) {
	if strings.HasPrefix(raw, "-") {
		negative, raw = true, raw[1:]
	}
	eIndex := strings.IndexAny(raw, "eE")
	exponentText := ""
	if eIndex >= 0 {
		exponentText, raw = raw[eIndex+1:], raw[:eIndex]
	}
	if exponentText != "" {
		parsed, err := strconv.ParseInt(exponentText, 10, 32)
		if err != nil || parsed < -maxProjectionJSONExponent || parsed > maxProjectionJSONExponent {
			return false, "", 0, false
		}
		exponent = int(parsed)
	}
	if dot := strings.IndexByte(raw, '.'); dot >= 0 {
		exponent -= len(raw) - dot - 1
		raw = raw[:dot] + raw[dot+1:]
	}
	raw = strings.TrimLeft(raw, "0")
	if raw == "" {
		return false, "0", 0, true
	}
	trimmed := strings.TrimRight(raw, "0")
	exponent += len(raw) - len(trimmed)
	return negative, trimmed, exponent, true
}
