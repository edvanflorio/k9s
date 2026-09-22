// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

//go:build !windows

package view

import "syscall"

// detachSysProcAttr puts the spawned terminal in its own session so it
// survives k9s and is shielded from the signals sent to our process group.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
