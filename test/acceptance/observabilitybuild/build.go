package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var roles = []string{"backend", "worker_base", "controlled_worker"}

type material struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Count only bytes accepted by the real log file. Raw-file validation below
// remains authoritative for successful builds; this summary preserves failures.
type buildLogWriter struct {
	file  *os.File
	hash  hash.Hash
	bytes int64
	err   error
}

func (w *buildLogWriter) Write(p []byte) (int, error) {
	n, e := w.file.Write(p)
	w.hash.Write(p[:n])
	w.bytes += int64(n)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	if w.err == nil {
		w.err = e
	}
	return n, e
}
func (w *buildLogWriter) material(name string) material {
	return material{name, w.bytes, fmt.Sprintf("%x", w.hash.Sum(nil))}
}

type commandProof struct {
	Schema    string   `json:"schema"`
	Role      string   `json:"role"`
	Argv      []string `json:"argv"`
	Started   string   `json:"started_at"`
	Completed string   `json:"completed_at"`
	Exit      int      `json:"exit_code"`
	Stdout    material `json:"stdout"`
	Stderr    material `json:"stderr"`
}
type builtImage struct {
	Role string `json:"role"`
	ID   string `json:"id"`
}
type provenance struct {
	Schema    string       `json:"schema"`
	Project   string       `json:"project"`
	Output    string       `json:"output"`
	Commit    string       `json:"source_commit"`
	Tree      string       `json:"source_tree"`
	Digest    string       `json:"source_manifest_sha256"`
	Context   string       `json:"context"`
	Engine    engineInfo   `json:"engine"`
	Platform  string       `json:"platform"`
	Images    []builtImage `json:"images"`
	Materials []material   `json:"materials"`
}

func buildArguments(contextName, platform, output, project, role string) []string {
	dockerfile := map[string]string{"backend": "images/backend/Dockerfile", "worker_base": "images/worker/Dockerfile", "controlled_worker": "test/acceptance/observability/Dockerfile.worker"}[role]
	tag := roleTag(project, role)
	source := filepath.Join(output, "build", "source")
	a := []string{"--context", contextName, "build", "--builder", contextName, "--load", "--pull=false", "--progress=plain", "--platform", platform, "--iidfile", filepath.Join(output, "build", role+".iid"), "-t", tag, "-f", filepath.Join(source, filepath.FromSlash(dockerfile))}
	if role == "controlled_worker" {
		a = append(a, "--build-arg", "WORKER_BASE="+project+"-worker-base:acceptance")
	}
	return append(a, source)
}
func roleTag(project, role string) string {
	return map[string]string{"backend": project + "-backend:acceptance", "worker_base": project + "-worker-base:acceptance", "controlled_worker": project + "-worker:controlled"}[role]
}

type dockerContext struct {
	Name      string `json:"Name"`
	Endpoints map[string]struct {
		Host string `json:"Host"`
	} `json:"Endpoints"`
}
type engineInfo struct {
	ID           string `json:"ID"`
	Name         string `json:"Name"`
	OS           string `json:"OSType"`
	Architecture string `json:"Architecture"`
}
type builderInfo struct {
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	Nodes  []struct {
		Name     string `json:"Name"`
		Endpoint string `json:"Endpoint"`
		Status   string `json:"Status"`
	} `json:"Nodes"`
}
type imageInfo struct {
	ID           string `json:"Id"`
	OS           string `json:"Os"`
	Architecture string `json:"Architecture"`
}

const infoFormat = `{"ID":{{json .ID}},"Name":{{json .Name}},"OSType":{{json .OSType}},"Architecture":{{json .Architecture}}}`
const imageFormat = `{"Id":{{json .Id}},"Os":{{json .Os}},"Architecture":{{json .Architecture}}}`

var imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var baseRefPattern = regexp.MustCompile(`^[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}$`)

