package capacityprobe

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPoolPID1ZeroStarttime(t *testing.T) {
	for _, tc := range []struct {
		name             string
		actual, expected uint64
		ticks            string
		group            string
		valid            bool
		want             uint64
	}{
		{"pid1-zero", 1, 1, "0", "0::/init.scope\n", true, 0},
		{"other-zero", 2, 2, "0", "0::/init.scope\n", false, 0},
		{"unbound-zero", 1, 0, "0", "0::/init.scope\n", false, 0},
		{"mismatch-zero", 2, 1, "0", "0::/init.scope\n", false, 0},
		{"actual-zero", 0, 1, "0", "0::/init.scope\n", false, 0},
		{"bad-ticks", 1, 1, "x", "0::/init.scope\n", false, 0},
		{"negative-ticks", 1, 1, "-1", "0::/init.scope\n", false, 0},
		{"overflow-ticks", 1, 1, "18446744073709551616", "0::/init.scope\n", false, 0},
		{"missing-ticks", 1, 1, "", "0::/init.scope\n", false, 0},
		{"bad-group", 1, 1, "0", "0::/bad/../path\n", false, 0},
		{"nonzero", 2, 2, "99", "0::/init.scope\n", true, 99},
		{"unbound-nonzero", 1, 0, "99", "0::/init.scope\n", true, 99},
		{"maximum", 1, 1, "18446744073709551615", "0::/init.scope\n", true, ^uint64(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Replace(deploymentFixtureStat(tc.actual), []byte("18 99 20\n"), []byte("18 "+tc.ticks+" 20\n"), 1)
			if tc.name == "missing-ticks" {
				raw = bytes.Replace(raw, []byte("18  20\n"), []byte("18\n"), 1)
			}
			ticks, group, err := poolProcess(raw, []byte(tc.group), tc.expected)
			if tc.valid {
				if err != nil || ticks != tc.want || group != "/sys/fs/cgroup/init.scope" {
					t.Fatalf("ticks=%d group=%q err=%v", ticks, group, err)
				}
			} else if !errors.Is(err, ErrPoolInvalid) || ticks != 0 || group != "" {
				t.Fatalf("accepted invalid: ticks=%d group=%q err=%v", ticks, group, err)
			}
		})
	}
}

func TestDeploymentPID1ZeroStarttime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		drift  int
		caller bool
	}{
		{"stable", 0, false}, {"second-read-drift", 2, false}, {"canonical-third-read-drift", 3, false}, {"caller-zero", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutate := func(op string, n int, v any) any {
				if op == "read:leaf:1:1" {
					ticks := "0"
					if tc.drift != 0 && n >= tc.drift {
						ticks = "1"
					}
					return bytes.Replace(v.([]byte), []byte("18 99 20\n"), []byte("18 "+ticks+" 20\n"), 1)
				}
				if tc.caller && op == "read:leaf:1:0" {
					return bytes.Replace(v.([]byte), []byte("18 99 20\n"), []byte("18 0 20\n"), 1)
				}
				return v
			}
			old, f := newDeploymentFixture(), newDeploymentFixture()
			old.mutate = mutate
			f.mutate = mutate
			deploymentParseAssertParity(t, old, f, context.Background(), context.Background())
			baseline := newDeploymentFixture()
			want, err := observeDeployment(context.Background(), baseline)
			if err != nil {
				t.Fatal(err)
			}
			deploymentAssertClosed(t, baseline)
			f = newDeploymentFixture()
			f.mutate = mutate
			got, err := observeDeployment(context.Background(), f)
			deploymentAssertClosed(t, f)
			parsed := newDeploymentFixture()
			parsed.mutate = mutate
			diagnostic, failure := observeDeploymentParseDiagnostic(context.Background(), parsed)
			deploymentAssertClosed(t, parsed)
			if tc.drift == 0 && !tc.caller {
				want.PID1Before.StartTimeTicks = 0
				want.PID1After.StartTimeTicks = 0
				if err != nil || failure != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(diagnostic, want) {
					t.Fatalf("zero not preserved: %#v %v %#v", got, err, failure)
				}
				if f.occurrences["read:leaf:1:1"] != 3 {
					t.Fatal("missing canonical PID1 read")
				}
			} else {
				if !errors.Is(err, ErrDeploymentInvalid) || got != (DeploymentObservation{}) || failure == nil || failure.Validate() != nil {
					t.Fatalf("identity accepted: %#v %v %#v", got, err, failure)
				}
				wantChecks := &DeploymentProcessParseChecks{"pid1", "bounded", "bounded", "accepted", "accepted"}
				if tc.caller {
					wantChecks = &DeploymentProcessParseChecks{"caller", "bounded", "bounded", "accepted", "rejected"}
				}
				if !reflect.DeepEqual(failure.ParseChecks, wantChecks) {
					t.Fatalf("checks: %#v want %#v", failure.ParseChecks, wantChecks)
				}
			}
		})
	}
}
