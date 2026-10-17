//go:build !windows

package shell

import "os/exec"

func prepareCmd(cmd *exec.Cmd) {}