func baseReferences(output string) (map[string]string, error) {
	return baseReferencesFrom(func(p string) ([]byte, error) { return regularRead(output, "build/source/"+p, maxFile) })
}
func baseReferencesFrom(read func(string) ([]byte, error)) (map[string]string, error) {
	refs := map[string]string{}
	for _, p := range []string{"images/backend/Dockerfile", "images/worker/Dockerfile", "test/acceptance/observability/Dockerfile.worker"} {
		b, e := read(p)
		if e != nil {
			return nil, e
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "ARG ") {
				continue
			}
			parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "ARG ")), "=", 2)
			key := parts[0]
			if key != "GO_IMAGE" && key != "DEBIAN_IMAGE" && key != "NODE_IMAGE" && key != "PYTHON_IMAGE" {
				continue
			}
			if len(parts) != 2 || !baseRefPattern.MatchString(parts[1]) {
				return nil, fmt.Errorf("base ARG must have pinned default %s", key)
			}
			if old, ok := refs[key]; ok && old != parts[1] {
				return nil, fmt.Errorf("conflicting base ARG %s", key)
			}
			refs[key] = parts[1]
		}
	}
	if len(refs) != 4 {
		return nil, fmt.Errorf("missing fixed base ARG")
	}
	unique := map[string]bool{}
	for _, v := range refs {
		unique[v] = true
	}
	if len(unique) != 4 {
		return nil, fmt.Errorf("base references are not unique")
	}
	return refs, nil
}
func inspectImage(ctx context.Context, d DockerCommand, output, name, contextName, ref, platform string) (imageInfo, error) {
	b, e := dockerInput(ctx, d, output, name, []string{"--context", contextName, "image", "inspect", ref, "--format", imageFormat})
	if e != nil {
		return imageInfo{}, e
	}
	var v imageInfo
	if e = decode(b, &v); e != nil {
		return v, e
	}
	if !imageIDPattern.MatchString(v.ID) || v.OS != "linux" || v.OS+"/"+v.Architecture != platform {
		return v, fmt.Errorf("image ID/platform mismatch %s", ref)
	}
	return v, nil
}

