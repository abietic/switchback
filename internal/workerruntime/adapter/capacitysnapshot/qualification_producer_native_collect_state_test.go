package capacitysnapshot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The fake stops at raw fact-producing Engine/proc/syscall seams. The real
// collect claim, staged/candidate/deadline checks, seven-check mapper,
// immutable native leaf, assembler and production validator all run.
func TestQualificationSyntheticCollectStateToProductionValidator(t *testing.T) {
	f := newQualificationCollectFixture(t)
	ctx, t0, c, opened, host, facts, cleanup, dir, ops := f.ctx, f.t0, f.candidate, f.opened, f.host, f.facts, f.cleanup, f.dir, f.ops
	native, common := f.collect(t)
	if f.samples != 1 || f.monoCalls != 2 {
		t.Fatal("collector did not traverse one bounded sample")
	}
	if err := collectNativeQualificationWith(ctx, dir, ops); err == nil {
		t.Fatal("collect phase resumed")
	}
	if native.Completed != t0.Add(5*time.Second).Format(time.RFC3339Nano) || len(native.Checks) != 7 {
		t.Fatal("native completion not mapped")
	}
	f.assemble(t)
	var final Trust
	if err := dir.decode(ctx, "prepared-trust.json", &final); err != nil {
		t.Fatal(err)
	}
	installed := map[string][]byte{}
	for _, leaf := range qualificationLeaves {
		raw, err := dir.read(ctx, "prepared-"+leaf)
		if err != nil {
			t.Fatal(err)
		}
		installed[leaf] = raw
	}
	if _, err := validateDeploymentQualification(final, installed); err != nil {
		t.Fatal(err)
	}
	newInputDir := func(candidate qualificationCandidateRecord) qualificationCandidateDirectory {
		return f.inputDirectory(t, candidate)
	}

	t.Run("mixed-window candidate", func(t *testing.T) {
		mixed := c
		mixed.OpenDigest = strings.Repeat("0", len(c.OpenDigest))
		d := newInputDir(mixed)
		prior := f.samples
		if err := collectNativeQualificationWith(ctx, d, ops); err == nil || f.samples != prior {
			t.Fatal("mixed candidate reached raw sampler")
		}
		if _, err := d.read(ctx, "native-observations.json"); !os.IsNotExist(err) {
			t.Fatalf("completion leaf after mixed window: %v", err)
		}
	})
	t.Run("unknown cleanup", func(t *testing.T) {
		d := newInputDir(c)
		bad := ops
		bad.sample = func(context.Context, qualificationCandidateDirectory, qualificationCandidateRecord, qualificationWindowOpen) (qualificationNativeFacts, qualificationCleanup, error) {
			unknown := cleanup
			unknown.FDsClosed = false
			return facts, unknown, nil
		}
		if err := collectNativeQualificationWith(ctx, d, bad); err == nil {
			t.Fatal("unknown cleanup published native completion")
		}
		if _, err := d.read(ctx, "native-observations.json"); !os.IsNotExist(err) {
			t.Fatalf("completion leaf after unknown cleanup: %v", err)
		}
		if err := collectNativeQualificationWith(ctx, d, ops); err == nil {
			t.Fatal("failed collect claim resumed")
		}
	})
	t.Run("assemble deadline crosses validation", func(t *testing.T) {
		d := newInputDir(c)
		for _, item := range []struct {
			name string
			v    any
		}{{"native-observations.json", native}, {"host-observations.json", host}, {"common-window.json", common}} {
			if err := d.put(ctx, item.name, item.v); err != nil {
				t.Fatal(err)
			}
		}
		clockReads := 0
		mono := func() (int64, error) {
			clockReads++
			if clockReads == 1 {
				return 100 + int64(9*time.Second), nil
			}
			return 100 + int64(241*time.Second), nil
		}
		now := func() time.Time {
			if clockReads <= 1 {
				return t0.Add(9 * time.Second)
			}
			return t0.Add(241 * time.Second)
		}
		if err := assembleQualificationFromDirectory(ctx, d, func(context.Context) (string, error) { return opened.GuestBoot, nil }, mono, now); err == nil {
			t.Fatal("assembly succeeded after original deadline")
		}
		if _, err := d.read(ctx, "prepared-trust.json"); !os.IsNotExist(err) {
			t.Fatalf("expired assembly published Trust: %v", err)
		}
		if err := assembleQualificationFromDirectory(ctx, d, func(context.Context) (string, error) { return opened.GuestBoot, nil }, mono, now); err == nil {
			t.Fatal("expired assemble claim resumed")
		}
	})
	t.Run("assemble deadline crosses write", func(t *testing.T) {
		d := newInputDir(c)
		for _, item := range []struct {
			name string
			v    any
		}{{"native-observations.json", native}, {"host-observations.json", host}, {"common-window.json", common}} {
			if err := d.put(ctx, item.name, item.v); err != nil {
				t.Fatal(err)
			}
		}
		expired, writes := false, 0
		mono := func() (int64, error) {
			if expired {
				return 100 + int64(241*time.Second), nil
			}
			return 100 + int64(9*time.Second), nil
		}
		now := func() time.Time {
			if expired {
				return t0.Add(241 * time.Second)
			}
			return t0.Add(9 * time.Second)
		}
		write := func(ctx context.Context, name string, raw []byte) error {
			writes++
			if err := d.write(ctx, name, raw); err != nil {
				return err
			}
			expired = true
			return nil
		}
		if err := assembleQualificationWithWriter(ctx, d, func(context.Context) (string, error) { return opened.GuestBoot, nil }, mono, now, write); err == nil || writes != 1 {
			t.Fatalf("mid-write expiry was accepted or repeated: writes=%d err=%v", writes, err)
		}
		if _, err := d.read(ctx, "prepared-trust.json"); !os.IsNotExist(err) {
			t.Fatalf("expired write published Trust: %v", err)
		}
		if err := assembleQualificationWithWriter(ctx, d, func(context.Context) (string, error) { return opened.GuestBoot, nil }, mono, now, write); err == nil {
			t.Fatal("failed assemble claim resumed")
		}
	})
}
