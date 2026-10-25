//go:build !unix

package calibredb

import "os/exec"

func killGroup(*exec.Cmd) {}
