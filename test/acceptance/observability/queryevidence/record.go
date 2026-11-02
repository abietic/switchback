package queryevidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Binding ties private query bytes to one claim and its controlled backend origins.
type Binding struct {
	Project        string            `json:"project"`
	SnapshotSHA256 string            `json:"snapshot_sha256"`
	Origins        map[string]string `json:"origins"`
}

type responseRecord struct {
	Schema     string    `json:"schema"`
	Sequence   int       `json:"sequence"`
	Attempt    int       `json:"attempt"`
	Method     string    `json:"method"`
	URL        string    `json:"url"`
	Status     int       `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	BodyBytes  int64     `json:"body_bytes"`
	BodySHA256 string    `json:"body_sha256"`
	Failure    string    `json:"failure,omitempty"`
}

type metadataFile struct {
	Sequence int    `json:"sequence"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
}

type manifest struct {
	Schema            string         `json:"schema"`
	Binding           Binding        `json:"binding"`
	SuccessfulAttempt int            `json:"successful_attempt"`
	Responses         []metadataFile `json:"responses"`
}

// Recorder serializes attempts and retains only bounded metadata in memory.
// A manifest means the caller sealed a complete final HTTP attempt, not G3 PASS.
type Recorder struct {
	mu           sync.Mutex
	directory    string
	binding      Binding
	upstream     http.RoundTripper
	attempt      int
	attemptCount int
	attemptOK    bool
	sealed       bool
	failed       bool
	totalBytes   int64
	files        []metadataFile
}

