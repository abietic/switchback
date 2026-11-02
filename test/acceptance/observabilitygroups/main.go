// Command observabilitygroups verifies test identities, original function bodies,
// and actual Go build selection. It never executes the enumerated tests.
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

var packages = []string{"internal/architecture", "internal/localproduct", "test/acceptance/observability"}
var native = map[string]string{
	"TestLocalObservabilityExploreNative":                           "internal/architecture",
	"TestLocalObservabilityMonitoringNative":                        "internal/architecture",
	"TestLocalObservabilityAlloyFreshVolumeNative":                  "internal/architecture",
	"TestLocalObservabilityPromQL":                                  "internal/architecture",
	"TestTelemetryWorkerDockerNativeCollectionAndReplacement":       "internal/localproduct",
	"TestSupplementProofsReadPublishedClaimThroughProductionLoader": "test/acceptance/observability",
}

const helper = "internal/architecture/TestMonitoringRegistryFixture"

func main() {
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 2 && args[0] == "--manifest":
		err = checkManifest(args[1])
	case len(args) == 3 && args[0] == "--export-manifest":
		err = checkCommit(args[1], args[2])
	case len(args) == 1 && !strings.HasPrefix(args[0], "-"):
		if _, err := command("", "git", "rev-parse", "--verify", args[0]+"^{commit}"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		err = check(args[0])
	default:
		fmt.Fprintln(os.Stderr, "usage: check-observability-test-groups BASE_COMMIT | --manifest FILE | --export-manifest BASE_COMMIT FILE")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("observability test identities, function bodies and build groups conserved; current groups compile")
}

func check(base string) error { return checkCommit(base, "") }

func checkCommit(base, exportPath string) error {
	rootBytes, err := command("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootBytes))
	old, err := os.MkdirTemp("", "observability-groups-baseline-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(old)
	archive, err := command(root, "git", "archive", "--format=tar", base)
	if err != nil {
		return err
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := filepath.Join(old, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(name, old+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		if h.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(name, 0700); err != nil {
				return err
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		if err := os.WriteFile(name, data, 0600); err != nil {
			return err
		}
	}
	before, err := functions(old, true)
	if err != nil {
		return err
	}
	after, err := functions(root, false)
	if err != nil {
		return err
	}
	expected := make(map[string]string, len(before))
	for id, value := range before {
		expected[id] = value
	}
	resolved, err := command(root, "git", "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return err
	}
	var additions []registeredAddition
	if strings.TrimSpace(string(resolved)) == registeredBaseline {
		additions = registeredAdditions
	}
	seen := map[string]bool{}
	for _, entry := range additions {
		if entry.ID == "" || seen[entry.ID] {
			return fmt.Errorf("registered addition empty or duplicate identity: %s", entry.ID)
		}
		seen[entry.ID] = true
		if len(entry.SHA256) != 64 || strings.Trim(entry.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("registered addition invalid digest: %s", entry.ID)
		}
		if _, ok := before[entry.ID]; ok {
			return fmt.Errorf("registered addition collides with baseline: %s", entry.ID)
		}
		if entry.Groups != nil && !reflect.DeepEqual(entry.Groups, []string{"", "integration"}) {
			return fmt.Errorf("registered addition invalid groups: %s", entry.ID)
		}
		actual, ok := after[entry.ID]
		if !ok {
			return fmt.Errorf("registered addition missing: %s", entry.ID)
		}
		sum := sha256.Sum256([]byte(actual))
		if fmt.Sprintf("%x", sum) != entry.SHA256 {
			return fmt.Errorf("registered addition changed: %s", entry.ID)
		}
		expected[entry.ID] = actual
	}
	if !reflect.DeepEqual(expected, after) {
		return fmt.Errorf("test/helper function identity or body changed: %s", differences(expected, after))
	}
	groups := make(map[string]map[string]string)
	for _, tags := range []string{"", "integration"} {
		original, err := selected(old, tags)
		if err != nil {
			return err
		}
		current, err := selected(root, tags)
		if err != nil {
			return err
		}
		for name, pkg := range native {
			delete(original, pkg+"/"+name)
		}
		delete(original, helper)
		for _, entry := range additions {
			for _, group := range entry.Groups {
				if group == tags {
					original[entry.ID] = "test"
				}
			}
		}
		groups[tags] = original
		if !reflect.DeepEqual(original, current) {
			return fmt.Errorf("ordinary group %q changed: %s", tags, differences(original, current))
		}
	}
	if err := checkCurrentGroups(root); err != nil {
		return err
	}
	if exportPath != "" {
		m := portableManifest{Version: 1, Provenance: "commit-mode normalized AST and ordinary groups; strict preservation and five compilation groups verified", Packages: packages}
		for id, body := range expected {
			m.Functions = append(m.Functions, manifestFunction{ID: id, SHA256: digest(body)})
		}
		sort.Slice(m.Functions, func(i, j int) bool { return m.Functions[i].ID < m.Functions[j].ID })
		for _, tags := range []string{"", "integration"} {
			g := manifestGroup{Tags: tags}
			for id := range groups[tags] {
				g.Tests = append(g.Tests, id)
			}
			sort.Strings(g.Tests)
			m.Groups = append(m.Groups, g)
		}
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(exportPath, append(data, '\n'), 0644)
	}
	return nil
}

func checkCurrentGroups(root string) error {
	ordinary, err := selected(root, "integration")
	if err != nil {
		return err
	}
	physical, err := selected(root, "integration,observability_native")
	if err != nil {
		return err
	}
	for name, pkg := range native {
		ordinary[pkg+"/"+name] = "test"
	}
	if !reflect.DeepEqual(ordinary, physical) {
		return fmt.Errorf("native group mismatch: %s", differences(ordinary, physical))
	}
	nativeOnly, err := selected(root, "observability_native")
	if err != nil {
		return err
	}
	nativeExpected, err := selected(root, "")
	if err != nil {
		return err
	}
	for name, pkg := range native {
		if pkg == "internal/architecture" {
			nativeExpected[pkg+"/"+name] = "test"
		}
	}
	if !reflect.DeepEqual(nativeExpected, nativeOnly) {
		return fmt.Errorf("native-only group mismatch: %s", differences(nativeExpected, nativeOnly))
	}
	fixture, err := selected(root, "observability_fixture")
	if err != nil {
		return err
	}
	plain, err := selected(root, "")
	if err != nil {
		return err
	}
	plain[helper] = "test"
	if !reflect.DeepEqual(plain, fixture) {
		return fmt.Errorf("fixture group mismatch: %s", differences(plain, fixture))
	}
	// Listing Test identities alone cannot detect a shared helper moved into
	// a narrower build group. -export compiles the selected test packages,
	// including external tests, without executing tests or TestMain.
	for _, tags := range []string{"", "integration", "observability_native", "integration,observability_native", "observability_fixture"} {
		args := []string{"list", "-p=1", "-export", "-test"}
		if tags != "" {
			args = append(args, "-tags="+tags)
		}
		for _, pkg := range packages {
			args = append(args, "./"+pkg)
		}
		if _, err := command(root, "go", args...); err != nil {
			return fmt.Errorf("current build group %q does not compile: %w", tags, err)
		}
	}
	return nil
}

func functions(root string, baseline bool) (map[string]string, error) {
	result := map[string]string{}
	for _, pkg := range packages {
		files, err := filepath.Glob(filepath.Join(root, pkg, "*_test.go"))
		if err != nil {
			return nil, err
		}
		for _, name := range files {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				return nil, err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				id := pkg + "/" + fn.Name.Name
				if fn.Recv != nil {
					var b bytes.Buffer
					if err := format.Node(&b, fset, fn.Recv.List[0].Type); err != nil {
						return nil, err
					}
					id += "/" + b.String()
				}
				if _, exists := result[id]; exists {
					return nil, fmt.Errorf("duplicate function %s", id)
				}
				// Normalize only the exact documented helper cross-build insertion.
				if baseline && id == "internal/architecture/newMonitoringFixture" {
					addFixtureTag(fn)
				}
				var b bytes.Buffer
				// An empty FileSet removes source-layout differences from the
				// printed AST. Retain semicolons: for-clause separators encode
				// control flow, even when every other token stays unchanged.
				if err := format.Node(&b, token.NewFileSet(), fn); err != nil {
					return nil, err
				}
				result[id] = tokens(b.Bytes())
			}
		}
	}
	return result, nil
}

func addFixtureTag(fn *ast.FuncDecl) {
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 7 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "CommandContext" {
			return true
		}
		owner, ok := sel.X.(*ast.Ident)
		if !ok || owner.Name != "exec" {
			return true
		}
		want := []string{"\"go\"", "\"test\"", "\"-c\"", "\"-o\""}
		for i, value := range want {
			lit, ok := call.Args[i+1].(*ast.BasicLit)
			if !ok || lit.Value != value {
				return true
			}
		}
		last, ok := call.Args[6].(*ast.BasicLit)
		if !ok || last.Value != "\".\"" {
			return true
		}
		ctx, ok := call.Args[0].(*ast.Ident)
		if !ok || ctx.Name != "ctx" {
			return true
		}
		binary, ok := call.Args[5].(*ast.Ident)
		if !ok || binary.Name != "binary" {
			return true
		}
		args := append([]ast.Expr{}, call.Args[:4]...)
		args = append(args, &ast.BasicLit{Kind: token.STRING, Value: "\"-tags=observability_fixture\""})
		call.Args = append(args, call.Args[4:]...)
		return true
	})
}

func tokens(source []byte) string {
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile("", -1, len(source)), source, nil, 0)
	var b strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		fmt.Fprintf(&b, "%d:%q;", tok, lit)
	}
	return b.String()
}

func selected(root, tags string) (map[string]string, error) {
	args := []string{"list", "-p=1", "-json", "-test"}
	if tags != "" {
		args = append(args, "-tags="+tags)
	}
	for _, pkg := range packages {
		args = append(args, "./"+pkg)
	}
	data, err := command(root, "go", args...)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	result := map[string]string{}
	for {
		var pkg struct {
			ImportPath, Dir           string
			TestGoFiles, XTestGoFiles []string
		}
		err := dec.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		for _, relative := range packages {
			if pkg.ImportPath != "github.com/abietic/switchback/"+relative {
				continue
			}
			for _, name := range append(pkg.TestGoFiles, pkg.XTestGoFiles...) {
				file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pkg.Dir, name), nil, 0)
				if err != nil {
					return nil, err
				}
				for _, decl := range file.Decls {
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
						continue
					}
					result[relative+"/"+fn.Name.Name] = "test"
				}
			}
		}
	}
	return result, nil
}

