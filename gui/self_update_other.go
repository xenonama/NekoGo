//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

func startSelfUpdateHelper(selfUpdatePlan) (*exec.Cmd, error) {
	return nil, errors.New("NekoGo self-update is supported on Windows only")
}
