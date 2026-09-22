// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

//go:build windows

package view

import "syscall"

// detachSysProcAttr is a no-op on Windows where the terminal host already
// spawns detached.
func detachSysProcAttr() *syscall.SysProcAttr {
	return nil
}