func differences(want, got map[string]string) string {
	var items []string
	for id, value := range want {
		if actual, ok := got[id]; !ok {
			items = append(items, "missing "+id)
		} else if value != actual {
			items = append(items, "changed "+id)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			items = append(items, "unexpected "+id)
		}
	}
	sort.Strings(items)
	return strings.Join(items, "; ")
}
func command(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// Ignore a parent workspace, which would select a different checkout.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOMAXPROCS=2")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", name, args, err, stderr.String())
	}
	return out, nil
}

// A manifest contains no private Git objects, source archive or author metadata.
// Export requires the strict commit gate to pass; CI only consumes reviewed data.
type portableManifest struct {
	Version    int                `json:"version"`
	Provenance string             `json:"provenance"`
	Packages   []string           `json:"packages"`
	Functions  []manifestFunction `json:"functions"`
	Groups     []manifestGroup    `json:"groups"`
}
type manifestFunction struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}
type manifestGroup struct {
	Tags  string   `json:"tags"`
	Tests []string `json:"tests"`
}

func digest(body string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(body))) }
func validID(id string) bool {
	for _, pkg := range packages {
		if strings.HasPrefix(id, pkg+"/") && len(id) > len(pkg)+1 {
			return true
		}
	}
	return false
}

// Reject ambiguous object members before typed decoding can apply last-value wins.
// Token traversal includes objects nested inside arrays and compares decoded keys.
func uniqueJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				token, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := token.(string)
				if !ok {
					return fmt.Errorf("invalid JSON object key")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON key: %s", key)
				}
				seen[key] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = dec.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("invalid manifest trailing data")
	}
	return nil
}

