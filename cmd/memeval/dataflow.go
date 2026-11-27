package main

import (
	"context"
	"encoding/json"
	"memeval/internal/config/redact"
	"memeval/internal/result"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/selftest"
	"memeval/internal/sut"
	"memeval/internal/transport/jsonrpc"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

func buildDataFlow(ctx context.Context, m *sut.Manifest, l *sut.Lock, r *result.Report) *result.DataFlow {
	d := &result.DataFlow{Parties: []result.DataFlowParty{}, Notes: []string{
		"SUT credentials are injected through env.list; same-user Docker access can inspect container environment values.",
		"Embedding is configured locally in the container variants; this disclosure is not independent verification of SDK traffic.",
		"Image installation/build may contact registries and download wheels/models; zero_egress describes runtime only.",
		"Evaluator LLM calls run on the host and are not intercepted by the SUT network policy.",
		"Summary follows effective evaluator llm.mode: non-replay modes may egress even when record-once hits the cassette cache.",
	}}
	add := func(role, endpoint, enforcement string, evidence ...string) {
		// Only a host:port or a provider name enters this block; never URL paths,
		// userinfo, queries, environment values or response/prompt text.
		endpoint = redact.From(ctx).Text(jsonrpc.Redact(endpoint, m.Secrets()))
		party := result.DataFlowParty{Role: role, Endpoint: endpoint, Direction: "egress", Enforcement: enforcement, Evidence: evidence}
		if role == "sut_llm_gateway" {
			party.Host = endpoint
			party.Metered = true
		}
		d.Parties = append(d.Parties, party)
	}
	iso := r.Isolation
	mode := sandbox.NetworkMode(iso.NetworkMode)
	if mode == sandbox.NetworkNone || mode == sandbox.NetworkGateway {
		d.ZeroEgressProof = dataFlowProof(iso, mode)
	}
	gateway := m.EffectiveGateway()
	gatewayEndpoint := hostPort(gateway.BaseURL)
	switch mode {
	case sandbox.NetworkGateway:
		d.Summary = "sut_gateway"
		add("sut_llm_gateway", gatewayEndpoint, "relay_single_target", "isolation.egress_allowlist", "isolation.selftest.checks.gateway_connect", "isolation.selftest.checks.relay_single_target")
		d.Notes = append(d.Notes, "Gateway proof verifies blocked general egress and an allowed single gateway target; it is not a claim of zero gateway traffic.")
	case sandbox.NetworkOpen:
		d.Summary = "sut_provider"
		endpoint := gatewayEndpoint
		if endpoint == "" {
			endpoint = "provider default (undisclosed)"
		}
		add("sut_provider", endpoint, "none", "isolation.network_mode=open")
		d.Notes = append(d.Notes, "Open networking is unrestricted; any disclosed SUT endpoint is not an egress allowlist.")
	default:
		if d.ZeroEgressProof == nil || !d.ZeroEgressProof.Verified {
			d.Summary = "sut_provider"
			add("sut_provider", "provider default (undisclosed)", "none", "isolation.network_enforced", "isolation.selftest")
			d.Notes = append(d.Notes, "Runtime egress restrictions lack complete passing evidence; zero egress is not established.")
		}
	}
	var metadata struct {
		Mode   string `json:"mode"`
		Models []struct {
			Provider string `json:"provider"`
		} `json:"models_pinned"`
	}
	flowDecode(r.EvaluatorLLM, &metadata)
	if metadata.Mode != "" && metadata.Mode != "none" && metadata.Mode != "replay" {
		providers := map[string]bool{}
		for _, pin := range metadata.Models {
			if p := providerEndpoint(pin.Provider); p != "" {
				providers[p] = true
			}
		}
		// Failed calls may not have a ModelPin yet. Include the configured
		// reader/judge provider too, without disclosing private endpoint URLs.
		for _, config := range []any{r.Reader, r.Protocol.Judge} {
			var v struct {
				Provider string `json:"provider"`
			}
			flowDecode(config, &v)
			if p := providerEndpoint(v.Provider); p != "" {
				providers[p] = true
			}
		}
		if len(providers) == 0 {
			providers["provider default (undisclosed)"] = true
		}
		names := make([]string, 0, len(providers))
		for p := range providers {
			names = append(names, p)
		}
		slices.Sort(names)
		for _, p := range names {
			add("evaluator_llm", p, "host_policy", "evaluator_llm.mode="+metadata.Mode, "evaluator_llm.models_pinned", "reader.provider / protocol.judge.provider")
		}
		if d.Summary != "" {
			d.Summary += "+"
		}
		d.Summary += "evaluator_providers"
	}
	if d.Summary == "" {
		d.Summary = "zero_egress"
	}
	// Sidecar images are pulled during install, never on the episode path.
	// These parties are historical possibilities, not observed run HTTP calls.
	registries := map[string]bool{}
	if l != nil {
		for _, im := range l.SidecarImages {
			if host := imageRegistry(im.Ref); host != "" {
				registries[host] = true
			}
		}
	}
	registriesSorted := make([]string, 0, len(registries))
	for host := range registries {
		registriesSorted = append(registriesSorted, host)
	}
	slices.Sort(registriesSorted)
	for _, host := range registriesSorted {
		add("sidecar_image_registry", host, "host_policy", "sut.lock.sidecar_images (install-time only; not runtime traffic)")
	}
	return d
}

func dataFlowProof(iso result.Isolation, mode sandbox.NetworkMode) *result.DataFlowProof {
	p := &result.DataFlowProof{Scope: "sut_runtime", NetworkMode: string(mode), NetworkEnforced: iso.NetworkEnforced, Checks: map[string]result.DataFlowCheck{}, RunCleanEvidence: "isolation.container.residue.run_clean"}
	var evidence selftest.Evidence
	flowDecode(iso.Selftest, &evidence)
	p.SelftestSHA256, p.ConfigurationSHA256 = evidence.SHA256, evidence.ConfigurationSHA256
	var runtime struct {
		Residue struct {
			RunClean *bool `json:"run_clean"`
		} `json:"residue"`
	}
	flowDecode(iso.Container, &runtime)
	p.RunClean = runtime.Residue.RunClean
	p.Verified = iso.NetworkEnforced && p.SelftestSHA256 != "" && p.RunClean != nil && *p.RunClean
	wants := selftest.GatewayExpectations()
	if mode == sandbox.NetworkNone {
		delete(wants, "gateway_connect")
		delete(wants, "relay_single_target")
	}
	wants["network_internal"] = true
	for name, want := range wants {
		observed, ok := evidence.Checks[name]
		v := result.DataFlowCheck{Expected: want, ControlPassed: observed.ControlPassed}
		if observed.Observed != nil {
			success := observed.Observed.Success
			v.Observed = &success
		}
		v.Passed = ok && observed.Expected == want && observed.ControlPassed && observed.Passed && v.Observed != nil && *v.Observed == want
		p.Checks[name] = v
		p.Verified = p.Verified && v.Passed
	}
	return p
}

func flowDecode(v any, out any) {
	raw, err := json.Marshal(v)
	if err == nil {
		_ = json.Unmarshal(raw, out)
	}
}
func hostPort(u *url.URL) string {
	if u == nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

var providerName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

func providerEndpoint(s string) string {
	if providerName.MatchString(s) {
		return s
	}
	if u, e := url.Parse(s); e == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return hostPort(u)
	}
	return ""
}
func imageRegistry(ref string) string {
	first, _, slash := strings.Cut(ref, "/")
	if first == "" {
		return ""
	}
	if !slash || !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io:443"
	}
	u, err := url.Parse("https://" + first)
	if err != nil || u.User != nil {
		return ""
	}
	return hostPort(u)
}
