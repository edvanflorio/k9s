// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lookPath fakes binary resolution: anything in found resolves, the rest does not.
func lookPath(found ...string) lookPathFn {
	set := make(map[string]bool, len(found))
	for _, f := range found {
		set[f] = true
	}

	return func(bin string) (string, error) {
		if set[bin] {
			return "/usr/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
}

func TestResolveTerm(t *testing.T) {
	uu := map[string]struct {
		cfg      *config.ExternalTerminal
		inPath   []string
		bin      string
		execArgs []string
		err      string
	}{
		"autodetect-prefers-modern": {
			cfg:      &config.ExternalTerminal{},
			inPath:   []string{"xterm", "kitty", "gnome-terminal"},
			bin:      "/usr/bin/kitty",
			execArgs: nil,
		},
		"autodetect-fallback": {
			cfg:      &config.ExternalTerminal{},
			inPath:   []string{"xterm"},
			bin:      "/usr/bin/xterm",
			execArgs: []string{"-e"},
		},
		"custom-bare-reuses-known-flag": {
			cfg:      &config.ExternalTerminal{Command: "gnome-terminal"},
			inPath:   []string{"gnome-terminal", "kitty"},
			bin:      "/usr/bin/gnome-terminal",
			execArgs: []string{"--"},
		},
		"custom-with-explicit-flag": {
			cfg:      &config.ExternalTerminal{Command: "wezterm start --"},
			inPath:   []string{"wezterm"},
			bin:      "/usr/bin/wezterm",
			execArgs: []string{"start", "--"},
		},
		"custom-unknown-binary": {
			cfg:      &config.ExternalTerminal{Command: "myterm"},
			inPath:   []string{"myterm"},
			bin:      "/usr/bin/myterm",
			execArgs: nil,
		},
		"custom-not-in-path": {
			cfg:    &config.ExternalTerminal{Command: "nope"},
			inPath: []string{"kitty"},
			err:    `terminal "nope" not in your path: not found`,
		},
		"nothing-available": {
			cfg:    &config.ExternalTerminal{},
			inPath: nil,
			err:    "no supported terminal emulator found in your path",
		},
	}

	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			spec, err := resolveTerm(u.cfg, lookPath(u.inPath...))
			if u.err != "" {
				require.Error(t, err)
				assert.Equal(t, u.err, err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, u.bin, spec.bin)
			assert.Equal(t, u.execArgs, spec.execArgs)
		})
	}
}

func TestBuildTermArgv(t *testing.T) {
	uu := map[string]struct {
		spec  termSpec
		extra []string
		hold  bool
		bin   string
		args  []string
		e     []string
	}{
		"exec-flag": {
			spec: termSpec{bin: "/usr/bin/alacritty", execArgs: []string{"-e"}},
			bin:  "/usr/bin/kubectl",
			args: []string{"exec", "-it", "p1"},
			e:    []string{"/usr/bin/alacritty", "-e", "/usr/bin/kubectl", "exec", "-it", "p1"},
		},
		"no-exec-flag": {
			spec: termSpec{bin: "/usr/bin/kitty"},
			bin:  "/usr/bin/kubectl",
			args: []string{"logs", "-f", "p1"},
			e:    []string{"/usr/bin/kitty", "/usr/bin/kubectl", "logs", "-f", "p1"},
		},
		"extra-args-precede-exec-flag": {
			spec:  termSpec{bin: "/usr/bin/gnome-terminal", execArgs: []string{"--"}},
			extra: []string{"--title", "k9s"},
			bin:   "/usr/bin/kubectl",
			args:  []string{"describe", "po/p1"},
			e: []string{
				"/usr/bin/gnome-terminal", "--title", "k9s", "--",
				"/usr/bin/kubectl", "describe", "po/p1",
			},
		},
		"hold-open-wraps-in-sh": {
			spec: termSpec{bin: "/usr/bin/kitty"},
			hold: true,
			bin:  "/usr/bin/kubectl",
			args: []string{"describe", "po/p1"},
			e: []string{
				"/usr/bin/kitty", "sh", "-c", holdOpenScript,
				"/usr/bin/kubectl", "/usr/bin/kubectl", "describe", "po/p1",
			},
		},
	}

	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			assert.Equal(t, u.e, buildTermArgv(&u.spec, u.extra, u.hold, u.bin, u.args))
		})
	}
}

