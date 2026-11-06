package capacityprobe

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Decode tokens first, including discarded extension objects. Required properties
// are selected by exact spelling from these maps; encoding/json's case-folded
// struct matching and last-write-wins duplicate semantics are never used.
func poolJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > MaxResponse {
		return nil, ErrPoolInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, ErrPoolInvalid
		}
		t, e := d.Token()
		if e != nil {
			return nil, ErrPoolInvalid
		}
		switch x := t.(type) {
		case json.Delim:
			switch x {
			case '{':
				m := map[string]any{}
				for d.More() {
					k, e := d.Token()
					s, ok := k.(string)
					if e != nil || !ok {
						return nil, ErrPoolInvalid
					}
					if _, ok = m[s]; ok {
						return nil, ErrPoolInvalid
					}
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					m[s] = v
				}
				if t, e = d.Token(); e != nil || t != json.Delim('}') {
					return nil, ErrPoolInvalid
				}
				return m, nil
			case '[':
				a := []any{}
				for d.More() {
					v, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					a = append(a, v)
				}
				if t, e = d.Token(); e != nil || t != json.Delim(']') {
					return nil, ErrPoolInvalid
				}
				return a, nil
			default:
				return nil, ErrPoolInvalid
			}
		default:
			return t, nil
		}
	}
	v, e := read(0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrPoolInvalid
	}
	return v, nil
}
func poolObject(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrPoolInvalid
	}
	return m, nil
}
func poolString(m map[string]any, k string) (string, error) {
	s, ok := m[k].(string)
	if !ok || len(s) > 4096 || strings.ContainsAny(s, "\x00\r\n") {
		return "", ErrPoolInvalid
	}
	return s, nil
}
func poolBool(m map[string]any, k string) (bool, error) {
	b, ok := m[k].(bool)
	if !ok {
		return false, ErrPoolInvalid
	}
	return b, nil
}
func poolNumber(m map[string]any, k string, positive bool) (uint64, error) {
	n, ok := m[k].(json.Number)
	if !ok {
		return 0, ErrPoolInvalid
	}
	s := string(n)
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, ErrPoolInvalid
		}
	}
	v, e := strconv.ParseInt(s, 10, 64)
	if e != nil || v < 0 || (positive && v == 0) {
		return 0, ErrPoolInvalid
	}
	return uint64(v), nil
}
func poolDecodeVersion(raw []byte) (api, ver string, e error) {
	v, e := poolJSON(raw)
	if e != nil {
		return "", "", e
	}
	m, e := poolObject(v)
	if e != nil {
		return "", "", e
	}
	api, e = poolString(m, "ApiVersion")
	if e != nil {
		return "", "", e
	}
	ver, e = poolString(m, "Version")
	if e != nil || api != "1.56" || !version.MatchString(ver) || len(ver) > 64 {
		return "", "", ErrPoolInvalid
	}
	return
}
func poolDecodeInfo(raw []byte) (o PoolEngineReadback, e error) {
	v, e := poolJSON(raw)
	if e != nil {
		return o, e
	}
	m, e := poolObject(v)
	if e != nil {
		return o, e
	}
	if o.ID, e = poolString(m, "ID"); e != nil {
		return o, e
	}
	if o.Version, e = poolString(m, "ServerVersion"); e != nil {
		return o, e
	}
	if o.DockerRoot, e = poolString(m, "DockerRootDir"); e != nil {
		return o, e
	}
	if o.Driver, e = poolString(m, "Driver"); e != nil {
		return o, e
	}
	if o.CPUCount, e = poolNumber(m, "NCPU", true); e != nil {
		return o, e
	}
	if o.MemoryBytes, e = poolNumber(m, "MemTotal", true); e != nil {
		return o, e
	}
	if !engineID.MatchString(o.ID) || !version.MatchString(o.Version) || len(o.Version) > 64 || o.DockerRoot != Root+"/docker" || o.Driver != "overlay2" {
		return o, ErrPoolInvalid
	}
	return
}
func poolDecodeList(raw []byte) (ids []string, e error) {
	v, e := poolJSON(raw)
	if e != nil {
		return nil, e
	}
	a, ok := v.([]any)
	if !ok || len(a) > 32 {
		return nil, ErrPoolInvalid
	}
	seen := map[string]bool{}
	ids = []string{}
	for _, v := range a {
		m, e := poolObject(v)
		if e != nil {
			return nil, e
		}
		s, e := poolString(m, "Id")
		if e != nil || !hexID.MatchString(s) || seen[s] {
			return nil, ErrPoolInvalid
		}
		seen[s] = true
		ids = append(ids, s)
	}
	sort.Strings(ids)
	return
}
func poolDecodeInspect(raw []byte) (o PoolContainerInspect, e error) {
	v, e := poolJSON(raw)
	if e != nil {
		return o, e
	}
	m, e := poolObject(v)
	if e != nil {
		return o, e
	}
	if o.ID, e = poolString(m, "Id"); e != nil {
		return o, e
	}
	if o.ImageDigest, e = poolString(m, "Image"); e != nil {
		return o, e
	}
	if !hexID.MatchString(o.ID) || !createdLayerImage.MatchString(o.ImageDigest) {
		return o, ErrPoolInvalid
	}
	state, e := poolObject(m["State"])
	if e != nil {
		return o, e
	}
	if o.Status, e = poolString(state, "Status"); e != nil {
		return o, e
	}
	valid := false
	for _, s := range []string{"created", "running", "paused", "restarting", "removing", "exited", "dead"} {
		valid = valid || o.Status == s
	}
	if !valid {
		return o, ErrPoolInvalid
	}
	if o.PID, e = poolNumber(state, "Pid", false); e != nil {
		return o, e
	}
	if o.Running, e = poolBool(state, "Running"); e != nil {
		return o, e
	}
	if o.Paused, e = poolBool(state, "Paused"); e != nil {
		return o, e
	}
	if o.Restarting, e = poolBool(state, "Restarting"); e != nil {
		return o, e
	}
	if o.Dead, e = poolBool(state, "Dead"); e != nil {
		return o, e
	}
	started, e := poolString(state, "StartedAt")
	if e != nil {
		return o, e
	}
	o.StartedAt, e = time.Parse(time.RFC3339Nano, started)
	if e != nil {
		return o, ErrPoolInvalid
	}
	cfg, e := poolObject(m["Config"])
	if e != nil {
		return o, e
	}
	labels, e := poolObject(cfg["Labels"])
	if e != nil || len(labels) > 64 {
		return o, ErrPoolInvalid
	}
	o.Labels = map[string]string{}
	for k, v := range labels {
		s, ok := v.(string)
		if !ok || k == "" || len(k) > 512 || len(s) > 512 || strings.ContainsAny(k+s, "\x00\r\n") {
			return o, ErrPoolInvalid
		}
		o.Labels[k] = s
	}
	hc, e := poolObject(m["HostConfig"])
	if e != nil {
		return o, e
	}
	if o.NanoCPUs, e = poolNumber(hc, "NanoCpus", false); e != nil {
		return o, e
	}
	if o.MemoryBytes, e = poolNumber(hc, "Memory", false); e != nil {
		return o, e
	}
	if o.MemorySwapBytes, e = poolNumber(hc, "MemorySwap", false); e != nil {
		return o, e
	}
	if o.PIDsMax, e = poolNumber(hc, "PidsLimit", false); e != nil {
		return o, e
	}
	if o.CgroupParent, e = poolString(hc, "CgroupParent"); e != nil {
		return o, e
	}
	if o.NetworkMode, e = poolString(hc, "NetworkMode"); e != nil {
		return o, e
	}
	if o.Privileged, e = poolBool(hc, "Privileged"); e != nil {
		return o, e
	}
	gd, e := poolObject(m["GraphDriver"])
	if e != nil {
		return o, e
	}
	if o.GraphDriver, e = poolString(gd, "Name"); e != nil {
		return o, e
	}
	data, e := poolObject(gd["Data"])
	if e != nil {
		return o, e
	}
	if o.UpperDir, e = poolString(data, "UpperDir"); e != nil {
		return o, e
	}
	return
}
