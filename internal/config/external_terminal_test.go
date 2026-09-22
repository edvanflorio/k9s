// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package config_test

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalTerminalActive(t *testing.T) {
	uu := map[string]struct {
		et *config.ExternalTerminal
		a  config.ExtTermAction
		e  bool
	}{
		"nil-is-off":   {et: nil, a: config.ExtTermShell, e: false},
		"disabled":     {et: &config.ExternalTerminal{}, a: config.ExtTermShell, e: false},
		"enabled-all":  {et: &config.ExternalTerminal{Enabled: true}, a: config.ExtTermYAML, e: true},
		"unknown-name": {et: &config.ExternalTerminal{Enabled: true}, a: "bogus", e: false},
		"subset-in": {
			et: &config.ExternalTerminal{Enabled: true, Actions: []config.ExtTermAction{config.ExtTermShell}},
			a:  config.ExtTermShell,
			e:  true,
		},
		"subset-out": {
			et: &config.ExternalTerminal{Enabled: true, Actions: []config.ExtTermAction{config.ExtTermShell}},
			a:  config.ExtTermLogs,
			e:  false,
		},
	}

	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			assert.Equal(t, u.e, u.et.Active(u.a))
		})
	}
}

func TestExternalTerminalHoldOpen(t *testing.T) {
	no, yes := false, true

	assert.True(t, (*config.ExternalTerminal)(nil).ShouldHoldOpen())
	assert.True(t, new(config.ExternalTerminal).ShouldHoldOpen())
	assert.True(t, (&config.ExternalTerminal{HoldOpen: &yes}).ShouldHoldOpen())
	assert.False(t, (&config.ExternalTerminal{HoldOpen: &no}).ShouldHoldOpen())
}

// The config must survive the real Load -> Merge path, not just unmarshalling.
// Merge copies K9s field by field, so a new field is silently dropped unless
// it is listed there.
func TestExternalTerminalSurvivesMerge(t *testing.T) {
	src := config.NewConfig(nil)
	src.K9s.ExternalTerminal = &config.ExternalTerminal{
		Enabled: true,
		Command: "kitty",
		Actions: []config.ExtTermAction{config.ExtTermShell},
	}

	dst := config.NewConfig(nil)
	dst.Merge(src)

	require.NotNil(t, dst.K9s.ExternalTerminal)
	assert.True(t, dst.K9s.ExternalTerminal.Enabled)
	assert.Equal(t, "kitty", dst.K9s.ExternalTerminal.Command)
	assert.True(t, dst.K9s.ExternalTerminal.Active(config.ExtTermShell))
	assert.False(t, dst.K9s.ExternalTerminal.Active(config.ExtTermLogs))
}

// An unconfigured external terminal must merge to nil, leaving it inactive.
func TestExternalTerminalMergeAbsent(t *testing.T) {
	dst := config.NewConfig(nil)
	dst.Merge(config.NewConfig(nil))

	assert.Nil(t, dst.K9s.ExternalTerminal)
	assert.False(t, dst.K9s.ExternalTerminal.Active(config.ExtTermShell))
}

func TestExtViewRoundTrip(t *testing.T) {
	uu := map[string]struct {
		v *config.ExtView
		e string
	}{
		"logs-with-container": {
			v: &config.ExtView{
				Kind: config.ExtViewLogs, GVR: "v1/pods",
				Path: "ns1/p1", Container: "c1",
			},
			e: "kind=logs,gvr=v1/pods,path=ns1/p1,container=c1",
		},
		"logs-previous": {
			v: &config.ExtView{
				Kind: config.ExtViewLogs, GVR: "v1/pods",
				Path: "ns1/p1", Previous: true,
			},
			e: "kind=logs,gvr=v1/pods,path=ns1/p1,previous=true",
		},
		"describe": {
			v: &config.ExtView{
				Kind: config.ExtViewDescribe,
				GVR:  "apps/v1/deployments", Path: "ns1/d1",
			},
			e: "kind=describe,gvr=apps/v1/deployments,path=ns1/d1",
		},
	}

	for k, u := range uu {
		t.Run(k, func(t *testing.T) {
			assert.Equal(t, u.e, u.v.String())

			back, err := config.ParseExtView(u.e)
			require.NoError(t, err)
			assert.Equal(t, u.v, back)
		})
	}
}

func TestParseExtView(t *testing.T) {
	// A blank spec means "boot normally", not an error.
	v, err := config.ParseExtView("")
	require.NoError(t, err)
	assert.Nil(t, v)

	uu := map[string]string{
		"unknown-kind":  "kind=bogus,gvr=v1/pods,path=ns1/p1",
		"missing-gvr":   "kind=logs,path=ns1/p1",
		"missing-path":  "kind=logs,gvr=v1/pods",
		"unknown-key":   "kind=logs,gvr=v1/pods,path=ns1/p1,fred=x",
		"malformed-tok": "kind=logs,gvr=v1/pods,path",
	}
	for k, spec := range uu {
		t.Run(k, func(t *testing.T) {
			_, err := config.ParseExtView(spec)
			require.Error(t, err)
		})
	}
}
