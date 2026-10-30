package process

import (
	"fmt"
	"github.com/sagernet/sing/common/atomic"
	"os/exec"
)

type Process struct {
	path    string
	args    []string
	noOut   bool
	cmd     *exec.Cmd
	stopped atomic.Bool
}

func NewProcess(path string, args []string, noOut bool) *Process {
	return &Process{path: path, args: args, noOut: noOut}
}

func (p *Process) Start() error {
	p.cmd = exec.Command(p.path, p.args...)

	p.cmd.Stdout = &pipeLogger{prefix: "Extra Core", noOut: p.noOut}
	p.cmd.Stderr = &pipeLogger{prefix: "Extra Core", noOut: p.noOut}

	err := p.cmd.Start()
	if err != nil {
		return err
	}
	p.stopped.Store(false)

	go func() {
		fmt.Println(p.path, ":", "process started, waiting for it to end")
		_ = p.cmd.Wait()
		if !p.stopped.Load() {
			fmt.Println("Extra process exited unexpectedly")
		}
	}()
	return nil
}

func (p *Process) Stop() {
	if p == nil {
		return
	}
	p.stopped.Store(true)
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	// Reap the child without blocking Stop: Wait was already consumed
	// by the Start watcher goroutine in the normal path; after a failed
	// Start or a Kill race this discards the exit cleanly.
	go func() {
		_ = p.cmd.Wait()
	}()
}
