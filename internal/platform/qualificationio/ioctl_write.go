package qualificationio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
)

// The seam is private. Production supplies only Pwrite on the already held
// O_DSYNC child FD and Fsync on that same FD. No amounts come from control/API.
type quotaIOOps struct {
	write func([]byte, int64) (int, error)
	sync  func() error
}

func quotaIO(ctx context.Context, k quotaIOOps) (WriteResult, error) {
	out := WriteResult{Requested: QuotaWriteBytes}
	if ctx == nil || k.write == nil || k.sync == nil {
		return out, fmt.Errorf("qualification quota I/O invalid")
	}
	buf := bytes.Repeat([]byte{0x71}, 4096)
	for out.Written < out.Requested {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		n, err := k.write(buf, int64(out.Written))
		if n < 0 && err != nil {
			n = 0
		} // Linux's error sentinel denotes no accepted bytes.
		if n < 0 || n > len(buf) {
			return out, fmt.Errorf("qualification write count invalid")
		}
		out.Written += uint64(n)
		if err != nil {
			out.WriteErrno = errnoValue(err)
			return out, ctx.Err()
		}
		if n != len(buf) {
			return out, io.ErrShortWrite
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if err := k.sync(); err != nil {
			out.SyncErrno = errnoValue(err)
			return out, ctx.Err()
		}
	}
	return out, ctx.Err()
}
func errnoValue(err error) uint64 {
	var e syscall.Errno
	if errors.As(err, &e) {
		return uint64(e)
	}
	return ^uint64(0)
}

func validQuotaResult(testCase string, w WriteResult) error {
	if w.Requested != QuotaWriteBytes || w.Written > w.Requested {
		return fmt.Errorf("qualification quota count invalid")
	}
	if testCase == "quota-denied" {
		if (w.WriteErrno != 0 && w.WriteErrno != 122) || (w.SyncErrno != 0 && w.SyncErrno != 122) || (w.WriteErrno != 122 && w.SyncErrno != 122) || w.Written >= w.Requested {
			return fmt.Errorf("qualification quota denial unproven")
		}
	} else if testCase != "quota-success" || w.WriteErrno != 0 || w.SyncErrno != 0 || w.Written != w.Requested {
		return fmt.Errorf("qualification quota success unproven")
	}
	return nil
}
