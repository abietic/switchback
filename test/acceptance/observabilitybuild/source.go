package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxFile = 16 << 20
const maxSource = 256 << 20

type entry struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type sourceProof struct {
	Schema  string  `json:"schema"`
	Project string  `json:"project"`
	Output  string  `json:"output"`
	Commit  string  `json:"source_commit"`
	Tree    string  `json:"source_tree"`
	Digest  string  `json:"source_manifest_sha256"`
	Entries []entry `json:"entries"`
}

func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func safePath(p string) bool {
	if p == "" || !utf8.ValidString(p) || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if c == ".." || c == ".git" {
			return false
		}
	}
	return true
}
func jsonBytes(v any) []byte { b, _ := json.Marshal(v); return b }
func exclusive(p string, b []byte) error {
	f, e := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	ce := f.Close()
	if e != nil {
		return e
	}
	return ce
}

// Git objects are the authority, not the caller's working tree or manifest.
func captureSource(ctx context.Context, repo, commit, project, output, destination string) (sourceProof, error) {
	s := sourceProof{Schema: "observability.build_source.v1", Project: project, Output: output, Commit: commit}
	tree, e := git(ctx, repo, "rev-parse", commit+"^{tree}")
	if e != nil {
		return s, e
	}
	s.Tree = strings.TrimSpace(string(tree))
	listing, e := git(ctx, repo, "ls-tree", "-r", "-z", "-l", commit)
	if e != nil {
		return s, e
	}
	type blob struct {
		mode, id string
		size     int64
	}
	want := map[string]blob{}
	var total int64
	for _, line := range bytes.Split(listing, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		parts := bytes.SplitN(line, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return s, fmt.Errorf("invalid Git tree")
		}
		fields := strings.Fields(string(parts[0]))
		p := string(parts[1])
		if len(fields) != 4 || !safePath(p) || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
			return s, fmt.Errorf("unsupported Git source %q", p)
		}
		size, e := strconv.ParseInt(fields[3], 10, 64)
		if e != nil || size < 0 || size > maxFile {
			return s, fmt.Errorf("source size limit %q", p)
		}
		total += size
		if total > maxSource || len(want) >= 12000 {
			return s, fmt.Errorf("source limits exceeded")
		}
		if _, ok := want[p]; ok {
			return s, fmt.Errorf("duplicate Git path")
		}
		want[p] = blob{fields[0], fields[2], size}
	}
	archiveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := gitCommand(archiveCtx, repo, "-c", "tar.umask=022", "archive", "--format=tar", commit)
	errout := boundedBuffer{limit: 1 << 20}
	command.Stderr = &errout
	pipe, e := command.StdoutPipe()
	if e != nil {
		return s, e
	}
	if e = command.Start(); e != nil {
		pipe.Close()
		return s, e
	}
	waited := false
	defer func() {
		cancel()
		pipe.Close()
		if !waited {
			command.Wait()
		}
	}()
	limited := &io.LimitedReader{R: pipe, N: maxSource + (32 << 20) + 1}
	tr := tar.NewReader(limited)
	seen := map[string]bool{}
	seenDirectories := map[string]bool{}
	globalSeen := false
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return s, fmt.Errorf("archive: %w", e)
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			if globalSeen || len(h.PAXRecords) != 1 || h.PAXRecords["comment"] != commit {
				return s, fmt.Errorf("unexpected global archive metadata")
			}
			globalSeen = true
			continue
		}
		if h.Typeflag == tar.TypeDir {
			p := strings.TrimSuffix(h.Name, "/")
			if !safePath(p) || seenDirectories[p] {
				return s, fmt.Errorf("invalid archive directory")
			}
			seenDirectories[p] = true
			valid := false
			for name := range want {
				if strings.HasPrefix(name, p+"/") {
					valid = true
					break
				}
			}
			if !valid {
				return s, fmt.Errorf("extra archive directory")
			}
			continue
		}
		w, ok := want[h.Name]
		mode := int64(0644)
		if w.mode == "100755" {
			mode = 0755
		}
		if !ok || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size != w.size || h.Mode&0777 != mode {
			return s, fmt.Errorf("archive mismatch %q", h.Name)
		}
		b, e := io.ReadAll(io.LimitReader(tr, maxFile+1))
		if e != nil || int64(len(b)) != w.size {
			return s, fmt.Errorf("truncated archive %q", h.Name)
		}
		hash := sha1.New()
		fmt.Fprintf(hash, "blob %d\x00", len(b))
		hash.Write(b)
		if hex.EncodeToString(hash.Sum(nil)) != w.id {
			return s, fmt.Errorf("archive changed Git blob %q", h.Name)
		}
		seen[h.Name] = true
		s.Entries = append(s.Entries, entry{h.Name, w.mode, w.size, digest(b)})
		if destination != "" {
			if e = os.MkdirAll(destination, 0700); e != nil {
				return s, e
			}
			p := filepath.Join(destination, filepath.FromSlash(h.Name))
			if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
				return s, e
			}
			if e = exclusive(p, b); e != nil {
				return s, e
			}
			mode := os.FileMode(0444)
			if w.mode == "100755" {
				mode = 0555
			}
			if e = os.Chmod(p, mode); e != nil {
				return s, e
			}
		}
	}
	if len(seen) != len(want) {
		return s, fmt.Errorf("archive omitted source")
	}
	if destination != "" {
		if e = filepath.WalkDir(destination, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				mode := os.FileMode(0755)
				if p == destination {
					mode = 0700
				}
				return os.Chmod(p, mode)
			}
			return nil
		}); e != nil {
			return s, e
		}
	}
	padding, e := io.ReadAll(io.LimitReader(limited, 10241))
	if e != nil || len(padding) > 10240 || limited.N <= 0 {
		return s, fmt.Errorf("archive trailing bytes or limit exceeded")
	}
	for _, b := range padding {
		if b != 0 {
			return s, fmt.Errorf("archive has trailing non-padding bytes")
		}
	}
	if e = command.Wait(); e != nil {
		waited = true
		return s, fmt.Errorf("archive command: %w: %s", e, errout.String())
	}
	waited = true
	sort.Slice(s.Entries, func(i, j int) bool { return s.Entries[i].Path < s.Entries[j].Path })
	s.Digest = digest(jsonBytes(s.Entries))
	return s, nil
}
