//go:build !unix

package procgroup

import "os/exec"

func KillOnCancel(*exec.Cmd) {}
