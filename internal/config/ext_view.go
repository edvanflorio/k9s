// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config

import (
	"fmt"
	"strings"
)

// EnvExtView carries the view spec to a detached k9s instance.
const EnvExtView = "K9S_EXT_VIEW"

// ExtViewKind denotes which view a detached k9s must open on start.
type ExtViewKind string

const (
	// ExtViewLogs opens the log view.
	ExtViewLogs ExtViewKind = "logs"

	// ExtViewDescribe opens the describe view.
	ExtViewDescribe ExtViewKind = "describe"

	// ExtViewYAML opens the yaml view.
	ExtViewYAML ExtViewKind = "yaml"
)

// ExtView describes the view a detached k9s instance must land on. It travels
// between the two processes as a flat key=value string.
type ExtView struct {
	Kind      ExtViewKind
	GVR       string
	Path      string
	Container string
	Previous  bool
}

// String renders the spec for the command line.
func (e *ExtView) String() string {
	ss := []string{
		"kind=" + string(e.Kind),
		"gvr=" + e.GVR,
		"path=" + e.Path,
	}
	if e.Container != "" {
		ss = append(ss, "container="+e.Container)
	}
	if e.Previous {
		ss = append(ss, "previous=true")
	}

	return strings.Join(ss, ",")
}

// ParseExtView reads back a spec rendered by String. A blank spec yields a nil
// view and no error: the instance simply boots normally.
func ParseExtView(s string) (*ExtView, error) {
	if s == "" {
		return nil, nil
	}
	var e ExtView
	for _, tok := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			return nil, fmt.Errorf("malformed view spec fragment %q", tok)
		}
		switch k {
		case "kind":
			e.Kind = ExtViewKind(v)
		case "gvr":
			e.GVR = v
		case "path":
			e.Path = v
		case "container":
			e.Container = v
		case "previous":
			e.Previous = v == "true"
		default:
			return nil, fmt.Errorf("unknown view spec key %q", k)
		}
	}
	switch e.Kind {
	case ExtViewLogs, ExtViewDescribe, ExtViewYAML:
	default:
		return nil, fmt.Errorf("unknown view kind %q", e.Kind)
	}
	if e.GVR == "" || e.Path == "" {
		return nil, fmt.Errorf("view spec needs both gvr and path: %q", s)
	}

	return &e, nil
}
