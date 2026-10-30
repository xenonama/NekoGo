//go:build !windows

package main

// procJob is Windows-only: elsewhere the managers already kill the
// child directly and there is no re-parenting behaviour to guard
// against, so every method is a no-op.
type procJob struct{}

func newProcJob() *procJob        { return nil }
func (j *procJob) attach(pid int) {}
func (j *procJob) kill()          {}
func (j *procJob) close()         {}
