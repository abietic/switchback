package secretcheck

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

// ScanJSON checks original plain-data strings/bytes and the exact emitted JSON,
// including decoded keys/values and embedded JSON text. Legacy custom marshalers
// remain supported for their emitted representation only, not arbitrary hidden
// internals. Proof-producing callers must reject executable/custom evidence.
func (scanner *Scanner) ScanJSON(ctx context.Context, source string, value any) error {
	if scanner == nil || len(scanner.tokens) == 0 || safeSource(source) == "" {
		return errors.New("known-token scanner is not configured")
	}
	walk := jsonDataWalker{scanner: scanner, source: source}
	if err := walk.visit(ctx, reflect.ValueOf(value), 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode content for known-token scan")
	}
	return scanner.scanJSONText(ctx, source, encoded, 0)
}

const maxJSONScanDepth = 256

// ValidateProofJSON accepts only finite plain data before a proof caller invokes
// ScanJSON or json.Marshal. The conservative escaped-byte budget bounds marshal
// allocation; 64 levels and 65536 values bound traversal, including cycles/shared
// graphs. Unknown JSON/Text marshalers and unexported state are rejected without
// executing them. RawMessage and time.Time are the only serializer exceptions.
// No values, credential registry or authorization are retained by this helper.
func ValidateProofJSON(ctx context.Context, value any, maxBytes int) error {
	if value == nil || maxBytes <= 0 {
		return errors.New("proof input invalid")
	}
	walk := jsonDataWalker{proof: true, bytesLeft: maxBytes, nodesLeft: 1 << 16}
	return walk.visit(ctx, reflect.ValueOf(value), 0)
}

type jsonDataWalker struct {
	scanner   *Scanner
	source    string
	proof     bool
	bytesLeft int
	nodesLeft int
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	jsonIsZeroerType  = reflect.TypeFor[interface{ IsZero() bool }]()
	rawJSONType       = reflect.TypeFor[json.RawMessage]()
	jsonTimeType      = reflect.TypeFor[time.Time]()
)

func (w *jsonDataWalker) spend(bytes int) error {
	if !w.proof {
		return nil
	}
	if bytes > w.bytesLeft {
		return errors.New("proof input size exceeded")
	}
	w.bytesLeft -= bytes
	return nil
}

