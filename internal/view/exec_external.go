// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/google/shlex"
)

var (
	// errExtTermOff indicates the action is not configured to run externally.
	// Callers fall back to the in-place behavior, silently.
	errExtTermOff = errors.New("external terminal not active")

	// errExtTermUnavailable indicates the action was meant to run externally
	// but could not. Callers fall back in place and tell the user why.
	errExtTermUnavailable = errors.New("external terminal unavailable")
)

// holdOpenScript keeps the spawned window around once the command exits so
// short lived output (describe/yaml) remains readable. Args are passed
// positionally to sidestep shell quoting.
const holdOpenScript = `"$@"; rc=$?; printf '\n[k9s] %s exited (%d). Press enter to close.' "$0" "$rc"; read -r _`

const (
	// execFlag is the flag most emulators use to hand over the command argv.
	execFlag = "-e"

	// argvSep is the separator the others use for the same purpose.
	argvSep = "--"
)

// termSpec describes how to hand an argv over to a terminal emulator.
type termSpec struct {
	bin string
	// execArgs precede the command argv. Empty means the command is appended
	// straight after the binary.
	execArgs []string
}

// knownTerms tracks the terminal emulators we know how to drive, most modern
// first. Every entry takes the command as argv (never as a shell string).
var knownTerms = []termSpec{
	{bin: "ghostty", execArgs: []string{execFlag}},
	{bin: "wezterm", execArgs: []string{"start", argvSep}},
	{bin: "kitty"},
	{bin: "alacritty", execArgs: []string{execFlag}},
	{bin: "foot"},
	// Ptyxis opens a new window when handed a command after --.
	{bin: "ptyxis", execArgs: []string{argvSep}},
	{bin: "gnome-terminal", execArgs: []string{argvSep}},
	{bin: "konsole", execArgs: []string{execFlag}},
	{bin: "xfce4-terminal", execArgs: []string{"-x"}},
	{bin: "terminator", execArgs: []string{"-x"}},
	{bin: "urxvt", execArgs: []string{execFlag}},
	{bin: "st", execArgs: []string{execFlag}},
	{bin: "xterm", execArgs: []string{execFlag}},
	{bin: "wt.exe", execArgs: []string{}},
}

// holdOpenActions tracks the actions whose command prints and exits, so the
// window must linger to stay readable. Everything else owns its window for its
// whole life: shells, editors and the k9s instances we spawn for logs,
// describe and yaml.
var holdOpenActions = []config.ExtTermAction{
	config.ExtTermPlugin,
}

type lookPathFn func(string) (string, error)

// resolveTerm figures out which terminal emulator to drive. An explicit config
// command wins, then K9S_TERMINAL, then autodetection over knownTerms.
func resolveTerm(cfg *config.ExternalTerminal, lookPath lookPathFn) (*termSpec, error) {
	if custom := customTerm(cfg); custom != "" {
		tokens, err := shlex.Split(custom)
		if err != nil || len(tokens) == 0 {
			return nil, fmt.Errorf("unable to parse terminal command %q", custom)
		}
		bin, err := lookPath(tokens[0])
		if err != nil {
			return nil, fmt.Errorf("terminal %q not in your path: %w", tokens[0], err)
		}
		spec := termSpec{bin: bin}
		if len(tokens) > 1 {
			spec.execArgs = tokens[1:]
			return &spec, nil
		}
		// No explicit exec flag given: reuse the one we know for this terminal.
		if known, ok := knownTermFor(tokens[0]); ok {
			spec.execArgs = known.execArgs
		}

		return &spec, nil
	}

	for i := range knownTerms {
		bin, err := lookPath(knownTerms[i].bin)
		if err != nil {
			continue
		}
		spec := knownTerms[i]
		spec.bin = bin

		return &spec, nil
	}

	return nil, errors.New("no supported terminal emulator found in your path")
}

func customTerm(cfg *config.ExternalTerminal) string {
	if env := os.Getenv(config.EnvExtTermCommand); env != "" {
		return env
	}
	if cfg != nil {
		return cfg.Command
	}

	return ""
}

func knownTermFor(bin string) (termSpec, bool) {
	base := strings.TrimSuffix(bin[strings.LastIndexByte(bin, '/')+1:], ".exe")
	for i := range knownTerms {
		if strings.TrimSuffix(knownTerms[i].bin, ".exe") == base {
			return knownTerms[i], true
		}
	}

	return termSpec{}, false
}

// holdOpenArgv wraps a command so the terminal window lingers once it exits.
func holdOpenArgv(bin string, args []string) (shell string, shellArgs []string) {
	out := make([]string, 0, len(args)+4)
	out = append(out, "-c", holdOpenScript, bin, bin)

	return "sh", append(out, args...)
}

