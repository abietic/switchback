package capacitysnapshot

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/moby/moby/client"
	"strings"
	"testing"
)

func qualificationImageTar(t *testing.T, binary []byte, mutation string) ([]byte, string) {
	t.Helper()
	var layer bytes.Buffer
	w := tar.NewWriter(&layer)
	header := &tar.Header{Name: "usr/local/bin/fixed", Mode: 0755, Size: int64(len(binary)), Typeflag: tar.TypeReg}
	if mutation == "link" {
		header.Typeflag = tar.TypeSymlink
		header.Linkname = "/wrong"
		header.Size = 0
	}
	if e := w.WriteHeader(header); e != nil {
		t.Fatal(e)
	}
	if mutation != "link" {
		_, _ = w.Write(binary)
	}
	_ = w.Close()
	cfg := []byte(`{"rootfs":{"diff_ids":["` + Digest(layer.Bytes()) + `"]}}`)
	id := Digest(cfg)
	if mutation == "layer-digest" {
		cfg = []byte(`{"rootfs":{"diff_ids":["` + Digest([]byte("wrong")) + `"]}}`)
		id = Digest(cfg)
	}
	var outer bytes.Buffer
	o := tar.NewWriter(&outer)
	manifest, _ := json.Marshal([]struct {
		Config string
		Layers []string
	}{{"config.json", []string{"layer/layer.tar"}}})
	for _, x := range []struct {
		name string
		b    []byte
	}{{"config.json", cfg}, {"manifest.json", manifest}, {"layer/layer.tar", layer.Bytes()}} {
		_ = o.WriteHeader(&tar.Header{Name: x.name, Mode: 0400, Size: int64(len(x.b)), Typeflag: tar.TypeReg})
		_, _ = o.Write(x.b)
	}
	_ = o.Close()
	return outer.Bytes(), id
}
func TestQualificationImageArchiveUsesSeparateBoundAndExactRoot(t *testing.T) {
	binary := bytes.Repeat([]byte("large-valid-executable"), 60000)
	raw, id := qualificationImageTar(t, binary, "")
	got, e := qualificationExtractImageArtifact(context.Background(), bytes.NewReader(raw), id, "usr/local/bin/fixed")
	if e != nil || !bytes.Equal(got, binary) || len(got) <= 512<<10 {
		t.Fatal(e, len(got))
	}
	for _, failure := range []string{"link", "layer-digest", "truncated", "wrong-image", "missing"} {
		t.Run(failure, func(t *testing.T) {
			raw, id := qualificationImageTar(t, binary, failure)
			target := "usr/local/bin/fixed"
			if failure == "truncated" {
				raw = raw[:len(raw)/2]
			}
			if failure == "wrong-image" {
				id = Digest([]byte("other"))
			}
			if failure == "missing" {
				target = "missing"
			}
			if _, e := qualificationExtractImageArtifact(context.Background(), bytes.NewReader(raw), id, target); e == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	var big bytes.Buffer
	w := tar.NewWriter(&big)
	_ = w.WriteHeader(&tar.Header{Name: "layer/layer.tar", Size: 3 << 30, Mode: 0400})
	if _, e = qualificationExtractImageArtifact(context.Background(), bytes.NewReader(big.Bytes()), id, "usr/local/bin/fixed"); e == nil {
		t.Fatal("archive bound ignored")
	}
}

// Current Moby classic image-save uses content-addressed blob paths for both
// config JSON and uncompressed tar layers. Metadata/manifest order is not fixed.
func qualificationMobyImageTar(t *testing.T, binary []byte, padding int, order string, mutation string) ([]byte, string) {
	t.Helper()
	var layer bytes.Buffer
	w := tar.NewWriter(&layer)
	if padding > 0 {
		if e := w.WriteHeader(&tar.Header{Name: "non-target-padding", Mode: 0400, Size: int64(padding), Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		if _, e := io.CopyN(w, bytes.NewReader(make([]byte, padding)), int64(padding)); e != nil {
			t.Fatal(e)
		}
	}
	name, kind := "usr/local/bin/fixed", byte(tar.TypeReg)
	switch mutation {
	case "target-symlink":
		kind = tar.TypeSymlink
	case "target-hardlink":
		kind = tar.TypeLink
	case "ancestor-symlink":
		name, kind = "usr/local/bin", tar.TypeSymlink
	case "ancestor-hardlink":
		name, kind = "usr/local/bin", tar.TypeLink
	}
	h := &tar.Header{Name: name, Mode: 0755, Size: int64(len(binary)), Typeflag: kind}
	if kind != tar.TypeReg {
		h.Size, h.Linkname = 0, "/foreign"
	}
	if mutation == "executable-oversize" {
		h.Size = (128 << 20) + 1
	}
	if e := w.WriteHeader(h); e != nil {
		t.Fatal(e)
	}
	if kind == tar.TypeReg && mutation != "executable-oversize" {
		if _, e := w.Write(binary); e != nil {
			t.Fatal(e)
		}
	}
	if mutation != "executable-oversize" {
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
	}
	layerDigest := Digest(layer.Bytes())
	diff := layerDigest
	if mutation == "wrong-diff" {
		diff = Digest([]byte("wrong diff"))
	}
	cfg, _ := json.Marshal(map[string]any{"rootfs": map[string]any{"diff_ids": []string{diff}}})
	id := Digest(cfg)
	configPath := "blobs/sha256/" + strings.TrimPrefix(id, "sha256:")
	layerPath := "blobs/sha256/" + strings.TrimPrefix(layerDigest, "sha256:")
	manifest, _ := json.Marshal([]struct {
		Config string
		Layers []string
	}{{configPath, []string{layerPath}}})
	metadata := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)
	metadataPath := "blobs/sha256/" + strings.TrimPrefix(Digest(metadata), "sha256:")
	parts := map[string][]byte{"config": cfg, "layer": layer.Bytes(), "manifest": manifest, "metadata": metadata}
	names := map[string]string{"config": configPath, "layer": layerPath, "manifest": "manifest.json", "metadata": metadataPath}
	var outer bytes.Buffer
	o := tar.NewWriter(&outer)
	for _, key := range strings.Split(order, ",") {
		if key == "layer" && mutation == "missing-layer" {
			continue
		}
		if e := o.WriteHeader(&tar.Header{Name: names[key], Mode: 0400, Size: int64(len(parts[key])), Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		if _, e := o.Write(parts[key]); e != nil {
			t.Fatal(e)
		}
	}
	if e := o.Close(); e != nil {
		t.Fatal(e)
	}
	return outer.Bytes(), id
}

func TestQualificationImageArchiveCurrentMobyBlobLayout(t *testing.T) {
	binary := bytes.Repeat([]byte("current-moby-binary"), 45000)
	if len(binary) <= 512<<10 {
		t.Fatal("fixture does not cross ordinary JSON cap")
	}
	for _, test := range []struct {
		name, order string
		padding     int
	}{
		{"config-first-manifest-last", "config,metadata,layer,manifest", 0},
		{"layer-first-manifest-last", "layer,metadata,config,manifest", 0},
		{"manifest-first", "manifest,layer,config,metadata", 0},
		{"layer-above-metadata-cap", "layer,metadata,config,manifest", (16 << 20) + 4096},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, id := qualificationMobyImageTar(t, binary, test.padding, test.order, "")
			got, e := qualificationExtractImageArtifact(context.Background(), bytes.NewReader(raw), id, "usr/local/bin/fixed")
			if e != nil || !bytes.Equal(got, binary) {
				t.Fatalf("current Moby blob layout: bytes=%d error=%v", len(got), e)
			}
		})
	}
}

func TestQualificationImageArchiveCurrentMobyRejectsInvalidLayers(t *testing.T) {
	binary := bytes.Repeat([]byte("bound-current-binary"), 40000)
	for _, mutation := range []string{"wrong-diff", "missing-layer", "target-symlink", "target-hardlink", "ancestor-symlink", "ancestor-hardlink", "executable-oversize", "truncated"} {
		t.Run(mutation, func(t *testing.T) {
			raw, id := qualificationMobyImageTar(t, binary, 0, "layer,metadata,config,manifest", mutation)
			if mutation == "truncated" {
				raw = raw[:len(raw)/2]
			}
			if _, e := qualificationExtractImageArtifact(context.Background(), bytes.NewReader(raw), id, "usr/local/bin/fixed"); e == nil {
				t.Fatal("invalid current Moby blob accepted")
			}
		})
	}
	for _, test := range []struct {
		name   string
		size   int64
		prefix []byte
	}{
		{"archive-oversize", (2 << 30) + 1, nil},
		{"metadata-oversize", (16 << 20) + 1, bytes.Repeat([]byte("{"), 512)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			if e := w.WriteHeader(&tar.Header{Name: "blobs/sha256/" + strings.Repeat("a", 64), Mode: 0400, Typeflag: tar.TypeReg, Size: test.size}); e != nil {
				t.Fatal(e)
			}
			if _, e := w.Write(test.prefix); e != nil {
				t.Fatal(e)
			}
			if _, e := qualificationExtractImageArtifact(context.Background(), bytes.NewReader(b.Bytes()), Digest([]byte("image")), "usr/local/bin/fixed"); e == nil {
				t.Fatal("blob payload bound ignored")
			}
		})
	}
}

// Manifest order, not the order in which outer blob entries arrive, determines
// the final executable. Include opaque/deletion markers in real tar layers.
func TestQualificationImageArchiveCurrentMobyLayerOrderAndWhiteouts(t *testing.T) {
	layer := func(name string, raw []byte) []byte {
		t.Helper()
		var b bytes.Buffer
		w := tar.NewWriter(&b)
		if e := w.WriteHeader(&tar.Header{Name: "{tar-not-json}", Typeflag: tar.TypeReg, Mode: 0400, Size: 1}); e != nil {
			t.Fatal(e)
		}
		if _, e := w.Write([]byte("x")); e != nil {
			t.Fatal(e)
		}
		if e := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(raw))}); e != nil {
			t.Fatal(e)
		}
		if _, e := w.Write(raw); e != nil {
			t.Fatal(e)
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
		return b.Bytes()
	}
	base, upper := []byte("base executable"), []byte("upper executable")
	for _, test := range []struct {
		name   string
		layers [][]byte
		want   []byte
	}{
		{"reverse-blob-order", [][]byte{layer("usr/local/bin/fixed", base), layer("usr/local/bin/fixed", upper)}, upper},
		{"target-whiteout", [][]byte{layer("usr/local/bin/fixed", base), layer("usr/local/bin/.wh.fixed", nil)}, nil},
		{"ancestor-whiteout", [][]byte{layer("usr/local/bin/fixed", base), layer("usr/local/.wh.bin", nil)}, nil},
		{"opaque-whiteout", [][]byte{layer("usr/local/bin/fixed", base), layer("usr/local/bin/.wh..wh..opq", nil)}, nil},
		{"restored-after-whiteout", [][]byte{layer("usr/local/bin/fixed", base), layer("usr/local/bin/.wh.fixed", nil), layer("usr/local/bin/fixed", upper)}, upper},
	} {
		t.Run(test.name, func(t *testing.T) {
			diffs, names := []string{}, []string{}
			for _, raw := range test.layers {
				d := Digest(raw)
				diffs = append(diffs, d)
				names = append(names, "blobs/sha256/"+strings.TrimPrefix(d, "sha256:"))
			}
			cfg, _ := json.Marshal(map[string]any{"rootfs": map[string]any{"diff_ids": diffs}})
			id := Digest(cfg)
			configPath := "blobs/sha256/" + strings.TrimPrefix(id, "sha256:")
			manifest, _ := json.Marshal([]struct {
				Config string
				Layers []string
			}{{configPath, names}})
			var b bytes.Buffer
			w := tar.NewWriter(&b)
			put := func(name string, raw []byte) {
				t.Helper()
				if e := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0400, Size: int64(len(raw))}); e != nil {
					t.Fatal(e)
				}
				if _, e := w.Write(raw); e != nil {
					t.Fatal(e)
				}
			}
			for i := len(test.layers) - 1; i >= 0; i-- {
				put(names[i], test.layers[i])
			}
			put(configPath, cfg)
			put("manifest.json", manifest)
			if e := w.Close(); e != nil {
				t.Fatal(e)
			}
			got, e := qualificationExtractImageArtifact(context.Background(), bytes.NewReader(b.Bytes()), id, "usr/local/bin/fixed")
			if test.want == nil {
				if e == nil {
					t.Fatal("whiteout resurrected lower executable")
				}
				return
			}
			if e != nil || !bytes.Equal(got, test.want) {
				t.Fatal(string(got), e)
			}
		})
	}
}

