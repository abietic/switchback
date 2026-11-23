//go:build !linux

package docker

import "fmt"

func readFirstReportStorage() (int64, error) {
	return 0, fmt.Errorf("first-report XFS capacity sampling requires Linux")
}