// buildTermArgv assembles the full argv handed to the terminal emulator.
func buildTermArgv(spec *termSpec, extraArgs []string, hold bool, bin string, args []string) []string {
	if hold {
		bin, args = holdOpenArgv(bin, args)
	}
	argv := make([]string, 0, len(extraArgs)+len(spec.execArgs)+len(args)+2)
	argv = append(argv, spec.bin)
	argv = append(argv, extraArgs...)
	argv = append(argv, spec.execArgs...)
	argv = append(argv, bin)

	return append(argv, args...)
}

// hasDisplay reports whether spawning a window can possibly work. Guards the
// headless/SSH case where no emulator could ever show up.
func hasDisplay() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
}

// extTermFor returns the external terminal config when the given action must
// be launched in a detached window.
func extTermFor(a *App, action config.ExtTermAction) (*config.ExternalTerminal, bool) {
	if a == nil || a.Config == nil || a.Config.K9s == nil {
		return nil, false
	}
	cfg := a.Config.K9s.ExternalTerminal
	if !cfg.Active(action) {
		return nil, false
	}

	return cfg, true
}

// launchExternal spawns opts in a detached terminal window and returns at once,
// leaving the k9s UI running and on screen.
func launchExternal(a *App, action config.ExtTermAction, opts *shellOpts, extraEnv []string) error {
	cfg, ok := extTermFor(a, action)
	if !ok {
		return errExtTermOff
	}
	if opts.binary == "" {
		return errExtTermOff
	}
	// Piping is wired through our own stdio, which a detached window does not
	// have. Keep those in place rather than silently dropping the pipes.
	if len(opts.pipes) > 0 {
		return errExtTermOff
	}
	if !hasDisplay() {
		return fmt.Errorf("%w: no DISPLAY/WAYLAND_DISPLAY set", errExtTermUnavailable)
	}
	spec, err := resolveTerm(cfg, exec.LookPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errExtTermUnavailable, err)
	}

	hold := cfg.ShouldHoldOpen() && slices.Contains(holdOpenActions, action)
	argv := buildTermArgv(spec, cfg.Args, hold, opts.binary, opts.args)

	// Background, never canceled: the window is meant to outlive this command
	// and k9s itself, so it must not be tied to any cancelable context.
	cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...)
	cmd.SysProcAttr = detachSysProcAttr()
	cmd.Env = append(externalEnv(), extraEnv...)
	// Detached: the child owns its own window, so it must not inherit our tty.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil

	slog.Debug("Launching external terminal",
		slogs.Action, string(action),
		slogs.Terminal, spec.bin,
		slogs.Command, opts.String(),
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: unable to launch %s: %w", errExtTermUnavailable, spec.bin, err)
	}
	// Reap the child so it does not linger as a zombie. The window outlives us
	// on purpose, so this is deliberately not tied to a cancelable context.
	go func() {
		if err := cmd.Wait(); err != nil {
			slog.Debug("External terminal exited", slogs.Error, err)
		}
	}()

	return nil
}

// externalEnv mirrors the KUBE_EDITOR handling done for in-place execution so
// editors configured with arguments keep working in a detached window.
func externalEnv() []string {
	env := os.Environ()
	e := os.Getenv("K9S_EDITOR")
	if e == "" {
		return env
	}
	tokens, err := shlex.Split(e)
	if err != nil || len(tokens) == 0 {
		return env
	}
	bin, err := exec.LookPath(tokens[0])
	if err != nil {
		return env
	}
	tokens[0] = bin
	for i := range tokens {
		tokens[i] = shellQuote(tokens[i])
	}

	return append(env, "KUBE_EDITOR="+strings.Join(tokens, " "))
}

// tryExternal runs opts in a detached window. It reports whether the launch
// happened; when it returns false the caller must run the command in place.
func tryExternal(a *App, action config.ExtTermAction, opts *shellOpts, extraEnv []string) bool {
	switch err := launchExternal(a, action, opts, extraEnv); {
	case err == nil:
		return true
	case errors.Is(err, errExtTermOff):
		return false
	default:
		slog.Warn("External terminal unavailable. Falling back",
			slogs.Action, string(action),
			slogs.Error, err,
		)
		a.Flash().Warnf("%s. Falling back", err)

		return false
	}
}

// k9sViewActions maps an external view onto the action that gates it.
var k9sViewActions = map[config.ExtViewKind]config.ExtTermAction{
	config.ExtViewLogs:     config.ExtTermLogs,
	config.ExtViewDescribe: config.ExtTermDescribe,
	config.ExtViewYAML:     config.ExtTermYAML,
}

// launchK9sView spawns another k9s in a detached window, told to boot straight
// into the given view. Keeping k9s on both ends preserves the skin, the
// filtering and the log viewer instead of dropping to raw kubectl output.
// Reports whether the window was launched; on false the caller renders in place.
func launchK9sView(a *App, spec *config.ExtView) bool {
	action, ok := k9sViewActions[spec.Kind]
	if !ok {
		return false
	}
	if _, active := extTermFor(a, action); !active {
		return false
	}
	bin, err := os.Executable()
	if err != nil {
		slog.Warn("Unable to locate the k9s binary. Falling back", slogs.Error, err)
		return false
	}

	return tryExternal(a, action, &shellOpts{
		binary: bin,
		args:   k9sViewArgs(a, spec),
		action: action,
	}, []string{config.EnvExtView + "=" + spec.String()})
}

