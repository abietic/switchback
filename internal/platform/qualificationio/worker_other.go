//go:build !linux || (!amd64 && !arm64 && !386 && !arm)

package qualificationio

import (
	"context"
	"fmt"
	"io"
)

func RunWorker(context.Context, io.Writer) error {
	return fmt.Errorf("qualification helper unsupported platform")
}
func RunBackend(context.Context, io.Writer) error {
	return fmt.Errorf("qualification helper unsupported platform")
}
func RunCompat(context.Context, io.Writer) error {
	return fmt.Errorf("qualification helper unsupported platform")
}
