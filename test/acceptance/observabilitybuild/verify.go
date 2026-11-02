package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var commitPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)

func checkBuild(ctx context.Context, repo, commit, project, output string) (provenance, error) {
	if e := privateRoots(output); e != nil {
		return provenance{}, e
	}
	b, e := regularRead(output, "build/provenance.json", 1<<20)
	if e != nil {
		return provenance{}, e
	}
	var p provenance
	if e = decode(b, &p); e != nil {
		return p, e
	}
	if p.Schema != "observability.build_provenance.v1" || p.Commit != commit || p.Project != project || p.Output != output {
		return p, fmt.Errorf("provenance identity mismatch")
	}
	cache := map[string][]byte{}
	names := evidenceNames()
	if len(p.Materials) != len(names) {
		return p, fmt.Errorf("incomplete raw materials")
	}
	for i, m := range p.Materials {
		if m.Name != names[i] {
			return p, fmt.Errorf("duplicate, extra or missing material %s", m.Name)
		}
		limit := int64(1 << 20)
		if strings.HasSuffix(m.Name, ".log") {
			limit = maxSource
		}
		if m.Name == "build/source.json" {
			limit = 8 << 20
		}
		if strings.HasSuffix(m.Name, ".iid") {
			limit = 128
		}
		b, e := regularRead(output, m.Name, limit)
		if e != nil {
			return p, e
		}
		if m != materialOf(m.Name, b) {
			return p, fmt.Errorf("raw material digest mismatch %s", m.Name)
		}
		cache[m.Name] = b
	}
	s, e := captureSource(ctx, repo, commit, project, output, "")
	if e != nil {
		return p, e
	}
	var recorded sourceProof
	if e = decode(cache["build/source.json"], &recorded); e != nil {
		return p, e
	}
	if !bytes.Equal(jsonBytes(s), jsonBytes(recorded)) || p.Tree != s.Tree || p.Digest != s.Digest {
		return p, fmt.Errorf("source proof does not match Git objects")
	}
	if e = validateClaimBytes(project, output, cache["claim.json"]); e != nil {
		return p, e
	}
	read := func(name string) ([]byte, error) {
		key := "build/source/" + name
		if b, ok := cache[key]; ok {
			return b, nil
		}
		b, e := regularRead(output, key, maxFile)
		if e == nil {
			cache[key] = b
		}
		return b, e
	}
	if e = verifySnapshotFrom(output, s, read); e != nil {
		return p, e
	}
	if e = verifyCommands(output, project, p, cache); e != nil {
		return p, e
	}
	return p, nil
}
func verifyCommands(output, project string, p provenance, cache map[string][]byte) error {
	last := time.Time{}
	check := func(role, receipt string, argv []string, stdout, stderr string, exit int) error {
		var r commandProof
		if e := decode(cache[receipt], &r); e != nil {
			return e
		}
		start, e := time.Parse(time.RFC3339Nano, r.Started)
		if e != nil {
			return fmt.Errorf("invalid command start")
		}
		end, e := time.Parse(time.RFC3339Nano, r.Completed)
		if e != nil || start.IsZero() || end.Before(start) || start.Before(last) || end.After(time.Now().UTC().Add(time.Minute)) {
			return fmt.Errorf("invalid command time order")
		}
		last = end
		if r.Schema != "observability.build_command.v1" || r.Role != role || !bytes.Equal(jsonBytes(r.Argv), jsonBytes(argv)) || r.Exit != exit || r.Stdout != materialOf(stdout, cache[stdout]) || r.Stderr != materialOf(stderr, cache[stderr]) {
			return fmt.Errorf("invalid command receipt %s", role)
		}
		return nil
	}
	input := func(name string, argv []string, exit int) error {
		return check(name, "build/"+name+".receipt.json", argv, "build/"+name, "build/"+name+".stderr", exit)
	}
	name := strings.TrimSuffix(string(cache["build/context-show.txt"]), "\n")
	var dc dockerContext
	var eng engineInfo
	if e := uniqueJSON(cache["build/context.json"]); e != nil {
		return e
	}
	if e := json.Unmarshal(cache["build/context.json"], &dc); e != nil {
		return e
	}
	if e := decode(cache["build/engine.json"], &eng); e != nil {
		return e
	}
	builder, e := parseBuilder(cache["build/builder.txt"])
	if e != nil {
		return e
	}
	platform, e := validateDockerIdentity(name, dc, eng, builder)
	if e != nil {
		return e
	}
	if p.Context != name || p.Engine != eng || p.Platform != platform {
		return fmt.Errorf("context/Engine/platform proof mismatch")
	}
	for _, v := range []struct {
		name string
		argv []string
	}{{"context-show.txt", []string{"context", "show"}}, {"context.json", []string{"context", "inspect", name, "--format", "{{json .}}"}}, {"engine.json", []string{"--context", name, "info", "--format", infoFormat}}, {"builder.txt", []string{"--context", name, "buildx", "inspect", name}}} {
		if e = input(v.name, v.argv, 0); e != nil {
			return e
		}
	}
	refs, e := baseReferencesFrom(func(name string) ([]byte, error) {
		b, ok := cache["build/source/"+name]
		if !ok {
			return nil, fmt.Errorf("missing cached source %s", name)
		}
		return b, nil
	})
	if e != nil {
		return e
	}
	image := func(raw, ref string) (imageInfo, error) {
		if e = input(raw, []string{"--context", name, "image", "inspect", ref, "--format", imageFormat}, 0); e != nil {
			return imageInfo{}, e
		}
		var v imageInfo
		if e = decode(cache["build/"+raw], &v); e != nil {
			return v, e
		}
		if !imageIDPattern.MatchString(v.ID) || v.OS+"/"+v.Architecture != platform {
			return v, fmt.Errorf("invalid image %s", raw)
		}
		return v, nil
	}
	for _, key := range []string{"GO_IMAGE", "DEBIAN_IMAGE", "NODE_IMAGE", "PYTHON_IMAGE"} {
		if _, e = image("base-"+key+".json", refs[key]); e != nil {
			return e
		}
	}
	for _, role := range roles {
		raw := "target-" + role + ".json"
		if e = input(raw, []string{"--context", name, "image", "inspect", roleTag(project, role), "--format", imageFormat}, 1); e != nil {
			return e
		}
		if len(cache["build/"+raw]) != 0 || !strings.Contains(string(cache["build/"+raw+".stderr"]), "No such image:") {
			return fmt.Errorf("target absence receipt invalid")
		}
	}
	if len(p.Images) != 3 {
		return fmt.Errorf("missing or duplicate image roles")
	}
	baseID := ""
	for i, role := range roles {
		if role == "controlled_worker" {
			v, e := image("worker-base-before.json", roleTag(project, "worker_base"))
			if e != nil {
				return e
			}
			if v.ID != baseID {
				return fmt.Errorf("controlled base-before mismatch")
			}
		}
		if e = check(role, "build/"+role+".command.json", buildArguments(name, platform, output, project, role), "build/"+role+".stdout.log", "build/"+role+".stderr.log", 0); e != nil {
			return e
		}
		iid := strings.TrimSuffix(string(cache["build/"+role+".iid"]), "\n")
		if !imageIDPattern.MatchString(iid) {
			return fmt.Errorf("invalid IID")
		}
		v, e := image(role+".image.json", roleTag(project, role))
		if e != nil {
			return e
		}
		if iid != v.ID || p.Images[i].Role != role || p.Images[i].ID != iid {
			return fmt.Errorf("image/IID/role proof mismatch")
		}
		if role == "worker_base" {
			baseID = iid
		}
		if role == "controlled_worker" {
			v, e := image("worker-base-after.json", roleTag(project, "worker_base"))
			if e != nil {
				return e
			}
			if v.ID != baseID {
				return fmt.Errorf("controlled base-after mismatch")
			}
		}
	}
	return nil
}