func (w *jsonDataWalker) visit(ctx context.Context, v reflect.Value, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.proof {
		w.nodesLeft--
		if w.nodesLeft < 0 || depth > 64 {
			return errors.New("proof input complexity exceeded")
		}
		if err := w.spend(32); err != nil {
			return err
		}
	}
	if depth > maxJSONScanDepth {
		return errors.New("known-token scan nesting exceeded")
	}
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return w.visit(ctx, v.Elem(), depth+1)
	}
	t := v.Type()
	if t.Kind() == reflect.Pointer && (t.Elem() == jsonTimeType || t.Elem() == rawJSONType) {
		if v.IsNil() {
			return nil
		}
		return w.visit(ctx, v.Elem(), depth+1)
	}
	if t == jsonTimeType {
		return w.spend(64)
	}
	if t == rawJSONType {
		raw := v.Bytes()
		if raw == nil {
			return nil
		}
		if w.proof {
			if len(raw) > w.bytesLeft/6 {
				return errors.New("proof input size exceeded")
			}
			if err := w.spend(6 * len(raw)); err != nil {
				return err
			}
			if !utf8.Valid(raw) || !json.Valid(raw) {
				return errors.New("proof input JSON invalid")
			}
			return w.checkJSON(ctx, raw, depth)
		}
		return w.scanner.scanJSONText(ctx, w.source, raw, depth+1)
	}
	if customJSONType(t) {
		if w.proof {
			return errors.New("proof input serializer unsupported")
		}
		return nil // Legacy compatibility promises only the emitted JSON here.
	}
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			return w.visit(ctx, v.Elem(), depth+1)
		}
	case reflect.String:
		if w.proof {
			if v.Len() > w.bytesLeft/6 {
				return errors.New("proof input size exceeded")
			}
			if err := w.spend(6 * v.Len()); err != nil {
				return err
			}
			if !utf8.ValidString(v.String()) {
				return errors.New("proof input text invalid")
			}
			return w.checkJSON(ctx, []byte(v.String()), depth)
		}
		return w.scanner.scanJSONText(ctx, w.source, []byte(v.String()), depth+1)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if customJSONType(v.Type().Elem()) {
				if w.proof {
					return errors.New("proof input serializer unsupported")
				}
				return nil
			}
			if w.proof {
				cost := 2
				if v.Kind() == reflect.Array {
					cost = 4
				}
				if v.Len() > w.bytesLeft/cost {
					return errors.New("proof input size exceeded")
				}
				return w.spend(cost * v.Len())
			}
			var raw []byte
			if v.Kind() == reflect.Slice {
				raw = v.Bytes()
			} else {
				raw = make([]byte, v.Len())
				for i := range raw {
					raw[i] = byte(v.Index(i).Uint())
				}
			}
			if utf8.Valid(raw) {
				return w.scanner.scanJSONText(ctx, w.source, raw, depth+1)
			}
			return w.scanner.matchJSONBytes(ctx, w.source, raw)
		}
		for i := 0; i < v.Len(); i++ {
			if err := w.visit(ctx, v.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if err := w.visit(ctx, iter.Key(), depth+1); err != nil {
				return err
			}
			if err := w.visit(ctx, iter.Value(), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.IsExported() {
				if w.proof && proofFieldExecutesIsZero(field) {
					return errors.New("proof input IsZero unsupported")
				}
				if err := w.spend(6*len(field.Name) + 6*len(field.Tag.Get("json"))); err != nil {
					return err
				}
				if err := w.visit(ctx, v.Field(i), depth+1); err != nil {
					return err
				}
			} else if w.proof {
				return errors.New("proof input hidden state unsupported")
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
	case reflect.Float32, reflect.Float64:
		if w.proof && (math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0)) {
			return errors.New("proof input number invalid")
		}
	default:
		if w.proof {
			return errors.New("proof input type unsupported")
		}
	}
	return nil
}

func customJSONType(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(textMarshalerType)
}

// proofFieldExecutesIsZero mirrors only encoding/json's omitzero callback
// selection. It does not call the method or attempt to reproduce field encoding.
// time.Time is the deliberate standard-library serializer exception accepted by
// ValidateProofJSON; an interface field remains unknown even if its current
// dynamic value happens to be a time.Time.
func proofFieldExecutesIsZero(field reflect.StructField) bool {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return false
	}
	_, options, _ := strings.Cut(tag, ",")
	if !jsonTagHasOption(options, "omitzero") {
		return false
	}
	t := field.Type
	if t == jsonTimeType || t.Kind() == reflect.Pointer && t.Elem() == jsonTimeType {
		return false
	}
	return t.Implements(jsonIsZeroerType) || reflect.PointerTo(t).Implements(jsonIsZeroerType)
}

func jsonTagHasOption(options, want string) bool {
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		if option == want {
			return true
		}
	}
	return false
}

// Count JSON tokens without a float64/object round trip. Embedded JSON text
// shares the same work budget as its containing original data value.
func (w *jsonDataWalker) checkJSON(ctx context.Context, raw []byte, depth int) error {
	if !json.Valid(raw) {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("proof input JSON invalid")
		}
		w.nodesLeft--
		if w.nodesLeft < 0 {
			return errors.New("proof input complexity exceeded")
		}
		switch value := token.(type) {
		case json.Delim:
			if value == '{' || value == '[' {
				depth++
				if depth > 64 {
					return errors.New("proof input complexity exceeded")
				}
			} else {
				depth--
			}
		case string:
			if depth >= 64 {
				return errors.New("proof input complexity exceeded")
			}
			if err := w.checkJSON(ctx, []byte(value), depth+1); err != nil {
				return err
			}
		}
	}
}

func (scanner *Scanner) matchJSONBytes(ctx context.Context, source string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, token := range scanner.tokens {
		if err := ctx.Err(); err != nil {
			return err
		}
		if bytes.Contains(data, []byte(token.Value)) {
			return &ExposureError{Source: source, TokenLabel: token.Label}
		}
	}
	return nil
}

func (scanner *Scanner) scanJSONText(ctx context.Context, source string, data []byte, depth int) error {
	if err := scanner.matchJSONBytes(ctx, source, data); err != nil {
		return err
	}
	if !utf8.Valid(data) {
		return errors.New("invalid text for known-token scan")
	}
	if depth > maxJSONScanDepth {
		return errors.New("known-token scan nesting exceeded")
	}
	if !json.Valid(data) {
		return nil
	} // Plain original text is not necessarily JSON.
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("invalid JSON for known-token scan")
		}
		switch value := token.(type) {
		case string:
			if err := scanner.scanJSONText(ctx, source, []byte(value), depth+1); err != nil {
				return err
			}
		case json.Number:
			if err := scanner.matchJSONBytes(ctx, source, []byte(value.String())); err != nil {
				return err
			}
		}
	}
}
