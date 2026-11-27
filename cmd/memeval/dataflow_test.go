package main

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/config/redact"
	"memeval/internal/llm"
	"memeval/internal/result"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sandbox/selftest"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var updateDataFlow = flag.Bool("update-data-flow-goldens", false, "rewrite synthetic data_flow goldens")

func flowFixture(t *testing.T, mode sandbox.NetworkMode) (*sut.Manifest, *sut.Lock, *result.Report) {
	t.Helper()
	g, err := sut.ParseLLMGateway("http://192.0.2.10:8317/v1", "fake-gateway-key", "fixture-model", "low")
	if err != nil {
		t.Fatal(err)
	}
	m := &sut.Manifest{Gateway: g}
	l := &sut.Lock{SidecarImages: []sut.SidecarImage{{Name: "postgres", Ref: "pgvector/pgvector:pg18@sha256:fixture"}}}
	e := &selftest.Evidence{SHA256: strings.Repeat("a", 64), ConfigurationSHA256: strings.Repeat("b", 64), Checks: map[string]selftest.Check{}}
	for name, want := range selftest.ContainerExpectations(mode, nil) {
		e.Checks[name] = selftest.Check{Expected: want, ControlPassed: true, Passed: true, Observed: &container.Observation{Success: want}}
	}
	r := &result.Report{Isolation: result.Isolation{NetworkMode: string(mode), NetworkEnforced: mode != sandbox.NetworkOpen, Selftest: e, Container: map[string]any{"residue": map[string]any{"run_clean": true}}}, EvaluatorLLM: map[string]any{"mode": "replay", "models_pinned": []llm.ModelPin{{Provider: "openai-compat", Model: "fixture"}}}}
	return m, l, r
}
func TestDataFlowGolden(t *testing.T) {
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkGateway, sandbox.NetworkOpen} {
		t.Run(string(mode), func(t *testing.T) {
			m, l, r := flowFixture(t, mode)
			if mode == sandbox.NetworkOpen {
				r.EvaluatorLLM = map[string]any{"mode": "record-once", "models_pinned": []llm.ModelPin{{Provider: "typesafe", Model: "jev-fixture"}, {Provider: "openai-compat", Model: "fixture"}, {Provider: "typesafe", Model: "other"}}}
			}
			d := buildDataFlow(t.Context(), m, l, r)
			want := map[sandbox.NetworkMode]string{sandbox.NetworkNone: "zero_egress", sandbox.NetworkGateway: "sut_gateway", sandbox.NetworkOpen: "sut_provider+evaluator_providers"}[mode]
			if d.Summary != want {
				t.Fatal(d)
			}
			if mode != sandbox.NetworkOpen && !d.ZeroEgressProof.Verified {
				t.Fatal(d.ZeroEgressProof)
			}
			raw, err := json.MarshalIndent(d, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			if strings.Contains(string(raw), m.Gateway.APIKey) || strings.Contains(string(raw), "/v1") {
				t.Fatal("credential or path leaked", string(raw))
			}
			p := filepath.Join("testdata", "data_flow", string(mode)+".golden.json")
			if *updateDataFlow {
				if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(p, raw, 0644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != string(golden) {
				t.Fatalf("data_flow drift:\n%s", raw)
			}
			// Loaded JSON has maps/slices instead of concrete metadata structs.
			wire, _ := json.Marshal(r)
			var decoded result.Report
			if err = json.Unmarshal(wire, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(d, buildDataFlow(t.Context(), m, l, &decoded)) {
				t.Fatal("typed and decoded metadata disagree")
			}
		})
	}
}
func TestDataFlowEvaluatorModesAndProof(t *testing.T) {
	for _, mode := range []string{"none", "replay", "record", "record-once", "live"} {
		for _, netMode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkGateway, sandbox.NetworkOpen} {
			m, l, r := flowFixture(t, netMode)
			r.EvaluatorLLM = map[string]any{"mode": mode, "models_pinned": []llm.ModelPin{}}
			r.Reader = map[string]any{"provider": "openai-compat"}
			r.Protocol.Judge = &result.JudgeInfo{Provider: "typesafe"}
			d := buildDataFlow(t.Context(), m, l, r)
			evaluators := 0
			for _, p := range d.Parties {
				if p.Role == "evaluator_llm" {
					evaluators++
				}
			}
			live := mode != "none" && mode != "replay"
			if live && evaluators != 2 || !live && evaluators != 0 || strings.Contains(d.Summary, "evaluator_providers") != live {
				t.Fatal(mode, netMode, d)
			}
			if netMode == sandbox.NetworkNone && live && d.Summary != "evaluator_providers" {
				t.Fatal(d)
			}
		}
	}
	for _, invalid := range []string{"missing-proof", "missing-observation", "failed-probe", "failed-control", "dirty-run", "unenforced", "missing-hash"} {
		t.Run(invalid, func(t *testing.T) {
			m, l, r := flowFixture(t, sandbox.NetworkNone)
			e := r.Isolation.Selftest.(*selftest.Evidence)
			switch invalid {
			case "missing-proof":
				r.Isolation.Selftest = nil
			case "missing-observation":
				c := e.Checks["tcp4_control"]
				c.Observed = nil
				e.Checks["tcp4_control"] = c
			case "failed-probe":
				c := e.Checks["tcp4_control"]
				c.Observed.Success = true
				e.Checks["tcp4_control"] = c
			case "failed-control":
				c := e.Checks["tcp4_control"]
				c.ControlPassed = false
				e.Checks["tcp4_control"] = c
			case "dirty-run":
				r.Isolation.Container = map[string]any{"residue": map[string]any{"run_clean": false}}
			case "unenforced":
				r.Isolation.NetworkEnforced = false
			case "missing-hash":
				e.SHA256 = ""
			}
			d := buildDataFlow(t.Context(), m, l, r)
			if d.ZeroEgressProof.Verified || d.Summary == "zero_egress" {
				t.Fatal(d)
			}
		})
	}
}
func TestDataFlowRedactsAndNormalizesEndpoints(t *testing.T) {
	m, l, r := flowFixture(t, sandbox.NetworkOpen)
	r.EvaluatorLLM = map[string]any{"mode": "record", "models_pinned": []llm.ModelPin{{Provider: "https://user:secret@provider.invalid/private?key=secret"}, {Provider: "private-provider-value"}, {Provider: "fake-gateway-key"}}}
	ctx := redact.With(context.Background(), redact.New([]string{"private-provider-value"}))
	d := buildDataFlow(ctx, m, l, r)
	raw, _ := json.Marshal(d)
	for _, secret := range []string{"secret", "private-provider-value", "fake-gateway-key", "/private"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("leak", secret, string(raw))
		}
	}
	if !strings.Contains(string(raw), "provider.invalid:443") {
		t.Fatal(string(raw))
	}
	for ref, want := range map[string]string{"qdrant/qdrant:v1": "docker.io:443", "ghcr.io/org/repo@sha256:x": "ghcr.io:443", "registry.invalid:5000/image:tag": "registry.invalid:5000"} {
		if got := imageRegistry(ref); got != want {
			t.Fatal(ref, got, want)
		}
	}
}