func materialOf(name string, b []byte) material { return material{name, int64(len(b)), digest(b)} }
func dockerInput(ctx context.Context, d DockerCommand, output, name string, a []string) ([]byte, error) {
	out, err := boundedBuffer{limit: 1 << 20}, boundedBuffer{limit: 1 << 20}
	started := time.Now().UTC()
	e := d(ctx, a, &out, &err)
	completed := time.Now().UTC()
	b := out.Bytes()
	if len(b) > 1<<20 || err.Len() > 1<<20 {
		return nil, fmt.Errorf("Docker metadata too large")
	}
	if writeErr := exclusive(filepath.Join(output, "build", name), b); writeErr != nil {
		return nil, writeErr
	}
	if writeErr := exclusive(filepath.Join(output, "build", name+".stderr"), err.Bytes()); writeErr != nil {
		return nil, writeErr
	}
	exit := 0
	if e != nil {
		exit = 1
	}
	r := commandProof{"observability.build_command.v1", name, a, started.Format(time.RFC3339Nano), completed.Format(time.RFC3339Nano), exit, materialOf("build/"+name, b), materialOf("build/"+name+".stderr", err.Bytes())}
	if writeErr := exclusive(filepath.Join(output, "build", name+".receipt.json"), jsonBytes(r)); writeErr != nil {
		return nil, writeErr
	}
	if e != nil {
		return nil, fmt.Errorf("Docker %v: %w: %s", a, e, err.String())
	}
	return b, nil
}
func parseBuilder(b []byte) (builderInfo, error) {
	var v builderInfo
	node := false
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := strings.SplitN(line, ":", 2)
		if len(p) != 2 {
			return v, fmt.Errorf("malformed builder receipt")
		}
		key, val := strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		scope := "builder:"
		if node {
			scope = "node:"
		}
		if key == "Nodes" {
			if seen["Nodes"] || val != "" {
				return v, fmt.Errorf("invalid builder nodes")
			}
			seen["Nodes"] = true
			node = true
			v.Nodes = append(v.Nodes, struct {
				Name     string `json:"Name"`
				Endpoint string `json:"Endpoint"`
				Status   string `json:"Status"`
			}{})
			continue
		}
		switch key {
		case "Name", "Driver", "Endpoint", "Status":
			if seen[scope+key] || val == "" {
				return v, fmt.Errorf("duplicate or empty builder %s", key)
			}
			seen[scope+key] = true
			if node {
				switch key {
				case "Name":
					v.Nodes[0].Name = val
				case "Endpoint":
					v.Nodes[0].Endpoint = val
				case "Status":
					v.Nodes[0].Status = val
				default:
					return v, fmt.Errorf("unexpected node driver")
				}
			} else {
				switch key {
				case "Name":
					v.Name = val
				case "Driver":
					v.Driver = val
				default:
					return v, fmt.Errorf("unexpected builder field")
				}
			}
		case "Last Activity", "BuildKit", "BuildKit version", "Platforms", "Labels", "All", "Filters", "Keep Duration", "Keep Bytes", "Max Used Space", "Min Free Space", "Reserved Space":
		default:
			if !strings.HasPrefix(key, "GC Policy rule#") && !strings.HasPrefix(key, "org.mobyproject.buildkit.worker.") {
				return v, fmt.Errorf("unknown builder field %q", key)
			}
		}
	}
	if v.Name == "" || v.Driver == "" || len(v.Nodes) != 1 || v.Nodes[0].Name == "" || v.Nodes[0].Endpoint == "" || v.Nodes[0].Status == "" {
		return v, fmt.Errorf("incomplete builder receipt")
	}
	return v, nil
}
func validateDockerIdentity(name string, c dockerContext, engine engineInfo, builder builderInfo) (string, error) {
	endpoint := c.Endpoints["docker"].Host
	if name == "" || strings.ContainsAny(name, " \t\r\n/") || c.Name != name || !strings.HasPrefix(endpoint, "unix:///") || strings.ContainsAny(endpoint, "\r\n") || engine.ID == "" || engine.Name == "" || engine.OS != "linux" || builder.Name != name || builder.Driver != "docker" || len(builder.Nodes) != 1 || builder.Nodes[0].Status != "running" || (builder.Nodes[0].Endpoint != name && builder.Nodes[0].Endpoint != endpoint) {
		return "", fmt.Errorf("Docker context/Engine/builder is not a consistent local single-node Docker driver")
	}
	arch := engine.Architecture
	switch arch {
	case "aarch64":
		arch = "arm64"
	case "x86_64":
		arch = "amd64"
	}
	if arch != "arm64" && arch != "amd64" {
		return "", fmt.Errorf("unsupported Docker architecture")
	}
	return "linux/" + arch, nil
}
func executeBuild(ctx context.Context, d DockerCommand, output, project string, s sourceProof) error {
	b, e := dockerInput(ctx, d, output, "context-show.txt", []string{"context", "show"})
	if e != nil {
		return e
	}
	name := strings.TrimSpace(string(b))
	inputs := map[string][]byte{}
	for _, input := range []struct {
		name string
		a    []string
	}{{"context.json", []string{"context", "inspect", name, "--format", "{{json .}}"}}, {"engine.json", []string{"--context", name, "info", "--format", infoFormat}}, {"builder.txt", []string{"--context", name, "buildx", "inspect", name}}} {
		if inputs[input.name], e = dockerInput(ctx, d, output, input.name, input.a); e != nil {
			return e
		}
	}
	var dc dockerContext
	var eng engineInfo
	if e = uniqueJSON(inputs["context.json"]); e != nil {
		return e
	}
	if e = json.Unmarshal(inputs["context.json"], &dc); e != nil {
		return e
	}
	if e = decode(inputs["engine.json"], &eng); e != nil {
		return e
	}
	builder, e := parseBuilder(inputs["builder.txt"])
	if e != nil {
		return e
	}
	platform, e := validateDockerIdentity(name, dc, eng, builder)
	if e != nil {
		return e
	}
	refs, e := baseReferences(output)
	if e != nil {
		return e
	}
	for _, key := range []string{"GO_IMAGE", "DEBIAN_IMAGE", "NODE_IMAGE", "PYTHON_IMAGE"} {
		if _, e = inspectImage(ctx, d, output, "base-"+key+".json", name, refs[key], platform); e != nil {
			return e
		}
	}
	for _, role := range roles {
		_, err := dockerInput(ctx, d, output, "target-"+role+".json", []string{"--context", name, "image", "inspect", roleTag(project, role), "--format", imageFormat})
		if err == nil {
			return fmt.Errorf("target already exists %s", role)
		}
		if !strings.Contains(err.Error(), "No such image:") {
			return fmt.Errorf("target absence unproven: %w", err)
		}
	}
	workerBaseID := ""
	images := []builtImage{}
	for _, role := range roles {
		if e := verifySnapshot(output, s); e != nil {
			return e
		}
		if role == "controlled_worker" {
			image, e := inspectImage(ctx, d, output, "worker-base-before.json", name, roleTag(project, "worker_base"), platform)
			if e != nil {
				return e
			}
			if image.ID != workerBaseID {
				return fmt.Errorf("worker base changed before controlled build")
			}
		}
		a := buildArguments(name, platform, output, project, role)
		outName, errName := role+".stdout.log", role+".stderr.log"
		out, e := os.OpenFile(filepath.Join(output, "build", outName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		errout, e := os.OpenFile(filepath.Join(output, "build", errName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			out.Close()
			return e
		}
		started := time.Now().UTC()
		outLog := buildLogWriter{file: out, hash: sha256.New()}
		errLog := buildLogWriter{file: errout, hash: sha256.New()}
		runErr := d(ctx, a, &outLog, &errLog)
		completed := time.Now().UTC()
		oe, ee := out.Close(), errout.Close()
		exit := 0
		if runErr != nil {
			exit = 1
			if ec, ok := runErr.(interface{ ExitCode() int }); ok {
				exit = ec.ExitCode()
			}
		}
		receipt := commandProof{"observability.build_command.v1", role, a, started.Format(time.RFC3339Nano), completed.Format(time.RFC3339Nano), exit, outLog.material("build/" + outName), errLog.material("build/" + errName)}
		if e = exclusive(filepath.Join(output, "build", role+".command.json"), jsonBytes(receipt)); e != nil {
			return e
		}
		if oe != nil || ee != nil {
			return fmt.Errorf("build log close failed: %v / %v", oe, ee)
		}
		if outLog.err != nil || errLog.err != nil {
			return fmt.Errorf("build log write failed: %v / %v", outLog.err, errLog.err)
		}
		ob, e := regularRead(output, "build/"+outName, maxSource)
		if e != nil {
			return e
		}
		eb, e := regularRead(output, "build/"+errName, maxSource)
		if e != nil {
			return e
		}
		if materialOf("build/"+outName, ob) != receipt.Stdout || materialOf("build/"+errName, eb) != receipt.Stderr {
			return fmt.Errorf("build log differs from written bytes")
		}
		if runErr != nil {
			return fmt.Errorf("%s build failed: %w", role, runErr)
		}
		if e := verifySnapshot(output, s); e != nil {
			return e
		}
		iid, e := regularRead(output, "build/"+role+".iid", 128)
		if e != nil {
			return e
		}
		id := strings.TrimSuffix(string(iid), "\n")
		if !imageIDPattern.MatchString(id) {
			return fmt.Errorf("invalid IID %s", role)
		}
		image, e := inspectImage(ctx, d, output, role+".image.json", name, roleTag(project, role), platform)
		if e != nil {
			return e
		}
		if image.ID != id {
			return fmt.Errorf("IID differs from selected image inspect %s", role)
		}
		images = append(images, builtImage{role, id})
		if role == "worker_base" {
			workerBaseID = id
		}
		if role == "controlled_worker" {
			image, e := inspectImage(ctx, d, output, "worker-base-after.json", name, roleTag(project, "worker_base"), platform)
			if e != nil {
				return e
			}
			if image.ID != workerBaseID {
				return fmt.Errorf("worker base changed after controlled build")
			}
		}
	}
	if e = verifySnapshot(output, s); e != nil {
		return e
	}
	p := provenance{"observability.build_provenance.v1", project, output, s.Commit, s.Tree, s.Digest, name, eng, platform, images, nil}
	for _, name := range evidenceNames() {
		b, e := regularRead(output, name, maxSource)
		if e != nil {
			return e
		}
		p.Materials = append(p.Materials, materialOf(name, b))
	}
	return exclusive(filepath.Join(output, "build", "provenance.json"), jsonBytes(p))
}
func evidenceNames() []string {
	names := []string{"claim.json", "build/source.json"}
	for _, p := range []string{"context-show.txt", "context.json", "engine.json", "builder.txt", "base-GO_IMAGE.json", "base-DEBIAN_IMAGE.json", "base-NODE_IMAGE.json", "base-PYTHON_IMAGE.json", "target-backend.json", "target-worker_base.json", "target-controlled_worker.json", "worker-base-before.json", "worker-base-after.json"} {
		names = append(names, "build/"+p, "build/"+p+".stderr", "build/"+p+".receipt.json")
	}
	for _, r := range roles {
		names = append(names, "build/"+r+".command.json", "build/"+r+".stdout.log", "build/"+r+".stderr.log", "build/"+r+".iid", "build/"+r+".image.json", "build/"+r+".image.json.stderr", "build/"+r+".image.json.receipt.json")
	}
	sort.Strings(names)
	return names
}
