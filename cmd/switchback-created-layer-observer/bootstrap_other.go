//go:build !linux || (!amd64 && !arm64)

package main

import (
	"context"
	"fmt"
)

func readInstalledObserverEnvironment(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("observer unsupported platform")
}
