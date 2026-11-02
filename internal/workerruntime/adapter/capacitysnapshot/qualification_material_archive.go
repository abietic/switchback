package capacitysnapshot

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

func qualificationExtractImageArtifact(ctx context.Context, r io.Reader, id, target string) ([]byte, error) {
	tr := tar.NewReader(r)
	layers := map[string][]byte{}
	configs := map[string]string{}
	configBytes := map[string][]byte{}
	layerHashes := map[string]string{}
	type entry struct {
		Config string
		Layers []string
	}
	var manifest []entry
	total := int64(0)
	entries := 0
	for {
		if e := qualificationContext(ctx); e != nil {
			return nil, e
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		entries++
		if entries > 4096 || h.Size < 0 || h.Size > (2<<30)-total {
			return nil, errQualification
		}
		total += h.Size
		if h.Name == "" || strings.HasPrefix(h.Name, "/") || path.Clean(h.Name) != strings.TrimSuffix(h.Name, "/") || strings.HasPrefix(h.Name, "../") {
			return nil, errQualification
		}
		if h.Typeflag == tar.TypeDir {
			if h.Size != 0 {
				return nil, errQualification
			}
			continue
		}
		if h.Name == "manifest.json" {
			if h.Typeflag != tar.TypeReg || h.Size > 65536 {
				return nil, errQualification
			}
			b, e := io.ReadAll(tr)
			if e != nil || json.Unmarshal(b, &manifest) != nil {
				return nil, errQualification
			}
			continue
		}
		blob := strings.HasPrefix(h.Name, "blobs/sha256/")
		if blob && !containerIDPattern.MatchString(strings.TrimPrefix(h.Name, "blobs/sha256/")) {
			return nil, errQualification
		}
		if !blob && !strings.HasSuffix(h.Name, ".json") && !strings.HasSuffix(h.Name, "/layer.tar") {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, errQualification
		}
		// The manifest can follow all blobs. Inspect only one tar block to
		// distinguish JSON metadata from tar content; never retain a whole layer.
		// Valid tar checksums take precedence, including names beginning with '{'.
		reader := bufio.NewReaderSize(tr, 512)
		prefix, e := reader.Peek(512)
		if e != nil && e != io.EOF {
			return nil, e
		}
		text := bytes.TrimSpace(prefix)
		metadata := strings.HasSuffix(h.Name, ".json") || (blob && len(text) > 0 && (text[0] == '{' || text[0] == '[') && !qualificationImageTarHeader(prefix))
		if metadata {
			if h.Size > 16<<20 {
				return nil, errQualification
			}
			b, e := io.ReadAll(reader)
			if e != nil || !json.Valid(b) {
				return nil, errQualification
			}
			digest := Digest(b)
			if blob && h.Name != "blobs/sha256/"+strings.TrimPrefix(digest, "sha256:") {
				return nil, fmt.Errorf("image blob identity mismatch")
			}
			configs[h.Name], configBytes[h.Name] = digest, b
			continue
		}
		digest, found, changed, e := qualificationExtractImageLayer(ctx, reader, target)
		if e != nil {
			return nil, e
		}
		if blob && h.Name != "blobs/sha256/"+strings.TrimPrefix(digest, "sha256:") {
			return nil, fmt.Errorf("image blob identity mismatch")
		}
		layerHashes[h.Name] = digest
		if changed {
			layers[h.Name] = found
		}
	}
	if len(manifest) != 1 || configs[manifest[0].Config] != id {
		return nil, fmt.Errorf("image archive config identity mismatch")
	}
	var config struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if json.Unmarshal(configBytes[manifest[0].Config], &config) != nil || len(config.RootFS.DiffIDs) != len(manifest[0].Layers) {
		return nil, errQualification
	}
	for i, layer := range manifest[0].Layers {
		if !digestPattern.MatchString(config.RootFS.DiffIDs[i]) || layerHashes[layer] != config.RootFS.DiffIDs[i] {
			return nil, fmt.Errorf("image layer identity mismatch")
		}
	}
	var out []byte
	for _, layer := range manifest[0].Layers {
		if b, ok := layers[layer]; ok {
			out = b
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fixed image executable missing")
	}
	return out, nil
}

// This is only content classification, not tar acceptance. The real tar reader
// below validates all headers and payloads. Both tar checksum variants match the
// standard reader; no filename suffix or outer archive ordering is required.
func qualificationImageTarHeader(block []byte) bool {
	if len(block) != 512 {
		return false
	}
	want, e := strconv.ParseUint(strings.Trim(string(block[148:156]), " \x00"), 8, 64)
	if e != nil {
		return false
	}
	unsigned, signed := int64(0), int64(0)
	for i, b := range block {
		if i >= 148 && i < 156 {
			unsigned += 32
			signed += 32
		} else {
			unsigned += int64(b)
			signed += int64(int8(b))
		}
	}
	return want == uint64(unsigned) || (signed >= 0 && want == uint64(signed))
}

func qualificationExtractImageLayer(ctx context.Context, r io.Reader, target string) (string, []byte, bool, error) {
	hash := sha256.New()
	tee := io.TeeReader(r, hash)
	inner := tar.NewReader(tee)
	var found []byte
	changed, count := false, 0
	for {
		if e := qualificationContext(ctx); e != nil {
			return "", nil, false, e
		}
		f, e := inner.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", nil, false, e
		}
		count++
		if count > 1000000 {
			return "", nil, false, errQualification
		}
		n := strings.TrimPrefix(path.Clean(f.Name), "./")
		if strings.HasPrefix(n, "/") || n == ".." || strings.HasPrefix(n, "../") || f.Size < 0 {
			return "", nil, false, errQualification
		}
		if n == target {
			if f.Typeflag != tar.TypeReg || f.Size <= 0 || f.Size > 128<<20 {
				return "", nil, false, errQualification
			}
			found, e = io.ReadAll(inner)
			if e != nil {
				return "", nil, false, e
			}
			changed = true
		}
		for p := target; p != "."; p = path.Dir(p) {
			if n == path.Join(path.Dir(p), ".wh."+path.Base(p)) || n == path.Join(path.Dir(p), ".wh..wh..opq") {
				found, changed = nil, true
			}
			if n == p && n != target && (f.Typeflag == tar.TypeSymlink || f.Typeflag == tar.TypeLink) {
				return "", nil, false, errQualification
			}
		}
	}
	if _, e := io.Copy(io.Discard, tee); e != nil {
		return "", nil, false, e
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), found, changed, nil
}

func qualificationImageArchiveURL(api, id string) (string, error) {
	if !regexp.MustCompile(`^1\.[0-9]{1,3}$`).MatchString(api) || !digestPattern.MatchString(id) {
		return "", errQualification
	}
	return "http://engine/v" + api + "/images/get?names=" + url.QueryEscape(id), nil
}