func TestQualificationStagedGeneratorOnlyAddsExactCreateCID(t *testing.T) {
	in := qualificationStaticIntentProjection(qualificationIntentFixture(t))
	created := qualificationCreated{Schema: "switchback.qualification.created.v1", StaticDigest: qualificationStaticIntentDigest(in), Nonce: in.Nonce}
	for _, r := range in.Source.Inputs.Roles {
		if r.Kind == "container" {
			created.Roles = append(created.Roles, qualificationCreatedRole{r.Role, strings.Repeat("b", 64), r.ImageDigest})
		}
	}
	out, e := qualificationApplyCreated(in, created)
	if e != nil || !qualificationSameJSON(in, qualificationStaticIntentProjection(out)) {
		t.Fatal(e)
	}
	created.Roles[0].Image = Digest([]byte("other"))
	if _, e = qualificationApplyCreated(in, created); e == nil {
		t.Fatal("image drift accepted")
	}
}
func TestQualificationPGInitialBaselineRejectsMissingAndHistory(t *testing.T) {
	var p qualificationPGPreparation
	if e := qualificationParsePGInitial([]byte("11111111-1111-1111-1111-111111111111|22222222-2222-2222-2222-222222222222|1\n"), &p); e != nil {
		t.Fatal(e)
	}
	for _, b := range []string{"", "missing schema", "11111111-1111-1111-1111-111111111111|22222222-2222-2222-2222-222222222222|2", "11111111-1111-1111-1111-111111111111|22222222-2222-2222-2222-222222222222|1\nold"} {
		if e := qualificationParsePGInitial([]byte(b), &p); e == nil {
			t.Fatal("history accepted")
		}
	}
}