// The hold-open wrapper must pass the command through "$@" so that arguments
// carrying spaces or quotes never get re-split by the shell.
func TestHoldOpenArgvIsQuotingSafe(t *testing.T) {
	bin, args := holdOpenArgv("/usr/bin/kubectl", []string{"describe", "po/a b", `q"uote`})

	assert.Equal(t, "sh", bin)
	assert.Equal(t, []string{
		"-c", holdOpenScript,
		"/usr/bin/kubectl", "/usr/bin/kubectl",
		"describe", "po/a b", `q"uote`,
	}, args)
}

// TestLaunchExternalSpawns drives the real spawn path end to end against a
// stand-in "terminal" that records the argv it was handed.
func TestLaunchExternalSpawns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	fakeTerm := filepath.Join(dir, "faketerm")
	require.NoError(t, os.WriteFile(fakeTerm,
		[]byte("#!/bin/sh\n{ for a in \"$@\"; do echo \"$a\"; done; echo \"ENV=$K9S_EXT_VIEW\"; } > "+out+"\n"), 0o600))
	require.NoError(t, os.Chmod(fakeTerm, 0o700))

	t.Setenv(config.EnvExtTermCommand, fakeTerm)
	t.Setenv("DISPLAY", ":0")

	a := NewApp(mock.NewMockConfig(t))
	no := false
	a.Config.K9s.ExternalTerminal = &config.ExternalTerminal{
		Enabled:  true,
		HoldOpen: &no,
	}

	require.NoError(t, launchExternal(a, config.ExtTermShell, &shellOpts{
		binary: "/usr/bin/kubectl",
		args:   []string{"exec", "-it", "p1", "--", "sh"},
		action: config.ExtTermShell,
	}, []string{config.EnvExtView + "=kind=logs,gvr=v1/pods,path=ns1/p1"}))

	// The spawn is detached, so wait for the stand-in to land its output.
	var got []byte
	for range 100 {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NotEmpty(t, got, "fake terminal never ran")

	assert.Equal(t,
		[]string{
			"/usr/bin/kubectl", "exec", "-it", "p1", "--", "sh",
			"ENV=kind=logs,gvr=v1/pods,path=ns1/p1",
		},
		strings.Split(strings.TrimSpace(string(got)), "\n"),
	)
}

// A disabled action must never spawn and must tell the caller to fall back.
func TestLaunchExternalDisabled(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	a.Config.K9s.ExternalTerminal = &config.ExternalTerminal{
		Enabled: true,
		Actions: []config.ExtTermAction{config.ExtTermLogs},
	}

	err := launchExternal(a, config.ExtTermShell, &shellOpts{binary: "/bin/true"}, nil)
	require.ErrorIs(t, err, errExtTermOff)
}

// Piped plugin commands cannot be detached and must stay in place.
func TestLaunchExternalRefusesPipes(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	a.Config.K9s.ExternalTerminal = &config.ExternalTerminal{Enabled: true}

	err := launchExternal(a, config.ExtTermPlugin, &shellOpts{
		binary: "/bin/true",
		pipes:  []string{"grep foo"},
	}, nil)
	require.ErrorIs(t, err, errExtTermOff)
}

// A view whose action is switched off must not spawn anything.
func TestLaunchK9sViewDisabled(t *testing.T) {
	a := NewApp(mock.NewMockConfig(t))
	a.Config.K9s.ExternalTerminal = &config.ExternalTerminal{
		Enabled: true,
		Actions: []config.ExtTermAction{config.ExtTermShell},
	}

	assert.False(t, launchK9sView(a, &config.ExtView{
		Kind: config.ExtViewLogs,
		GVR:  "v1/pods",
		Path: "ns1/p1",
	}))
}

// bootExtView is a no-op without a spec, so a normal k9s boots normally.
func TestBootExtViewNoSpec(t *testing.T) {
	t.Setenv(config.EnvExtView, "")
	assert.False(t, bootExtView(NewApp(mock.NewMockConfig(t))))
}

// A malformed spec must not wedge startup: the instance boots normally.
func TestBootExtViewBadSpec(t *testing.T) {
	t.Setenv(config.EnvExtView, "kind=bogus,gvr=v1/pods,path=ns1/p1")
	assert.False(t, bootExtView(NewApp(mock.NewMockConfig(t))))
}
