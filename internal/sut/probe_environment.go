package sut

import (
	"fmt"
	"maps"
	"memeval/internal/sandbox"
	"strings"
)

const probeAPIKey = "memeval-probe-placeholder"

// offlineProbeManifest preserves the runtime model/effort and original URL,
// but never passes the gateway credential or provider passthrough to describe.
// Only this private copy bypasses the runtime requirement for egress: describe
// must construct the adapter without issuing model requests on network none.
func offlineProbeManifest(m *Manifest) (*Manifest, error) {
	g := m.EffectiveGateway()
	if m.LLM.Required && (strings.TrimSpace(g.Model) == "" || g.Source["MEMEVAL_SUT_LLM_MODEL"] == "default") {
		return nil, fmt.Errorf("llm.required but no model configured (MEMEVAL_SUT_LLM_MODEL or manifest llm.model)")
	}
	g.APIKey = probeAPIKey
	probe := *m
	probe.Gateway = g
	probe.LLM.Env = maps.Clone(m.LLM.Env)
	probe.LLM.Required = false
	probe.Sandbox.Backend = "container"
	probe.Sandbox.Network = sandbox.NetworkNone
	probe.Sidecars = nil
	probe.Process.EnvPassthrough = nil
	probe.Process.Env = maps.Clone(m.Process.Env)
	if probe.Process.Env == nil {
		probe.Process.Env = map[string]string{}
	}
	maps.Copy(probe.Process.Env, m.Runtime.ProbeEnv)
	return &probe, nil
}
