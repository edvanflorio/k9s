// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config

import "slices"

// ExtTermAction denotes an action that can be launched in an external terminal.
type ExtTermAction string

const (
	// ExtTermShell tracks the shell/exec action.
	ExtTermShell ExtTermAction = "shell"

	// ExtTermAttach tracks the container attach action.
	ExtTermAttach ExtTermAction = "attach"

	// ExtTermEdit tracks the resource edit action.
	ExtTermEdit ExtTermAction = "edit"

	// ExtTermPlugin tracks plugin invocations.
	ExtTermPlugin ExtTermAction = "plugin"

	// ExtTermLogs tracks the resource logs action.
	ExtTermLogs ExtTermAction = "logs"

	// ExtTermDescribe tracks the resource describe action.
	ExtTermDescribe ExtTermAction = "describe"

	// ExtTermYAML tracks the resource yaml action.
	ExtTermYAML ExtTermAction = "yaml"
)

// EnvExtTermCommand overrides the external terminal binary.
const EnvExtTermCommand = "K9S_TERMINAL"

// defaultExtTermActions tracks the actions enabled by default.
var defaultExtTermActions = []ExtTermAction{
	ExtTermShell,
	ExtTermAttach,
	ExtTermEdit,
	ExtTermPlugin,
	ExtTermLogs,
	ExtTermDescribe,
	ExtTermYAML,
}

// ExternalTerminal tracks external terminal preferences. When enabled, actions
// spawn a detached terminal window instead of taking over the k9s terminal.
type ExternalTerminal struct {
	Enabled  bool            `json:"enabled" yaml:"enabled"`
	Command  string          `json:"command,omitempty" yaml:"command,omitempty"`
	Args     []string        `json:"args,omitempty" yaml:"args,omitempty"`
	HoldOpen *bool           `json:"holdOpen,omitempty" yaml:"holdOpen,omitempty"`
	Actions  []ExtTermAction `json:"actions,omitempty" yaml:"actions,omitempty"`
}

// NewExternalTerminal returns a new instance.
func NewExternalTerminal() *ExternalTerminal {
	return &ExternalTerminal{}
}

// Active checks if a given action should launch in an external terminal.
func (e *ExternalTerminal) Active(a ExtTermAction) bool {
	if e == nil || !e.Enabled {
		return false
	}
	if len(e.Actions) == 0 {
		return slices.Contains(defaultExtTermActions, a)
	}

	return slices.Contains(e.Actions, a)
}

// ShouldHoldOpen checks if the terminal window must linger once the command exits.
func (e *ExternalTerminal) ShouldHoldOpen() bool {
	if e == nil || e.HoldOpen == nil {
		return true
	}

	return *e.HoldOpen
}