func checkManifest(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := uniqueJSONKeys(data); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	var m portableManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid manifest trailing data")
	}
	if m.Version != 1 || m.Provenance == "" || !reflect.DeepEqual(m.Packages, packages) || len(m.Functions) == 0 || len(m.Groups) != 2 {
		return fmt.Errorf("invalid manifest schema")
	}
	want := map[string]string{}
	for _, f := range m.Functions {
		if !validID(f.ID) || want[f.ID] != "" || len(f.SHA256) != 64 || strings.Trim(f.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("invalid manifest function: %s", f.ID)
		}
		want[f.ID] = f.SHA256
	}
	rootBytes, err := command("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootBytes))
	bodies, err := functions(root, false)
	if err != nil {
		return err
	}
	got := map[string]string{}
	for id, body := range bodies {
		got[id] = digest(body)
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("test/helper function identity or body changed: %s", differences(want, got))
	}
	for i, tags := range []string{"", "integration"} {
		g := m.Groups[i]
		if g.Tags != tags || len(g.Tests) == 0 {
			return fmt.Errorf("invalid manifest group")
		}
		expected := map[string]string{}
		for _, id := range g.Tests {
			if !validID(id) || want[id] == "" || expected[id] != "" {
				return fmt.Errorf("invalid manifest group identity: %s", id)
			}
			expected[id] = "test"
		}
		current, err := selected(root, tags)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(expected, current) {
			return fmt.Errorf("ordinary group %q changed: %s", tags, differences(expected, current))
		}
	}
	return checkCurrentGroups(root)
}
