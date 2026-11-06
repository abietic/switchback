package capacitypublisher

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func changeRaw(q *capacityprobe.PoolQuotaReadback, fn func([]byte)) {
	raw, e := hex.DecodeString(q.Target.RawHex)
	if e != nil {
		panic(e)
	}
	fn(raw)
	q.Target.RawHex = hex.EncodeToString(raw)
	h, u, ih, iu := binary.LittleEndian.Uint64(raw[8:16]), binary.LittleEndian.Uint64(raw[40:48]), binary.LittleEndian.Uint64(raw[24:32]), binary.LittleEndian.Uint64(raw[48:56])
	q.Target.HardBlocks = &h
	q.Target.UsedBlocks = &u
	q.Target.HardInodes = &ih
	q.Target.UsedInodes = &iu
}
func rejectObservation(t *testing.T, m *Mapper, o capacityprobe.PoolObservation) {
	t.Helper()
	before := deepClone(o)
	r, e := m.Map(context.Background(), o, 1)
	if e == nil || !reflect.DeepEqual(r, Result{}) {
		t.Fatalf("wanted complete zero rejection, got %#v %v", r, e)
	}
	if !reflect.DeepEqual(o, before) {
		t.Fatal("input mutated")
	}
	switch e.Error() {
	case "observation_invalid", "observation_stale", "identity_drift", "inventory_incomplete", "quota_unproven", "resource_mismatch":
	default:
		t.Fatalf("unbounded reason %q", e)
	}
}
func TestMapperInodeMatrix(t *testing.T) {
	cases := []struct {
		name       string
		hard, used uint64
		ok         bool
		assessment capacitysnapshot.InodeEvidenceAssessment
	}{
		{"zero_empty", 0, 0, true, capacitysnapshot.InodeBoundUnproven}, {"zero_used", 0, 2, true, capacitysnapshot.InodeBoundUnproven}, {"zero_max", 0, math.MaxUint64, true, capacitysnapshot.InodeBoundUnproven}, {"positive_below", 2, 1, true, capacitysnapshot.InodesObserved}, {"positive_equal", 2, 2, true, capacitysnapshot.InodesObserved}, {"positive_over", 1, 2, false, ""}, {"signed_boundary", math.MaxInt64, math.MaxInt64, true, capacitysnapshot.InodesObserved}, {"unsigned_boundary", uint64(math.MaxInt64) + 1, uint64(math.MaxInt64) + 1, true, capacitysnapshot.InodesObserved}, {"unsigned_max", math.MaxUint64, math.MaxUint64, true, capacitysnapshot.InodesObserved}}
	for _, tc := range cases {
		for _, location := range []string{"static", "worker"} {
			for _, def := range []struct {
				name       string
				hard, used uint64
			}{{"zero", 0, 0}, {"positive", 2, 1}, {"different", 99, 123}, {"max", math.MaxUint64, math.MaxUint64}} {
				t.Run(tc.name+"/"+location+"/default_"+def.name, func(t *testing.T) {
					m, o := newFixtureMapper(t, true)
					q := &o.Storage[0].Quota
					if location == "worker" {
						q = &o.Workers[0].Quota
					}
					changeRaw(q, func(raw []byte) {
						binary.LittleEndian.PutUint64(raw[24:32], tc.hard)
						binary.LittleEndian.PutUint64(raw[48:56], tc.used)
					})
					binary.LittleEndian.PutUint64(q.Default0[24:32], def.hard)
					binary.LittleEndian.PutUint64(q.Default0[48:56], def.used)
					if !tc.ok {
						rejectObservation(t, m, o)
						return
					}
					before := deepClone(o)
					r, e := m.Map(context.Background(), o, 1)
					if e != nil {
						t.Fatal(e)
					}
					got := r.StorageInodes["core-data"]
					if location == "worker" {
						got = r.LayerInodes[syntheticWorkerCID]
					}
					if got != (capacitysnapshot.CreatedLayerInodeObservation{HardInodes: tc.hard, UsedInodes: tc.used, Assessment: tc.assessment}) || !reflect.DeepEqual(o, before) {
						t.Fatalf("raw/counter mismatch: %#v", got)
					}
				})
			}
		}
	}
}
func TestMapperRawRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*capacityprobe.PoolQuotaReadback)
	}{
		{"version", func(q *capacityprobe.PoolQuotaReadback) { changeRaw(q, func(b []byte) { b[0] = 2 }) }}, {"type", func(q *capacityprobe.PoolQuotaReadback) { changeRaw(q, func(b []byte) { b[1] = 1 }) }}, {"project", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint32(b[4:8], 0) })
		}}, {"raw_hex", func(q *capacityprobe.PoolQuotaReadback) { q.Target.RawHex = "xx" }}, {"counter_mismatch", func(q *capacityprobe.PoolQuotaReadback) { u := uint64(9); q.Target.UsedBlocks = &u }}, {"inode_mismatch", func(q *capacityprobe.PoolQuotaReadback) { u := uint64(9); q.Target.UsedInodes = &u }}, {"nil_counter", func(q *capacityprobe.PoolQuotaReadback) { q.Target.HardInodes = nil }}, {"status", func(q *capacityprobe.PoolQuotaReadback) { q.Target.Status = "unavailable" }}, {"abi", func(q *capacityprobe.PoolQuotaReadback) { q.Target.ABI = "other" }}, {"units", func(q *capacityprobe.PoolQuotaReadback) { q.Target.UnitBytes = 4096 }},
		{"default_version", func(q *capacityprobe.PoolQuotaReadback) { q.Default0[0] = 2 }}, {"default_type", func(q *capacityprobe.PoolQuotaReadback) { q.Default0[1] = 1 }}, {"default_project", func(q *capacityprobe.PoolQuotaReadback) { q.Default0[4] = 1 }},
		{"hard_zero", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], 0) })
		}}, {"hard_overflow", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], math.MaxUint64) })
		}}, {"hard_signed_overflow", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], math.MaxInt64/512+1) })
		}}, {"used_overflow", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint64(b[40:48], math.MaxUint64) })
		}}, {"used_over_hard", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint64(b[40:48], 20971521) })
		}}, {"worker_soft", func(q *capacityprobe.PoolQuotaReadback) {
			changeRaw(q, func(b []byte) { binary.LittleEndian.PutUint64(b[16:24], 1) })
		}},
		{"missing_state", func(q *capacityprobe.PoolQuotaReadback) { q.StateSamples = nil }}, {"duplicate_state", func(q *capacityprobe.PoolQuotaReadback) {
			s := q.StateSamples[0]
			s.Sequence = 2
			s.Started = s.Ended
			s.Ended = s.Ended.Add(time.Millisecond)
			q.StateSamples = append(q.StateSamples, s)
		}}, {"truncated", func(q *capacityprobe.PoolQuotaReadback) { q.StateSamplesTruncated = true }}, {"sample_sequence", func(q *capacityprobe.PoolQuotaReadback) { q.StateSamples[0].Sequence = 2 }}, {"sample_source", func(q *capacityprobe.PoolQuotaReadback) { q.StateSamples[0].Source = "fixture" }}, {"sample_errno", func(q *capacityprobe.PoolQuotaReadback) { v := uint64(0); q.StateSamples[0].Errno = &v }}, {"late_failed_prefix", func(q *capacityprobe.PoolQuotaReadback) {
			s := q.StateSamples[0]
			s.Sequence = 2
			s.Operation = "target"
			s.Started = s.Ended
			s.Ended = s.Ended.Add(time.Millisecond)
			s.Outcome = "failed"
			q.StateSamples = append(q.StateSamples, s)
		}}, {"overlap", func(q *capacityprobe.PoolQuotaReadback) {
			s := q.StateSamples[0]
			s.Sequence = 2
			s.Operation = "target"
			q.StateSamples = append(q.StateSamples, s)
		}}, {"outside_window", func(q *capacityprobe.PoolQuotaReadback) {
			q.StateSamples[0].Ended = q.StateSamples[0].Ended.Add(time.Hour)
		}}, {"negative_interval", func(q *capacityprobe.PoolQuotaReadback) { q.StateSamples[0].Ended = q.StateSamples[0].Started }}, {"sample_limit", func(q *capacityprobe.PoolQuotaReadback) {
			q.StateSamples = make([]capacityprobe.CreatedLayerSample, 65)
		}}, {"qstat_version", func(q *capacityprobe.PoolQuotaReadback) { q.QStatV[0] = 2 }}, {"qstat_off", func(q *capacityprobe.PoolQuotaReadback) { q.QStatV[2] = 0 }}, {"qstat_unknown", func(q *capacityprobe.PoolQuotaReadback) { q.QStatV[3] = 1 }}, {"qstat_padding", func(q *capacityprobe.PoolQuotaReadback) { q.QStatV[159] = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			tc.mutate(&o.Workers[0].Quota)
			rejectObservation(t, m, o)
		})
	}
}
func TestMapperByteQuotaEqualityAndStaticSoft(t *testing.T) {
	for _, which := range []string{"static", "worker"} {
		t.Run(which, func(t *testing.T) {
			m, o := newFixtureMapper(t, true)
			q := &o.Storage[0].Quota
			if which == "worker" {
				q = &o.Workers[0].Quota
			}
			changeRaw(q, func(b []byte) {
				binary.LittleEndian.PutUint64(b[40:48], 20971520)
				if which == "static" {
					binary.LittleEndian.PutUint64(b[16:24], 0)
				}
			})
			r, e := m.Map(context.Background(), o, 1)
			if e != nil {
				t.Fatal(e)
			}
			d := r.Facts.Storage[0]
			if which == "worker" {
				d = r.Facts.Containers[0].Layer
			}
			if d.HardBytes != 10737418240 || d.UsedBytes != 10737418240 {
				t.Fatal(d)
			}
		})
	}
}

func TestMapperStaticByteSignedBoundary(t *testing.T) {
	tr, c, o := fixture(false)
	tr.Storage[0].HardBytes = 9223372036854775296
	tr.Storage[0].Quota.HardBlocks512 = 18014398509481983
	changeRaw(&o.Storage[0].Quota, func(b []byte) {
		binary.LittleEndian.PutUint64(b[8:16], 18014398509481983)
		binary.LittleEndian.PutUint64(b[40:48], 18014398509481983)
	})
	m, e := NewMapper(tr, c, syntheticWorkerImage)
	if e != nil {
		t.Fatal(e)
	}
	r, e := m.Map(context.Background(), o, 1)
	if e != nil || r.Facts.Storage[0].HardBytes != 9223372036854775296 || r.Facts.Storage[0].UsedBytes != 9223372036854775296 {
		t.Fatalf("boundary %#v %v", r, e)
	}
}
