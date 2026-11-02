// Package secretcheck verifies that exact runtime-known credentials are absent
// from persisted and externally projected content.
package secretcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const scanBufferSize = 32 << 10

type KnownToken struct {
	Label string
	Value string
}

type ExposureError struct {
	Source     string
	TokenLabel string
}

func (err *ExposureError) Error() string {
	return fmt.Sprintf("known token %s was found in %s", err.TokenLabel, err.Source)
}

func IsExposure(err error) bool {
	var exposure *ExposureError
	return errors.As(err, &exposure)
}

type ObjectReader interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

type Scanner struct {
	tokens   []KnownToken
	maxBytes int
}

func (Scanner) String() string   { return "[REDACTED]" }
func (Scanner) GoString() string { return "[REDACTED]" }

func New(tokens []KnownToken) (*Scanner, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("known token registry is required")
	}
	labels := make(map[string]struct{}, len(tokens))
	values := make(map[string]struct{}, len(tokens))
	registry := make([]KnownToken, len(tokens))
	maxBytes := 0
	for index, token := range tokens {
		if !canonicalLabel(token.Label) || token.Value == "" {
			return nil, fmt.Errorf("known token registry entry is invalid")
		}
		if _, duplicate := labels[token.Label]; duplicate {
			return nil, fmt.Errorf("known token label is duplicated")
		}
		if _, duplicate := values[token.Value]; duplicate {
			return nil, fmt.Errorf("known token value is duplicated")
		}
		labels[token.Label] = struct{}{}
		values[token.Value] = struct{}{}
		registry[index] = token
		maxBytes = max(maxBytes, len(token.Value))
	}
	return &Scanner{tokens: registry, maxBytes: maxBytes}, nil
}

func (scanner *Scanner) ScanBytes(ctx context.Context, source string, content []byte) error {
	return scanner.ScanReader(ctx, source, bytes.NewReader(content))
}

func (scanner *Scanner) ScanObject(ctx context.Context, store ObjectReader, key string) error {
	if store == nil || key == "" {
		return fmt.Errorf("ObjectStore known-token scan target is required")
	}
	reader, err := store.Open(ctx, key)
	if err != nil {
		return fmt.Errorf("open ObjectStore content for known-token scan")
	}
	defer reader.Close()
	return scanner.ScanReader(ctx, "objectstore:"+key, reader)
}

func (scanner *Scanner) ScanReader(ctx context.Context, source string, reader io.Reader) error {
	if scanner == nil || len(scanner.tokens) == 0 || reader == nil {
		return fmt.Errorf("known-token scanner is not configured")
	}
	source = safeSource(source)
	if source == "" {
		return fmt.Errorf("known-token scan source is required")
	}
	buffer := make([]byte, scanBufferSize)
	overlap := make([]byte, 0, scanner.maxBytes-1)
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := reader.Read(buffer)
		if read > 0 {
			emptyReads = 0
			window := make([]byte, 0, len(overlap)+read)
			window = append(window, overlap...)
			window = append(window, buffer[:read]...)
			for _, token := range scanner.tokens {
				if bytes.Contains(window, []byte(token.Value)) {
					return &ExposureError{Source: source, TokenLabel: token.Label}
				}
			}
			keep := min(scanner.maxBytes-1, len(window))
			overlap = append(overlap[:0], window[len(window)-keep:]...)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return io.ErrNoProgress
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read %s for known-token scan", source)
		}
	}
}

func canonicalLabel(label string) bool {
	if label == "" || len(label) > 64 || strings.TrimSpace(label) != label {
		return false
	}
	for _, character := range label {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				if character != '-' && character != '_' && character != '.' {
					return false
				}
			}
		}
	}
	return true
}

func safeSource(source string) string {
	if source == "" || len(source) > 512 || strings.TrimSpace(source) != source || strings.ContainsAny(source, "\r\n\x00") {
		return ""
	}
	return source
}
