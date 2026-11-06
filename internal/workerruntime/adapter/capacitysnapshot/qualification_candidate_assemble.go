package capacitysnapshot

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

// assembleQualificationInstalled packages only exclusive leaves from this
// candidate's completed window. The installer receives prepared-* leaves; it
// still owns final Trust/config installation and manifest-last commit.
func assembleQualificationInstalled(ctx context.Context) error {
	dir, err := openQualificationCandidate(ctx, qualificationGuestCandidateRoot, 0)
	if err != nil {
		return err
	}
	defer dir.close()
	return assembleQualificationFromDirectory(ctx, dir, qualificationGuestBoot, qualificationMonotonicNS, time.Now)
}

func assembleQualificationFromDirectory(ctx context.Context, dir qualificationCandidateDirectory,
	boot func(context.Context) (string, error), mono func() (int64, error), now func() time.Time) error {
	return assembleQualificationWithWriter(ctx, dir, boot, mono, now, dir.write)
}

func assembleQualificationWithWriter(ctx context.Context, dir qualificationCandidateDirectory,
	boot func(context.Context) (string, error), mono func() (int64, error), now func() time.Time,
	write func(context.Context, string, []byte) error) error {
	if err := dir.claim(ctx, "assemble"); err != nil {
		return err
	}
	var staged qualificationStaged
	var opened qualificationWindowOpen
	var candidate qualificationCandidateRecord
	var native qualificationNativeObservation
	var before qualificationHostBefore
	var host qualificationHostReceipt
	var common qualificationCommonEnd
	for _, item := range []struct {
		name string
		into any
	}{
		{"staged.json", &staged},
		{"window-open.json", &opened},
		{"candidate.json", &candidate},
		{"native-observations.json", &native},
		{"host-before.json", &before},
		{"host-observations.json", &host},
		{"common-window.json", &common},
	} {
		if err := dir.decode(ctx, item.name, item.into); err != nil {
			return fmt.Errorf("qualification assembly input %s: %w", item.name, err)
		}
	}
	b, err := boot(ctx)
	if err != nil {
		return err
	}
	m, err := mono()
	if err != nil {
		return err
	}
	remaining, err := qualificationRemaining(opened, b, m, now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	checkRemaining := func() error {
		if err := qualificationContext(ctx); err != nil {
			return err
		}
		currentBoot, err := boot(ctx)
		if err != nil {
			return err
		}
		currentMono, err := mono()
		if err != nil {
			return err
		}
		if _, err := qualificationRemaining(opened, currentBoot, currentMono, now()); err != nil {
			return err
		}
		return qualificationContext(ctx)
	}
	staticDigest, err := qualificationValidateStaticIntent(ctx, dir.decode, staged)
	if err != nil || candidate.StaticIntentDigest != staticDigest {
		return errQualification
	}
	if candidate.StagedDigest != qualificationDigest(staged) || candidate.OpenDigest != qualificationDigest(opened) ||
		candidate.TestProject != staged.TestProject || candidate.Source.Inputs.Architecture != staged.Source.Inputs.Architecture ||
		common.Schema != "switchback.qualification.common_end.v1" || common.Candidate != candidate.Fingerprint ||
		common.HostBeforeDigest != qualificationDigest(before) || common.NativeDigest != qualificationDigest(native) || common.HostDigest != qualificationDigest(host) ||
		!reflect.DeepEqual(common.Window, host.Window) || native.OpenDigest != candidate.OpenDigest ||
		host.Candidate != candidate.Fingerprint || native.Candidate != candidate.Fingerprint ||
		before.Schema != "switchback.qualification.host_before.v1" || before.Candidate != candidate.Fingerprint || before.OpenDigest != candidate.OpenDigest || before.HostBoot != host.Window.HostBootBefore || len(before.Checks) != 2 || len(host.Checks) != 5 ||
		!reflect.DeepEqual(before.Checks, host.Checks[:2]) {
		return errQualification
	}
	start, se := time.Parse(time.RFC3339Nano, opened.Started)
	end, ee := time.Parse(time.RFC3339Nano, common.Window.Ended)
	completed, ce := time.Parse(time.RFC3339Nano, native.Completed)
	beforeEnd, be := time.Parse(time.RFC3339Nano, before.Started)
	if se != nil || ee != nil || ce != nil || common.Window.ID != opened.ID ||
		be != nil || !beforeEnd.After(start) || !completed.After(beforeEnd) || !end.After(completed) || end.Sub(start) > qualificationBudget ||
		common.Window.Started != opened.Started || native.GuestBootBefore != opened.GuestBoot ||
		native.GuestBootAfter != opened.GuestBoot {
		return errQualification
	}
	for _, check := range native.Checks {
		from, fe := time.Parse(time.RFC3339Nano, check.Observation.Started)
		if fe != nil || from.Before(beforeEnd) {
			return errQualification
		}
	}
	for _, check := range host.Checks[2:] {
		from, fe := time.Parse(time.RFC3339Nano, check.Observation.Started)
		if fe != nil || !from.After(completed) {
			return errQualification
		}
	}
	manifest, err := qualificationManifestPrefix(candidate)
	if err != nil {
		return err
	}
	fingerprint, err := qualificationCandidate(candidate.Trust, manifest, candidate.Binding)
	if err != nil || fingerprint != candidate.Fingerprint {
		return errQualification
	}
	files, err := assembleQualification(candidate, host, native)
	if err != nil {
		return err
	}
	if err := checkRemaining(); err != nil {
		return err
	}
	for _, name := range qualificationLeaves {
		if err := checkRemaining(); err != nil {
			return err
		}
		if err := write(ctx, "prepared-"+name, files[name]); err != nil {
			return fmt.Errorf("qualification prepared leaf %s: %w", name, err)
		}
		if err := checkRemaining(); err != nil {
			return err
		}
	}
	return checkRemaining()
}
