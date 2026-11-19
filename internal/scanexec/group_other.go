//go:build !unix

package scanexec

import "os/exec"

// ownGroup leaves cmd as it is: without process groups only the scanner
// itself is killed when its context is cancelled.
func ownGroup(*exec.Cmd) {}

// killGroup does nothing without process groups.
func killGroup(*exec.Cmd) {}