func verifySnapshot(output string, s sourceProof) error {
	return verifySnapshotFrom(output, s, func(p string) ([]byte, error) { return regularRead(output, "build/source/"+p, maxFile) })
}
func verifySnapshotFrom(output string, s sourceProof, read func(string) ([]byte, error)) error {
	if e := privateRoots(output); e != nil {
		return e
	}
	want := map[string]entry{}
	dirs := map[string]bool{".": true}
	for _, v := range s.Entries {
		want[v.Path] = v
		for p := filepath.Dir(v.Path); p != "."; p = filepath.Dir(p) {
			dirs[filepath.ToSlash(p)] = true
		}
	}
	seen := map[string]bool{}
	root := filepath.Join(output, "build", "source")
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot link")
		}
		if d.IsDir() {
			mode := os.FileMode(0755)
			if rel == "." {
				mode = 0700
			}
			if !dirs[rel] || info.Mode().Perm() != mode {
				return fmt.Errorf("extra or non-private snapshot directory %s", rel)
			}
			return nil
		}
		v, ok := want[rel]
		if !ok || seen[rel] || !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected snapshot file %s", rel)
		}
		mode := os.FileMode(0444)
		if v.Mode == "100755" {
			mode = 0555
		}
		if info.Mode().Perm() != mode {
			return fmt.Errorf("snapshot mode mismatch %s", rel)
		}
		b, e := read(rel)
		if e != nil {
			return e
		}
		if int64(len(b)) != v.Bytes || digest(b) != v.SHA256 {
			return fmt.Errorf("snapshot content mismatch %s", rel)
		}
		seen[rel] = true
		return nil
	})
	if e != nil {
		return e
	}
	if len(seen) != len(want) {
		return fmt.Errorf("missing snapshot file")
	}
	return nil
}

var tokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var projectPattern = regexp.MustCompile(`^switchback-obsv-[a-z0-9]{8,24}$`)

// Check every path component before opening; fstat binds the bytes to that inode.
func privateDirectory(p string) error {
	info, e := os.Lstat(p)
	if e != nil {
		return e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return fmt.Errorf("private root must be directory mode 0700: %s", p)
	}
	return nil
}
func privateRoots(output string) error {
	for _, p := range []string{output, filepath.Join(output, "build"), filepath.Join(output, "build", "source")} {
		if e := privateDirectory(p); e != nil {
			return e
		}
	}
	return nil
}
func regularRead(root, name string, limit int64) ([]byte, error) {
	if !safePath(name) {
		return nil, fmt.Errorf("unsafe evidence name %q", name)
	}
	rootInfo, e := os.Lstat(root)
	if e != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("invalid evidence root %s", root)
	}
	current := root
	parts := strings.Split(name, "/")
	for i, c := range parts {
		current = filepath.Join(current, c)
		info, e := os.Lstat(current)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink evidence %s", name)
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return nil, fmt.Errorf("non-directory evidence parent")
			}
			if (i == 0 && c == "build" || i == 1 && parts[0] == "build" && c == "source") && info.Mode().Perm() != 0700 {
				return nil, fmt.Errorf("non-private evidence root %s", current)
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > limit {
			return nil, fmt.Errorf("invalid regular evidence %s", name)
		}
		f, e := os.Open(current)
		if e != nil {
			return nil, e
		}
		opened, e := f.Stat()
		if e != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("evidence changed while opening")
		}
		b, e := io.ReadAll(io.LimitReader(f, limit+1))
		after, ae := f.Stat()
		ce := f.Close()
		if e != nil {
			return nil, e
		}
		if ae != nil || ce != nil || int64(len(b)) > limit || int64(len(b)) != info.Size() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
			return nil, fmt.Errorf("evidence size or identity changed")
		}
		return b, nil
	}
	return nil, fmt.Errorf("missing evidence name")
}
func decode(b []byte, v any) error {
	if e := uniqueJSON(b); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("extra JSON value")
	}
	return nil
}
func uniqueJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var consume func() error
	consume = func() error {
		token, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return fmt.Errorf("duplicate JSON key %v", key)
				}
				seen[k] = true
				if e = consume(); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid JSON object")
			}
		case '[':
			for d.More() {
				if e := consume(); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid JSON array")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		return nil
	}
	if e := consume(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func validateClaim(project, output string) error {
	b, e := regularRead(output, "claim.json", 1<<20)
	if e != nil {
		return e
	}
	return validateClaimBytes(project, output, b)
}
func validateClaimBytes(project, output string, b []byte) error {
	var e error
	var c map[string]string
	if e = uniqueJSON(b); e != nil {
		return e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	if c["project"] != project || c["output"] != output || !tokenPattern.MatchString(c["token"]) {
		return fmt.Errorf("claim identity mismatch")
	}
	return nil
}