// k9sViewArgs points the spawned k9s at the very same cluster and identity.
func k9sViewArgs(a *App, spec *config.ExtView) []string {
	args := []string{"--context", a.Config.K9s.ActiveContextName()}
	if ns, _ := client.Namespaced(spec.Path); client.IsNamespaced(ns) {
		args = append(args, "-n", ns)
	}
	flags := a.Conn().Config().Flags()
	if flags.KubeConfig != nil && *flags.KubeConfig != "" {
		args = append(args, "--kubeconfig", *flags.KubeConfig)
	}
	if flags.Insecure != nil && *flags.Insecure {
		args = append(args, "--insecure-skip-tls-verify")
	}
	if u, err := a.Conn().Config().ImpersonateUser(); err == nil {
		args = append(args, "--as", u)
	}
	if g, err := a.Conn().Config().ImpersonateGroups(); err == nil {
		args = append(args, "--as-group", g)
	}

	return args
}

// launchLogsExt opens the k9s log view for a resource in its own window.
func launchLogsExt(a *App, gvr *client.GVR, o *dao.LogOptions) bool {
	return launchK9sView(a, &config.ExtView{
		Kind:      config.ExtViewLogs,
		GVR:       gvr.String(),
		Path:      o.Path,
		Container: o.Container,
		Previous:  o.Previous,
	})
}

// launchDescribeExt opens the k9s describe view in its own window.
func launchDescribeExt(a *App, gvr *client.GVR, path string) bool {
	return launchK9sView(a, &config.ExtView{
		Kind: config.ExtViewDescribe,
		GVR:  gvr.String(),
		Path: path,
	})
}

// launchYAMLExt opens the k9s yaml view in its own window.
func launchYAMLExt(a *App, gvr *client.GVR, path string) bool {
	return launchK9sView(a, &config.ExtView{
		Kind: config.ExtViewYAML,
		GVR:  gvr.String(),
		Path: path,
	})
}

// bootExtView lands a freshly spawned k9s straight on the requested view.
// Reports whether it took over the default startup command.
func bootExtView(a *App) bool {
	raw := os.Getenv(config.EnvExtView)
	if raw == "" {
		return false
	}
	// Consume it so anything this instance spawns boots normally.
	_ = os.Unsetenv(config.EnvExtView)

	spec, err := config.ParseExtView(raw)
	if err != nil || spec == nil {
		if err != nil {
			slog.Error("Bad external view spec. Booting normally", slogs.Error, err)
		}
		return false
	}

	gvr := client.NewGVR(spec.GVR)
	var comp model.Component
	switch spec.Kind {
	case config.ExtViewLogs:
		cfg := a.Config.K9s.Logger
		comp = NewLog(gvr, &dao.LogOptions{
			Path:            spec.Path,
			Container:       spec.Container,
			Previous:        spec.Previous,
			Lines:           cfg.TailCount,
			SinceSeconds:    cfg.SinceSeconds,
			ShowTimestamp:   cfg.ShowTime,
			LogBufferSize:   cfg.LogBufferSize,
			SingleContainer: spec.Container != "",
			AllContainers:   spec.Container == "",
		})
	case config.ExtViewDescribe:
		comp = NewLiveView(a, "Describe", model.NewDescribe(gvr, spec.Path))
	case config.ExtViewYAML:
		comp = NewLiveView(a, yamlAction, model.NewYAML(gvr, spec.Path))
	default:
		return false
	}

	slog.Debug("Booting into external view",
		slogs.Action, string(spec.Kind),
		slogs.GVR, spec.GVR,
		slogs.Path, spec.Path,
	)
	if err := a.inject(comp, true); err != nil {
		slog.Error("External view boot failed. Booting normally", slogs.Error, err)
		return false
	}

	return true
}

// logExtTermStatus reports, at startup, whether detached windows are armed and
// which terminal will be driven. Without it the only trace of the feature is a
// debug line at keypress time, which makes a misconfigured setup look silent.
func logExtTermStatus(a *App) {
	cfg := a.Config.K9s.ExternalTerminal
	if cfg == nil || !cfg.Enabled {
		return
	}
	if !hasDisplay() {
		slog.Warn("External terminal enabled but no display. Actions stay in place")
		return
	}
	spec, err := resolveTerm(cfg, exec.LookPath)
	if err != nil {
		slog.Warn("External terminal enabled but unusable. Actions stay in place",
			slogs.Error, err,
		)
		return
	}

	aa := cfg.Actions
	if len(aa) == 0 {
		aa = config.DefaultExtTermActions()
	}
	ss := make([]string, 0, len(aa))
	for _, x := range aa {
		ss = append(ss, string(x))
	}
	slog.Info("External terminal armed",
		slogs.Terminal, spec.bin,
		slogs.Action, strings.Join(ss, ","),
	)
}
