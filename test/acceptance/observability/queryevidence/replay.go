package queryevidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
)

// Replay is a local, read-only transport with no upstream or network fallback.
type Replay struct {
	mu        sync.Mutex
	directory string
	binding   Binding
	records   []responseRecord
	consumed  map[int]bool
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid query evidence file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("query evidence file unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() > limit {
		return nil, errors.New("query evidence file replaced")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit || int64(len(raw)) != opened.Size() {
		return nil, errors.New("query evidence read failed")
	}
	return raw, nil
}

func strictJSON(raw []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dest) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid query evidence JSON")
	}
	return nil
}

// OpenReplay verifies all bytes, including pending history, against expected binding.
// The local fixture model does not defend against concurrent hostile host changes.
func OpenReplay(directory string, binding Binding) (*Replay, error) {
	if !validBinding(binding) || !canonicalDirectory(directory, false) {
		return nil, errors.New("invalid replay binding or directory")
	}
	raw, err := readBounded(filepath.Join(directory, "manifest.json"), 16<<20)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := strictJSON(raw, &m); err != nil {
		return nil, err
	}
	if m.Schema != "observability.query_manifest.v1" || !reflect.DeepEqual(binding, m.Binding) || len(m.Responses) == 0 || len(m.Responses) > 4096 || m.SuccessfulAttempt < 1 {
		return nil, errors.New("query manifest mismatch")
	}
	origins := make(map[string]string, 3)
	for name, origin := range binding.Origins {
		origins[name] = origin
	}
	binding.Origins = origins
	replay := &Replay{directory: directory, binding: binding, consumed: map[int]bool{}}
	expected := map[string]bool{"manifest.json": true}
	attempt := 0
	var total int64
	for i, file := range m.Responses {
		sequence := i + 1
		name := fmt.Sprintf("%06d", sequence)
		if file.Sequence != sequence || file.Name != name+".json" || !digestPattern.MatchString(file.SHA256) {
			return nil, errors.New("query metadata index mismatch")
		}
		// 4096-byte URLs may expand sixfold when encoded as JSON strings.
		meta, err := readBounded(filepath.Join(directory, file.Name), 32<<10)
		if err != nil {
			return nil, err
		}
		if checksum(meta) != file.SHA256 {
			return nil, errors.New("query metadata hash mismatch")
		}
		var record responseRecord
		if err := strictJSON(meta, &record); err != nil {
			return nil, err
		}
		request, err := http.NewRequest(record.Method, record.URL, nil)
		if err != nil || record.Method != http.MethodGet || record.URL != request.URL.String() || !validRequest(request, binding) {
			return nil, errors.New("query response request mismatch")
		}
		if record.Schema != "observability.query_response.v1" || record.Sequence != sequence || record.Attempt < 1 || record.Attempt < attempt || record.Attempt > attempt+1 {
			return nil, errors.New("query response sequence mismatch")
		}
		if record.Status < 100 || record.Status > 599 || record.StartedAt.IsZero() || record.FinishedAt.IsZero() || record.FinishedAt.Before(record.StartedAt) || record.BodyBytes < 0 || record.BodyBytes > 4<<20 || !digestPattern.MatchString(record.BodySHA256) || record.Failure != "" {
			return nil, errors.New("query response metadata mismatch")
		}
		body, err := readBounded(filepath.Join(directory, name+".body"), 4<<20)
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != record.BodyBytes || checksum(body) != record.BodySHA256 {
			return nil, errors.New("query response body mismatch")
		}
		total += int64(len(body))
		if total > 64<<20 {
			return nil, errors.New("query evidence total bytes exceeded")
		}
		expected[file.Name] = true
		expected[name+".body"] = true
		attempt = record.Attempt
		if record.Attempt == m.SuccessfulAttempt {
			if record.Status != http.StatusOK {
				return nil, errors.New("successful attempt contains non-200 response")
			}
			replay.records = append(replay.records, record)
		}
	}
	if attempt != m.SuccessfulAttempt || len(replay.records) == 0 {
		return nil, errors.New("query successful attempt mismatch")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != len(expected) {
		return nil, errors.New("query evidence file list mismatch")
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			return nil, errors.New("unexpected query evidence file")
		}
	}
	return replay, nil
}

func (r *Replay) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !validRequest(request, r.binding) {
		return nil, errors.New("replay query rejected")
	}
	if err := request.Context().Err(); err != nil {
		return nil, errors.New("replay query canceled")
	}
	// Search across URLs, but consume repeated URLs in their recorded FIFO order.
	for _, record := range r.records {
		if !r.consumed[record.Sequence] && record.Method == request.Method && record.URL == request.URL.String() {
			raw, err := readBounded(filepath.Join(r.directory, fmt.Sprintf("%06d.body", record.Sequence)), 4<<20)
			if err != nil {
				return nil, err
			}
			if int64(len(raw)) != record.BodyBytes || checksum(raw) != record.BodySHA256 {
				return nil, errors.New("replay response body replaced")
			}
			if err := request.Context().Err(); err != nil {
				return nil, errors.New("replay query canceled")
			}
			r.consumed[record.Sequence] = true
			return &http.Response{StatusCode: record.Status, Status: fmt.Sprintf("%d %s", record.Status, http.StatusText(record.Status)), Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw)), Request: request}, nil
		}
	}
	return nil, errors.New("unrecorded query")
}

func (r *Replay) AssertConsumed() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.consumed) != len(r.records) {
		return errors.New("unconsumed queries")
	}
	return nil
}
