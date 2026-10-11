//go:build !unix

package bash

import "os/exec"

func killProcessGroupOnCancel(*exec.Cmd) {}
