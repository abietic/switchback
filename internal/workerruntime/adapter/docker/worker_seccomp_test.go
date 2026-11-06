package docker

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func decodeSeccompObject(t *testing.T, data []byte) map[string]json.RawMessage {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func seccompSemanticValue(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestWorkerSeccompPreservesEntireBaseline(t *testing.T) {
	output, err := buildWorkerSeccomp(embeddedWorkerSeccompBaseline)
	if err != nil {
		t.Fatal(err)
	}
	before := decodeSeccompObject(t, embeddedWorkerSeccompBaseline)
	after := decodeSeccompObject(t, []byte(output))
	var beforeRules, afterRules []map[string]json.RawMessage
	if err := json.Unmarshal(before["syscalls"], &beforeRules); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after["syscalls"], &afterRules); err != nil {
		t.Fatal(err)
	}
	delete(before, "syscalls")
	delete(after, "syscalls")
	beforeFields, _ := json.Marshal(before)
	afterFields, _ := json.Marshal(after)
	if !reflect.DeepEqual(seccompSemanticValue(t, beforeFields), seccompSemanticValue(t, afterFields)) {
		t.Fatal("top-level default/errno/archMap/extensions changed")
	}
	if len(afterRules) <= len(beforeRules) {
		t.Fatal("missing prefix rules")
	}
	removed := 0
	for i, rule := range beforeRules {
		var names []string
		if err := json.Unmarshal(rule["names"], &names); err != nil {
			t.Fatal(err)
		}
		peers := make([]string, 0, len(names))
		for _, name := range names {
			if name == "ioctl" {
				removed++
				continue
			}
			peers = append(peers, name)
		}
		filtered, err := json.Marshal(peers)
		if err != nil {
			t.Fatal(err)
		}
		rule["names"] = filtered
		want, err := json.Marshal(rule)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(afterRules[i])
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(seccompSemanticValue(t, want), seccompSemanticValue(t, got)) {
			t.Fatalf("baseline rule %d changed beyond removing ioctl (including ordering/conditions/errno/extensions)", i)
		}
	}
	if removed != 1 {
		t.Fatalf("removed %d ioctl names", removed)
	}
	for i, rule := range afterRules[len(beforeRules):] {
		if len(rule) != 3 {
			t.Fatalf("appended rule %d has unexpected fields", i)
		}
		if string(rule["names"]) != `["ioctl"]` || string(rule["action"]) != `"SCMP_ACT_ALLOW"` {
			t.Fatalf("appended rule %d is not ioctl ALLOW", i)
		}
	}
}

func TestWorkerSeccompIntervalsCoverExactlyAllowedLow32(t *testing.T) {
	output, err := buildWorkerSeccomp(embeddedWorkerSeccompBaseline)
	if err != nil {
		t.Fatal(err)
	}
	var profile struct {
		Syscalls []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
			Args   []struct {
				Index    uint   `json:"index"`
				Value    uint64 `json:"value"`
				ValueTwo uint64 `json:"valueTwo"`
				Op       string `json:"op"`
			} `json:"args"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal([]byte(output), &profile); err != nil {
		t.Fatal(err)
	}
	type interval struct{ start, end uint64 }
	var intervals []interval
	for _, rule := range profile.Syscalls {
		for _, name := range rule.Names {
			if name != "ioctl" {
				continue
			}
			if len(rule.Names) != 1 || rule.Action != "SCMP_ACT_ALLOW" || len(rule.Args) != 1 {
				t.Fatal("invalid ioctl rule shape")
			}
			arg := rule.Args[0]
			if arg.Index != 1 || arg.Op != "SCMP_CMP_MASKED_EQ" || arg.Value == 0 || arg.Value > 0xffffffff || arg.ValueTwo&^arg.Value != 0 {
				t.Fatalf("invalid low32 prefix comparator: %+v", arg)
			}
			inverse := uint32(^uint32(arg.Value))
			if inverse&(inverse+1) != 0 {
				t.Fatalf("mask %#x is not a contiguous high prefix", arg.Value)
			}
			intervals = append(intervals, interval{arg.ValueTwo, arg.ValueTwo + uint64(inverse) + 1})
		}
	}
	if len(intervals) == 0 {
		t.Fatal("no ioctl intervals")
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	var cursor uint64
	var gaps []interval
	for _, current := range intervals {
		if current.start < cursor || current.end <= current.start || current.end > 1<<32 {
			t.Fatalf("overlapping/invalid interval: %+v at %#x", current, cursor)
		}
		if current.start > cursor {
			gaps = append(gaps, interval{cursor, current.start})
		}
		cursor = current.end
	}
	if cursor < 1<<32 {
		gaps = append(gaps, interval{cursor, 1 << 32})
	}
	want := []interval{{0x40046602, 0x40046603}, {0x40086602, 0x40086603}, {0x401c5820, 0x401c5821}}
	if !reflect.DeepEqual(gaps, want) {
		t.Fatalf("low32 gaps=%#v want=%#v", gaps, want)
	}
}

func TestWorkerSeccompRejectsUnpinnedInputAndIsDeterministic(t *testing.T) {
	for _, input := range [][]byte{nil, []byte(`{`), []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), append(append([]byte{}, embeddedWorkerSeccompBaseline...), '\n')} {
		if output, err := buildWorkerSeccomp(input); err == nil || output != "" {
			t.Fatalf("unpinned input returned policy: output=%q error=%v", output, err)
		}
	}
	first, err := buildWorkerSeccomp(embeddedWorkerSeccompBaseline)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildWorkerSeccomp(embeddedWorkerSeccompBaseline)
	if err != nil || first != second {
		t.Fatalf("nondeterministic output, error=%v", err)
	}
}
