//go:build !windows

package prompt

import "os/exec"

func prepareCmd(cmd *exec.Cmd) {}
