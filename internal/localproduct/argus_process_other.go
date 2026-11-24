//go:build !linux

package localproduct

import "os/exec"

func prepareArgusProcess(_ *exec.Cmd) {}
