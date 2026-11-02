package capacityprobe

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const createdLayerMaxResponse = 1 << 20
const createdLayerMaxDepth = 32
const createdLayerDockerRoot = "/mnt/lima-sbquota/docker"

var createdLayerSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
var createdLayerImage = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var errCreatedLayerJSON = errors.New("created_layer_json_invalid")

// Validate the full JSON stream before selecting fields. Duplicate keys, including
// case aliases, are rejected even in ignored subtrees; no convenient value wins.
func createdLayerObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) > createdLayerMaxResponse || !utf8.Valid(raw) {
		return nil, errCreatedLayerJSON
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := createdLayerJSONValue(d, 0); err != nil {
		return nil, errCreatedLayerJSON
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errCreatedLayerJSON
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, errCreatedLayerJSON
	}
	return out, nil
}

func createdLayerJSONValue(d *json.Decoder, depth int) error {
	if depth > createdLayerMaxDepth {
		return errCreatedLayerJSON
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok {
				return errCreatedLayerJSON
			}
			folded := strings.ToLower(key)
			if seen[folded] {
				return errCreatedLayerJSON
			}
			seen[folded] = true
			if err := createdLayerJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		t, err = d.Token()
		if err != nil || t != json.Delim('}') {
			return errCreatedLayerJSON
		}
	case '[':
		for d.More() {
			if err := createdLayerJSONValue(d, depth+1); err != nil {
				return err
			}
		}
		t, err = d.Token()
		if err != nil || t != json.Delim(']') {
			return errCreatedLayerJSON
		}
	default:
		return errCreatedLayerJSON
	}
	return nil
}

func createdLayerField(m map[string]json.RawMessage, key string, out any) error {
	raw, ok := m[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errCreatedLayerJSON
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errCreatedLayerJSON
	}
	return nil
}

func createdLayerEngineVersion(raw []byte) (string, string, error) {
	m, err := createdLayerObject(raw)
	if err != nil {
		return "", "", err
	}
	var api, server string
	if createdLayerField(m, "ApiVersion", &api) != nil || createdLayerField(m, "Version", &server) != nil || api != "1.56" || !version.MatchString(server) || len(server) > 64 {
		return "", "", errCreatedLayerJSON
	}
	return api, server, nil
}

func createdLayerEngineInfo(raw []byte, api, server string) (*CreatedLayerEngine, error) {
	m, err := createdLayerObject(raw)
	if err != nil {
		return nil, err
	}
	e := &CreatedLayerEngine{APIVersion: api, Source: "engine_get_v1.56_version_info"}
	if createdLayerField(m, "ID", &e.ID) != nil || createdLayerField(m, "ServerVersion", &e.Version) != nil || createdLayerField(m, "DockerRootDir", &e.DockerRoot) != nil || createdLayerField(m, "Driver", &e.Driver) != nil || !engineID.MatchString(e.ID) || e.Version != server || e.DockerRoot != createdLayerDockerRoot || e.Driver != "overlay2" {
		return nil, errCreatedLayerJSON
	}
	return e, nil
}

func createdLayerDecodeInspect(raw []byte, cid string) (*CreatedLayerInspect, error) {
	m, err := createdLayerObject(raw)
	if err != nil {
		return nil, err
	}
	c := &CreatedLayerInspect{Source: "engine_get_v1.56_container_inspect"}
	if createdLayerField(m, "Id", &c.ID) != nil || c.ID != cid {
		return nil, errCreatedLayerJSON
	}
	if _, ok := m["Image"]; ok {
		if createdLayerField(m, "Image", &c.Image) != nil || !createdLayerImage.MatchString(c.Image) {
			return nil, errCreatedLayerJSON
		}
	}
	var state, driver map[string]json.RawMessage
	if createdLayerField(m, "State", &state) != nil || createdLayerField(m, "GraphDriver", &driver) != nil {
		return nil, errCreatedLayerJSON
	}
	if createdLayerField(state, "Status", &c.Status) != nil || createdLayerField(state, "Pid", &c.PID) != nil || createdLayerField(state, "Running", &c.Running) != nil || createdLayerField(state, "Paused", &c.Paused) != nil || createdLayerField(state, "Restarting", &c.Restarting) != nil || createdLayerField(state, "Dead", &c.Dead) != nil || createdLayerField(state, "StartedAt", &c.StartedAt) != nil {
		return nil, errCreatedLayerJSON
	}
	if c.Status != "created" || c.PID != 0 || c.Running || c.Paused || c.Restarting || c.Dead || c.StartedAt != "0001-01-01T00:00:00Z" {
		return nil, errCreatedLayerJSON
	}
	var data map[string]json.RawMessage
	if createdLayerField(driver, "Name", &c.Driver) != nil || c.Driver != "overlay2" || createdLayerField(driver, "Data", &data) != nil || createdLayerField(data, "UpperDir", &c.UpperDir) != nil {
		return nil, errCreatedLayerJSON
	}
	prefix := createdLayerDockerRoot + "/overlay2/"
	if !strings.HasPrefix(c.UpperDir, prefix) || !strings.HasSuffix(c.UpperDir, "/diff") || path.Clean(c.UpperDir) != c.UpperDir {
		return nil, errCreatedLayerJSON
	}
	segment := strings.TrimSuffix(strings.TrimPrefix(c.UpperDir, prefix), "/diff")
	if !createdLayerSegment.MatchString(segment) {
		return nil, errCreatedLayerJSON
	}
	c.ParentDir = path.Dir(c.UpperDir)
	return c, nil
}