func checksum(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

var projectPattern = regexp.MustCompile(`^[a-z0-9-]{1,128}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validBinding(binding Binding) bool {
	if !projectPattern.MatchString(binding.Project) || !digestPattern.MatchString(binding.SnapshotSHA256) || len(binding.Origins) != 3 {
		return false
	}
	seen := map[string]bool{}
	for _, name := range []string{"grafana", "langfuse", "prometheus"} {
		raw := binding.Origins[name]
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || seen[raw] {
			return false
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			return false
		}
		ip := net.ParseIP(host)
		p, err := strconv.Atoi(port)
		if ip == nil || !ip.IsLoopback() || err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port || u.String() != raw {
			return false
		}
		seen[raw] = true
	}
	return true
}

func canonicalDirectory(directory string, parentOnly bool) bool {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return false
	}
	target := directory
	if parentOnly {
		target = filepath.Dir(directory)
	}
	canonical, err := filepath.EvalSymlinks(target)
	if err != nil || canonical != target {
		return false
	}
	info, err := os.Lstat(target)
	return err == nil && info.IsDir()
}

func validRequest(request *http.Request, binding Binding) bool {
	if request == nil || request.URL == nil || request.Method != http.MethodGet || request.Body != nil && request.Body != http.NoBody || request.ContentLength != 0 || request.Response != nil || request.Header.Get("Host") != "" {
		return false
	}
	u := request.URL
	if len(u.String()) > 4096 || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.RawPath != "" || request.Host != "" && request.Host != u.Host {
		return false
	}
	origin := u.Scheme + "://" + u.Host
	switch origin {
	case binding.Origins["grafana"]:
		return u.Path == "/api/datasources/proxy/uid/switchback-loki/loki/api/v1/series" || u.Path == "/api/datasources/proxy/uid/switchback-loki/loki/api/v1/query_range"
	case binding.Origins["prometheus"]:
		return u.Path == "/api/v1/query"
	case binding.Origins["langfuse"]:
		for _, prefix := range []string{"/api/public/traces/", "/api/public/observations/"} {
			if strings.HasPrefix(u.Path, prefix) {
				id := strings.TrimPrefix(u.Path, prefix)
				return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\")
			}
		}
	}
	return false
}

func writeExclusive(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// NewRecorder creates a new private directory under a canonical existing parent.
func NewRecorder(directory string, binding Binding, upstream http.RoundTripper) (*Recorder, error) {
	if !validBinding(binding) || !canonicalDirectory(directory, true) {
		return nil, errors.New("invalid query evidence binding or directory")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, err
	}
	if upstream == nil {
		upstream = http.DefaultTransport
	}
	origins := make(map[string]string, 3)
	for name, origin := range binding.Origins {
		origins[name] = origin
	}
	binding.Origins = origins
	return &Recorder{directory: directory, binding: binding, upstream: upstream}, nil
}

// BeginAttempt starts the next poll iteration; empty iterations cannot be skipped.
func (r *Recorder) BeginAttempt() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed || r.failed || r.attempt > 0 && r.attemptCount == 0 {
		return errors.New("invalid attempt transition")
	}
	r.attempt++
	r.attemptCount = 0
	r.attemptOK = true
	return nil
}

func (r *Recorder) RoundTrip(request *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed || r.failed || r.attempt == 0 {
		return nil, errors.New("recorder is not in an attempt")
	}
	if !validRequest(request, r.binding) {
		r.failed = true
		return nil, errors.New("query request rejected")
	}
	if len(r.files) >= 4096 {
		r.failed = true
		return nil, errors.New("query record limit exceeded")
	}
	started := time.Now().UTC()
	res, transportErr := r.upstream.RoundTrip(request)
	failure := ""
	status := 0
	var raw []byte
	if transportErr != nil {
		failure = "transport_error"
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	} else if res == nil || res.Body == nil {
		failure = "transport_error"
	} else {
		status = res.StatusCode
		limit := int64(4 << 20)
		if remaining := int64(64<<20) - r.totalBytes; remaining < limit {
			limit = remaining
		}
		var readErr error
		raw, readErr = io.ReadAll(io.LimitReader(res.Body, limit+1))
		closeErr := res.Body.Close()
		if int64(len(raw)) > limit {
			raw = raw[:limit]
			failure = "body_oversize"
			if limit < 4<<20 {
				failure = "total_bytes_exceeded"
			}
		} else if readErr != nil || closeErr != nil {
			failure = "body_read_error"
		} else if status < 100 || status > 599 {
			failure = "invalid_status"
		}
	}
	sequence := len(r.files) + 1
	record := responseRecord{Schema: "observability.query_response.v1", Sequence: sequence, Attempt: r.attempt, Method: request.Method, URL: request.URL.String(), Status: status, StartedAt: started, FinishedAt: time.Now().UTC(), BodyBytes: int64(len(raw)), BodySHA256: checksum(raw), Failure: failure}
	name := fmt.Sprintf("%06d", sequence)
	if err := writeExclusive(filepath.Join(r.directory, name+".body"), raw); err != nil {
		r.failed = true
		return nil, errors.New("query body persistence failed")
	}
	meta, err := json.Marshal(record)
	if err != nil {
		r.failed = true
		return nil, errors.New("query metadata encoding failed")
	}
	if err := writeExclusive(filepath.Join(r.directory, name+".json"), meta); err != nil {
		r.failed = true
		return nil, errors.New("query metadata persistence failed")
	}
	r.files = append(r.files, metadataFile{Sequence: sequence, Name: name + ".json", SHA256: checksum(meta)})
	r.totalBytes += int64(len(raw))
	r.attemptCount++
	if failure != "" {
		r.failed = true
		return nil, errors.New("query response failed: " + failure)
	}
	if res.StatusCode != http.StatusOK {
		r.attemptOK = false
	}
	copyRes := *res
	copyRes.Body = io.NopCloser(bytes.NewReader(raw))
	return &copyRes, nil
}

// Seal must be called only after the caller's complete correlation assertions pass.
func (r *Recorder) Seal() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed || r.failed || r.attemptCount == 0 || !r.attemptOK {
		return errors.New("attempt cannot be sealed")
	}
	raw, err := json.Marshal(manifest{Schema: "observability.query_manifest.v1", Binding: r.binding, SuccessfulAttempt: r.attempt, Responses: r.files})
	if err != nil {
		return err
	}
	if err := writeExclusive(filepath.Join(r.directory, "manifest.json"), raw); err != nil {
		r.failed = true
		return errors.New("query manifest persistence failed")
	}
	r.sealed = true
	return nil
}
