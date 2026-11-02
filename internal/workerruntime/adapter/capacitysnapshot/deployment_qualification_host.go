package capacitysnapshot

import (
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

var qualificationHostChecks = []string{"diskutil-info-before", "apfs-list-before", "diskutil-info-after", "apfs-list-after", "writer-paths"}
var qualificationWriterIDs = []string{"system-image", "data-image", "lima-state", "lima-log", "vz-helper", "cache", "temp", "conversion", "qualification", "snapshot", "clone"}

func validateQualificationHostTarget(h qualificationHostTarget) error {
	if !qualificationUUIDPattern.MatchString(h.VolumeUUID) || !qualificationUUIDPattern.MatchString(h.ContainerUUID) || !qualificationUUIDPattern.MatchString(h.PhysicalStoreUUID) || h.PhysicalStore == "" || !absolute(h.Mount) || h.QuotaBytes != 120259084288 || h.ReserveBytes != 120259084288 || len(h.Writers) != len(qualificationWriterIDs) {
		return errQualification
	}
	seen := map[string]bool{}
	for _, w := range h.Writers {
		known := false
		for _, id := range qualificationWriterIDs {
			if w.ID == id {
				known = true
			}
		}
		if !known || seen[w.ID] || !digestPattern.MatchString(w.ArtifactDigest) {
			return errQualification
		}
		seen[w.ID] = true
		if w.Policy == "forbidden_during_frozen_deployment" {
			if w.ID != "conversion" && w.ID != "snapshot" && w.ID != "clone" {
				return errQualification
			}
			if w.Path != "" {
				return errQualification
			}
		} else if w.Policy != "volume_mapped" || !absolute(w.Path) || !strings.HasPrefix(w.Path, h.Mount+"/") {
			return errQualification
		}
	}
	return nil
}

// Apple's XML plist has typed dictionaries/arrays. Unknown OS keys are retained
// but never treated as evidence; duplicate dictionary keys and unsupported XML
// constructs fail. No external entity or attachment is resolved.
func qualificationPlist(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, errQualification
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	nodes := 0
	var value func(xml.StartElement, int) (any, error)
	value = func(start xml.StartElement, depth int) (any, error) {
		nodes++
		if nodes > 20000 || depth > 24 {
			return nil, errQualification
		}
		switch start.Name.Local {
		case "dict":
			out := map[string]any{}
			for {
				tok, e := d.Token()
				if e != nil {
					return nil, errQualification
				}
				switch x := tok.(type) {
				case xml.CharData:
					if strings.TrimSpace(string(x)) != "" {
						return nil, errQualification
					}
				case xml.EndElement:
					if x.Name.Local != "dict" {
						return nil, errQualification
					}
					return out, nil
				case xml.StartElement:
					if x.Name.Local != "key" {
						return nil, errQualification
					}
					var key string
					if d.DecodeElement(&key, &x) != nil || key == "" {
						return nil, errQualification
					}
					if _, ok := out[key]; ok {
						return nil, errQualification
					}
					for {
						t, e := d.Token()
						if e != nil {
							return nil, errQualification
						}
						if c, ok := t.(xml.CharData); ok && strings.TrimSpace(string(c)) == "" {
							continue
						}
						s, ok := t.(xml.StartElement)
						if !ok {
							return nil, errQualification
						}
						v, e := value(s, depth+1)
						if e != nil {
							return nil, e
						}
						out[key] = v
						break
					}
				default:
					return nil, errQualification
				}
			}
		case "array":
			out := []any{}
			for {
				tok, e := d.Token()
				if e != nil {
					return nil, errQualification
				}
				switch x := tok.(type) {
				case xml.CharData:
					if strings.TrimSpace(string(x)) != "" {
						return nil, errQualification
					}
				case xml.EndElement:
					if x.Name.Local != "array" {
						return nil, errQualification
					}
					return out, nil
				case xml.StartElement:
					v, e := value(x, depth+1)
					if e != nil {
						return nil, e
					}
					out = append(out, v)
				default:
					return nil, errQualification
				}
			}
		case "string", "integer", "true", "false", "data", "date":
			var s string
			if d.DecodeElement(&s, &start) != nil {
				return nil, errQualification
			}
			if start.Name.Local == "integer" {
				v, e := strconv.ParseUint(s, 10, 64)
				if e != nil {
					return nil, errQualification
				}
				return v, nil
			}
			if start.Name.Local == "true" || start.Name.Local == "false" {
				if s != "" {
					return nil, errQualification
				}
				return start.Name.Local == "true", nil
			}
			return s, nil
		}
		return nil, errQualification
	}
	for {
		tok, e := d.Token()
		if e != nil {
			return nil, errQualification
		}
		if s, ok := tok.(xml.StartElement); ok {
			if s.Name.Local != "plist" {
				return nil, errQualification
			}
			break
		}
	}
	var result map[string]any
	for {
		tok, e := d.Token()
		if e != nil {
			return nil, errQualification
		}
		if c, ok := tok.(xml.CharData); ok && strings.TrimSpace(string(c)) == "" {
			continue
		}
		s, ok := tok.(xml.StartElement)
		if !ok || s.Name.Local != "dict" {
			return nil, errQualification
		}
		v, e := value(s, 0)
		if e != nil {
			return nil, e
		}
		result = v.(map[string]any)
		break
	}
	closed := false
	for {
		tok, e := d.Token()
		if e == io.EOF {
			if closed {
				return result, nil
			}
			return nil, errQualification
		}
		if e != nil {
			return nil, errQualification
		}
		switch x := tok.(type) {
		case xml.CharData:
			if strings.TrimSpace(string(x)) != "" {
				return nil, errQualification
			}
		case xml.EndElement:
			if closed || x.Name.Local != "plist" {
				return nil, errQualification
			}
			closed = true
		default:
			return nil, errQualification
		}
	}
}
func plistText(m map[string]any, k string) string           { v, _ := m[k].(string); return v }
func plistNumber(m map[string]any, k string) (uint64, bool) { v, ok := m[k].(uint64); return v, ok }
func plistArray(m map[string]any, k string) []any           { v, _ := m[k].([]any); return v }
func validateQualificationHost(r qualificationHostReceipt, h qualificationHostTarget) error {
	checks, e := qualificationChecks(r.Checks, qualificationHostChecks, r.Window)
	if e != nil {
		return e
	}
	var identity string
	for _, phase := range []string{"before", "after"} {
		info, e := qualificationPlist(checks["diskutil-info-"+phase])
		if e != nil {
			return e
		}
		list, e := qualificationPlist(checks["apfs-list-"+phase])
		if e != nil {
			return e
		}
		if plistText(info, "VolumeUUID") != h.VolumeUUID && plistText(info, "DiskUUID") != h.VolumeUUID {
			return errQualification
		}
		if plistText(info, "MountPoint") != h.Mount || plistText(info, "FilesystemType") != "apfs" {
			return errQualification
		}
		if _, ok := plistNumber(info, "FreeSpace"); !ok {
			return errQualification
		}
		if _, ok := plistNumber(info, "CapacityInUse"); !ok {
			return errQualification
		}
		stores := plistArray(info, "APFSPhysicalStores")
		if len(stores) != 1 {
			return errQualification
		}
		store, ok := stores[0].(map[string]any)
		if !ok || plistText(store, "APFSPhysicalStore") != h.PhysicalStore {
			return errQualification
		}
		matched := 0
		for _, entry := range plistArray(list, "Containers") {
			container, ok := entry.(map[string]any)
			if !ok {
				return errQualification
			}
			if plistText(container, "APFSContainerUUID") != h.ContainerUUID {
				continue
			}
			matched++
			if plistText(container, "ContainerReference") != plistText(info, "APFSContainerReference") {
				return errQualification
			}
			physical := plistArray(container, "PhysicalStores")
			if len(physical) != 1 {
				return errQualification
			}
			p, ok := physical[0].(map[string]any)
			if !ok || plistText(p, "DeviceIdentifier") != h.PhysicalStore || plistText(p, "DiskUUID") != h.PhysicalStoreUUID {
				return errQualification
			}
			count := 0
			for _, item := range plistArray(container, "Volumes") {
				v, ok := item.(map[string]any)
				if !ok {
					return errQualification
				}
				if plistText(v, "APFSVolumeUUID") != h.VolumeUUID {
					continue
				}
				count++
				quota, qok := plistNumber(v, "CapacityQuota")
				reserve, rok := plistNumber(v, "CapacityReserve")
				if !qok || !rok || quota != h.QuotaBytes || reserve != h.ReserveBytes || plistText(v, "DeviceIdentifier") != plistText(info, "DeviceIdentifier") {
					return errQualification
				}
				if _, ok := plistNumber(v, "CapacityInUse"); !ok {
					return errQualification
				}
			}
			if count != 1 {
				return errQualification
			}
		}
		if matched != 1 {
			return errQualification
		}
		current := plistText(info, "DeviceIdentifier") + ":" + plistText(info, "APFSContainerReference")
		if phase == "after" && current != identity {
			return errQualification
		}
		identity = current
	}
	var paths qualificationHostPaths
	if strictJSON(checks["writer-paths"], &paths) != nil || len(paths.Writers) != len(h.Writers) || paths.Mount != h.Mount || paths.VolumeUUID != h.VolumeUUID || paths.StatDevice == 0 || paths.StatInode == 0 {
		return errQualification
	}
	seen := map[string]bool{}
	device := paths.StatDevice
	for _, w := range paths.Writers {
		if seen[w.ID] {
			return errQualification
		}
		seen[w.ID] = true
		found := false
		for _, expected := range h.Writers {
			if w.ID != expected.ID {
				continue
			}
			found = true
			if w.Path != expected.Path || w.ArtifactDigest != expected.ArtifactDigest || w.Policy != expected.Policy {
				return errQualification
			}
			if w.Policy == "volume_mapped" {
				if w.VolumeUUID != h.VolumeUUID || w.StatDevice == 0 || w.StatInode == 0 {
					return errQualification
				}
				if device != 0 && device != w.StatDevice {
					return errQualification
				}
				device = w.StatDevice
			} else if w.VolumeUUID != "" || w.StatDevice != 0 || w.StatInode != 0 || w.AllocatedBlocks512 != 0 {
				return errQualification
			}
		}
		if !found {
			return errQualification
		}
	}
	return nil
}
