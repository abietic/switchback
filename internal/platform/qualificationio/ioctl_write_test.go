package qualificationio

import (
	"context"
	"errors"
	"syscall"
	"testing"
)

func TestQuotaWriteActualCountsAndNoRetryAfterError(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		writeError            error
		syncError             error
		wantWritten           uint64
		wantWrites, wantSyncs int
	}{{"denied-first", syscall.EDQUOT, nil, 0, 1, 0}, {"denied-sync", nil, syscall.EDQUOT, 4096, 1, 1}, {"success", nil, nil, 8192, 2, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			writes, syncs := 0, 0
			got, err := quotaIO(context.Background(), quotaIOOps{write: func(b []byte, offset int64) (int, error) {
				writes++
				if len(b) != 4096 || offset != int64((writes-1)*4096) {
					t.Fatal("unexpected write")
				}
				if tc.writeError != nil {
					return -1, tc.writeError
				}
				return len(b), nil
			}, sync: func() error { syncs++; return tc.syncError }})
			if err != nil || got.Written != tc.wantWritten || writes != tc.wantWrites || syncs != tc.wantSyncs {
				t.Fatalf("got %+v err %v calls %d/%d", got, err, writes, syncs)
			}
		})
	}
}

func TestQuotaWriteCancelPartialAndDelayedAllocationDoNotFabricatePass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := quotaIO(ctx, quotaIOOps{write: func([]byte, int64) (int, error) { called = true; return 0, nil }, sync: func() error { called = true; return nil }}); !errors.Is(err, context.Canceled) || called {
		t.Fatal("cancelled I/O ran")
	}
	if _, err := quotaIO(context.Background(), quotaIOOps{write: func([]byte, int64) (int, error) { return 2048, nil }, sync: func() error { return nil }}); err == nil {
		t.Fatal("short write became success")
	}
	if validQuotaResult("quota-denied", WriteResult{Requested: 8192, Written: 8192, SyncErrno: 122}) == nil {
		t.Fatal("full accepted byte count was fabricated as partial denial")
	}
	if validQuotaResult("quota-denied", WriteResult{Requested: 8192, Written: 0, WriteErrno: 122, SyncErrno: 5}) == nil {
		t.Fatal("mixed unknown I/O errno accepted")
	}
	if validQuotaResult("quota-success", WriteResult{Requested: 8192, Written: 4096}) == nil {
		t.Fatal("partial success accepted")
	}
}
