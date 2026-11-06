package capacitysnapshot

import (
	"context"
	"time"
)

type qualificationNativeCollectOps struct {
	architecture string
	boot         func(context.Context) (string, error)
	mono         func() (int64, error)
	now          func() time.Time
	self         func(context.Context, qualificationSource, string) error
	hash         func(context.Context, string, int64) (string, error)
	waitHost     func(context.Context, qualificationCandidateDirectory, qualificationCandidateRecord, qualificationWindowOpen) error
	sample       func(context.Context, qualificationCandidateDirectory, qualificationCandidateRecord, qualificationWindowOpen) (qualificationNativeFacts, qualificationCleanup, error)
}

func collectNativeQualificationWith(ctx context.Context, dir qualificationCandidateDirectory, k qualificationNativeCollectOps) error {
	if err := dir.claim(ctx, "collect"); err != nil {
		return err
	}
	var staged qualificationStaged
	var candidate qualificationCandidateRecord
	var opened qualificationWindowOpen
	for _, item := range []struct {
		name  string
		value any
	}{{"staged.json", &staged}, {"candidate.json", &candidate}, {"window-open.json", &opened}} {
		if err := dir.decode(ctx, item.name, item.value); err != nil {
			return err
		}
	}
	if qualificationValidateStaged(staged) != nil || qualificationValidateCreateBoundStaged(staged) != nil || candidate.Schema != candidateSchema || candidate.StagedDigest != qualificationDigest(staged) || candidate.OpenDigest != qualificationDigest(opened) || candidate.TestProject != staged.TestProject || candidate.Source.Inputs.Architecture != k.architecture {
		return errQualification
	}
	staticDigest, err := qualificationValidateStaticIntent(ctx, dir.decode, staged)
	if err != nil || candidate.StaticIntentDigest != staticDigest {
		return errQualification
	}
	manifest, err := qualificationManifestPrefix(candidate)
	if err != nil {
		return err
	}
	fingerprint, err := qualificationCandidate(candidate.Trust, manifest, candidate.Binding)
	if err != nil || fingerprint != candidate.Fingerprint || !qualificationSameJSON(candidate.Trust, staged.Trust) || !qualificationSameJSON(candidate.Binding, staged.Binding) {
		return errQualification
	}
	if err := k.self(ctx, candidate.Source, "native-producer"); err != nil {
		return err
	}
	boot, err := k.boot(ctx)
	if err != nil {
		return err
	}
	mono, err := k.mono()
	if err != nil {
		return err
	}
	remaining, err := qualificationRemaining(opened, boot, mono, k.now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if err := qualificationCheckStagedArtifacts(ctx, staged, k.hash); err != nil {
		return err
	}
	if err := k.waitHost(ctx, dir, candidate, opened); err != nil {
		return err
	}
	facts, cleanup, err := k.sample(ctx, dir, candidate, opened)
	if err != nil {
		return err
	}
	if err := validateQualificationCleanup(cleanup, candidate.TestProject); err != nil {
		return err
	}
	checks, err := qualificationMapNativeChecks(opened.ID, facts)
	if err != nil {
		return err
	}
	bootAfter, err := k.boot(ctx)
	if err != nil || bootAfter != opened.GuestBoot {
		return errQualification
	}
	mono, err = k.mono()
	if err != nil {
		return err
	}
	if _, err := qualificationRemaining(opened, bootAfter, mono, k.now()); err != nil {
		return err
	}
	completed := k.now()
	if !completed.After(facts.ReadonlyEnd) {
		return errQualification
	}
	native := qualificationNativeObservation{Schema: nativeObservationSchema, Candidate: candidate.Fingerprint, OpenDigest: candidate.OpenDigest,
		WindowID: opened.ID, GuestBootBefore: boot, GuestBootAfter: bootAfter,
		Completed: completed.UTC().Format(time.RFC3339Nano), Checks: checks, Cleanup: cleanup}
	return dir.put(ctx, "native-observations.json", native)
}