func TestQualificationImageArchiveUsesNegotiatedOlderAPI(t *testing.T) {
	requests := []string{}
	fake := qualificationFakeRoundTrip(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		header := make(http.Header)
		header.Set("API-Version", "1.47")
		body := ""
		switch req.URL.Path {
		case "/_ping":
		case "/v1.47/info":
			body = `{"ID":"private-fake-engine"}`
		case "/v1.47/images/exact-local/json":
			body = `{"Id":"` + Digest([]byte("exact-image")) + `"}`
		default:
			t.Fatalf("SDK used unsupported fake Engine API: %s", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: req}, nil
	})
	engine, e := client.NewClientWithOpts(client.WithHost(qualificationEngineSocket), client.WithHTTPClient(&http.Client{Transport: qualificationBoundedEngineTransport{base: fake}}), client.WithAPIVersionNegotiation())
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	if _, e = engine.Info(context.Background(), client.InfoOptions{}); e != nil {
		t.Fatal(e)
	}
	image, e := engine.ImageInspect(context.Background(), "exact-local")
	if e != nil || len(requests) != 3 || engine.ClientVersion() != "1.47" {
		t.Fatal(requests, engine.ClientVersion(), e)
	}
	endpoint, e := qualificationImageArchiveURL(engine.ClientVersion(), image.ID)
	if e != nil || !strings.Contains(endpoint, "/v1.47/images/get?") || strings.Contains(endpoint, "1.55") {
		t.Fatal(endpoint, e)
	}
	for _, api := range []string{"", "../1.55", "1.47/containers/json", "v1.47"} {
		if _, e = qualificationImageArchiveURL(api, Digest([]byte("image"))); e == nil {
			t.Fatal("invalid API accepted")
		}
	}
}
func TestQualificationRuntimeBudgetIsNecessaryOnlyAndPreserves80Percent(t *testing.T) {
	core := []CoreDomain{{CPUQuotaMicros: 120000, CPUPeriodMicros: 100000, MemoryMax: 3 << 30}}
	r, e := qualificationRuntimeBudget("engine", 4, 10<<30, core)
	if e != nil || !r.NecessaryCapacityOnly || r.CoreCPUMilli != 1200 || r.WorkerCPUMilli != 2000 {
		t.Fatal(r, e)
	}
	core[0].CPUQuotaMicros = 180000
	if _, e = qualificationRuntimeBudget("engine", 4, 10<<30, core); e == nil {
		t.Fatal("1.8 + 2 exceeds 4 * .8")
	}
	core[0].CPUQuotaMicros = 120000
	if _, e = qualificationRuntimeBudget("engine", 4, 8<<30, core); e == nil {
		t.Fatal("3GiB + 4GiB exceeds 8GiB * .8")
	}
}
func TestQualificationWebSelectsActualEngineLeaderOnly(t *testing.T) {
	r := qualificationRole{Role: "web", Kind: "container", UID: 101}
	if !qualificationCandidateRolePID(r, 100, 100) || qualificationCandidateRolePID(r, 101, 100) || qualificationCandidateRolePID(r, 100, 0) {
		t.Fatal("web master/worker ambiguity")
	}
	r.UID = 0
	if qualificationCandidateRolePID(r, 100, 100) {
		t.Fatal("inherited image UID changed")
	}
}
func TestQualificationPostgresSelectsExactPostmasterOnly(t *testing.T) {
	r := qualificationRole{Role: "postgres", Kind: "container", UID: 999}
	if !qualificationCandidateRolePID(r, 100, 100) || qualificationCandidateRolePID(r, 101, 100) || qualificationCandidateRolePID(r, 100, 0) {
		t.Fatal("postmaster/checkpointer ambiguity")
	}
}
func TestQualificationTypedIntentUsesKitAndCurrentHostMaterial(t *testing.T) {
	in := qualificationStaticIntentProjection(qualificationIntentFixture(t))
	kit := qualificationKit{Schema: qualificationMaterialSchema, Architecture: in.Source.Inputs.Architecture, Toolchain: "go1.26.4", Source: in.Source.Files}
	images := map[string]string{}
	for _, b := range in.Source.Builds {
		if strings.HasPrefix(b.Role, "writer-") {
			continue
		}
		kit.Artifacts = append(kit.Artifacts, qualificationKitArtifact{b.Role, "/opt/switchback/qualification-kit/bin/" + b.Role, b.BinaryDigest})
		images[b.Role] = b.ImageDigest
	}
	h := qualificationHostMaterial{Schema: "switchback.qualification.host_material.v1", Host: in.Source.Inputs.Host, Writers: in.Writers, Config: qualificationSourceFile{Path: "host-config/lima.yaml", Type: "regular", Mode: 0600, Bytes: 10, Digest: Digest([]byte("current-config"))}, Toolchain: "go1.27.1"}
	for i := range h.Host.Writers {
		for _, a := range h.Writers {
			if a.ID == h.Host.Writers[i].ID {
				h.Host.Writers[i].ArtifactDigest = qualificationDigest(a)
			}
		}
	}
	out, e := qualificationBuildIntent(kit, h, in.Trust, in.Source.Inputs.Roles, nil, images, in.Nonce)
	if e != nil || qualificationValidateIntentInput(out) != nil {
		t.Fatal(e)
	}
	if out.Trust.ManifestDigest != "" || out.Inventory.SourceManifestDigest != "" {
		t.Fatal("runtime generator fabricated final authority")
	}
	before := qualificationStaticIntentDigest(out)
	h.Writers[0].ExecutableDigest = Digest([]byte("changed actual writer"))
	for i := range h.Host.Writers {
		if h.Host.Writers[i].ID == h.Writers[0].ID {
			h.Host.Writers[i].ArtifactDigest = qualificationDigest(h.Writers[0])
		}
	}
	changed, e := qualificationBuildIntent(kit, h, in.Trust, in.Source.Inputs.Roles, nil, images, in.Nonce)
	if e != nil || qualificationStaticIntentDigest(changed) == before {
		t.Fatal("current writer material omitted", e)
	}
}
func TestQualificationCoreSecretMarkersBindActualValuesWithoutPlaintext(t *testing.T) {
	password := strings.Repeat("a", 64)
	url := qualificationDatabaseURL(password)
	r := qualificationRole{Role: "backend", Environment: []qualificationEnv{{qualificationDatabaseMarker, Digest([]byte(url))}}}
	actual := []string{qualificationDatabaseMarker + "=" + Digest([]byte(url)), "SWITCHBACK_DATABASE_URL=" + url}
	if e := qualificationCoreEnvironment(r, password, actual); e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]string{{actual[0], "SWITCHBACK_DATABASE_URL=" + strings.Replace(url, "@postgres:", "@other:", 1)}, {actual[0], "SWITCHBACK_DATABASE_URL=" + Digest([]byte(url))}, {actual[0], actual[1], actual[1]}} {
		if e := qualificationCoreEnvironment(r, password, bad); e == nil {
			t.Fatal("marker accepted without exact actual secret/target")
		}
	}
	encoded := qualificationJSON(r)
	if bytes.Contains(encoded, []byte(password)) || bytes.Contains(encoded, []byte(url)) {
		t.Fatal("plaintext escaped into public source")
	}
	p := qualificationRole{Role: "postgres", Environment: []qualificationEnv{{qualificationPasswordMarker, Digest([]byte(password))}}}
	env := []string{qualificationPasswordMarker + "=" + Digest([]byte(password)), "POSTGRES_PASSWORD=" + password, "POSTGRES_USER=switchback", "POSTGRES_DB=postgres", "PGDATA=/var/lib/postgresql/data/pgdata", "POSTGRES_INITDB_WALDIR=/var/lib/postgresql/wal"}
	if e := qualificationCoreEnvironment(p, password, env); e != nil {
		t.Fatal(e)
	}
	env[1] = "POSTGRES_PASSWORD=changed"
	if e := qualificationCoreEnvironment(p, password, env); e == nil {
		t.Fatal("PG actual secret drift accepted")
	}
}
func TestQualificationCoreExposedPortsEqualFixedLoopbackBindings(t *testing.T) {
	for _, role := range []string{"backend", "web", "postgres", "worker"} {
		ports, bindings := qualificationCorePorts(role)
		if len(ports) != len(bindings) {
			t.Fatal(role)
		}
		for p, b := range bindings {
			if _, ok := ports[p]; !ok || len(b) != 1 || b[0].HostIP.String() != "127.0.0.1" || p.String() != "8080/tcp" {
				t.Fatal(role, b)
			}
			want := "48081"
			if role == "web" {
				want = "44175"
			}
			if b[0].HostPort != want {
				t.Fatal(role)
			}
		}
		if (role == "postgres" || role == "worker") && len(ports) != 0 {
			t.Fatal("unexpected published port")
		}
	}
}
