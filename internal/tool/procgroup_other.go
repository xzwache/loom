//go:build !unix

package tool

import "os/exec"

func killProcessGroupOnCancel(*exec.Cmd) {}
