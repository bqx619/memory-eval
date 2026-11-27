package container

import (
	"encoding/json"
	"errors"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container/governor"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRewriteGatewayURL(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"http://127.0.0.1:8317/v1", "http://llm-gateway:8317/v1"},
		{"http://127.0.0.1/v1/", "http://llm-gateway:80/v1/"},
		{"http://[::1]:8080/v%31", "http://llm-gateway:8080/v%31"},
	} {
		u, _ := url.Parse(tc.src)
		env := map[string]string{"MATCH": tc.src, "TEMPLATE": "{llm.base_url}", "OTHER": tc.src + "/unmatched", "KEY": "fake-gateway-key"}
		got := rewriteGatewayURL(env, u)
		if got["MATCH"] != tc.want || got["TEMPLATE"] != tc.want || got["OTHER"] != env["OTHER"] || got["KEY"] != env["KEY"] || env["MATCH"] != tc.src {
			t.Fatal(got, env)
		}
	}
	for _, base := range []string{"https://127.0.0.1", "http://example.invalid", "http://127.0.0.1:0"} {
		u, _ := url.Parse(base)
		if _, e := gatewayTarget(u); e == nil {
			t.Fatal("invalid relay target accepted", base)
		}
	}
}

func TestGatewayProfileIdentity(t *testing.T) {
	b := New(Config{})
	r := runConfig{Name: "sut-a", Network: "episode-a", Image: "fixture:image", User: "65532:65532", EP: "/tmp/a", Cwd: "/tmp/a/work", RelayTarget: "127.0.0.1:8317", RelayImage: "memeval/probe:fixture"}
	want, err := b.profile(r, nil, sandbox.NetworkGateway)
	if err != nil {
		t.Fatal(err)
	}
	r.Name, r.Network, r.EP, r.Cwd = "sut-b", "episode-b", "/tmp/b", "/tmp/b/work"
	if got, err := b.profile(r, nil, sandbox.NetworkGateway); err != nil || got != want {
		t.Fatal("instance names changed gateway policy", err)
	}
	for _, change := range []func(*runConfig){
		func(r *runConfig) { r.RelayTarget = "127.0.0.2:8317" },
		func(r *runConfig) { r.RelayImage = "memeval/probe:changed" },
	} {
		other := r
		change(&other)
		if got, err := b.profile(other, nil, sandbox.NetworkGateway); err != nil || got == want {
			t.Fatal("gateway policy identity ignored relay", err)
		}
	}
}

func TestGatewayLifecycleAndSecrets(t *testing.T) {
	b, f := fixture(t)
	s := spec(f)
	s.Network = sandbox.NetworkGateway
	s.GatewayURL, _ = url.Parse("http://127.0.0.1:8317/v1")
	s.Services = []sandbox.Service{{Name: "test"}}
	s.Env["OPENAI_BASE_URL"] = s.GatewayURL.String()
	s.Env["OPENAI_API_KEY"] = "fake-gateway-key"
	cfg := governor.Defaults()
	if err := b.ConfigureGovernor(t.TempDir(), cfg); err != nil {
		t.Fatal(err)
	}
	h, err := b.Prepare(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := b.episode(h)
	if !e.internal || e.governor == nil || h.(*sandbox.Prepared).Spec.Env["OPENAI_BASE_URL"] != "http://llm-gateway:8317/v1" {
		t.Fatal("missing governor/internal network")
	}
	gov := e.governor
	v, err := b.inspectContainer(t.Context(), gov.name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gov.id, "governor-") || gov.id == e.id ||
		v.Config.Labels["memeval.episode"] != gov.id || v.Config.Labels["memeval.env"] != "probe" ||
		v.Config.Labels["memeval.probe"] == "" {
		t.Fatal("governor must override image ownership and inherit its probe fingerprint", gov.id, v.Config.Labels)
	}
	assertRunGovernor := func(want bool) {
		t.Helper()
		resources, err := b.Resources(t.Context(), "container", "memeval.run", b.runID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range resources {
			found = found || r.Name == gov.name
		}
		if found != want {
			t.Fatalf("run governor present=%v, want=%v: %+v", found, want, resources)
		}
	}
	assertRunGovernor(true)
	ids, err := b.list(t.Context(), "container", e.id)
	if err != nil || !containsID(ids, e.containerID) || containsID(ids, gov.containerID) {
		t.Fatal("episode inventory must contain SUT, never governor", ids, err)
	}
	for _, role := range []string{"init", "sidecar"} {
		if _, err := b.PrepareServiceCommand(t.Context(), h, sandbox.Service{Name: role}, s.Command, s.Env, role); err != nil {
			t.Fatal(err)
		}
	}
	var args []string
	connected, ready := false, false
	for _, a := range f.Calls(t) {
		if strings.Contains(strings.Join(a, " "), "fake-gateway-key") || contains(a, "-p") {
			t.Fatal(a)
		}
		if a[2] == "create" && contains(a, "__container-governor") {
			args = a
		}
		if contains(a, "__container-governor-status") {
			ready = true
		}
		if strings.Join(a[2:], " ") == "network connect --alias llm-gateway "+e.network+" "+e.governor.name {
			connected = true
		}
	}
	for _, want := range []string{"--read-only", "--cap-drop", "ALL", "no-new-privileges", "65532:65532", "--pids-limit", "64", "bridge", "memeval.role=governor", "0.0.0.0:8317", "127.0.0.1:8317", "--meter-dir", "/meter"} {
		if !contains(args, want) {
			t.Fatal(want, args)
		}
	}
	if !contains(args, "memeval.episode="+gov.id) || !contains(args, "memeval.env=probe") {
		t.Fatal("governor image ownership was not explicitly overridden", args)
	}
	if !ready || !connected || !contains(args, "-v") || contains(args, "--env-file") {
		t.Fatal(args, ready, connected)
	}
	raw, _ := json.Marshal(h.EffectiveConfig())
	if strings.Contains(string(raw), "fake-gateway-key") || h.EffectiveConfig()["relay_name"] != e.governor.name {
		t.Fatal(string(raw))
	}
	before := len(f.Calls(t))
	if err = b.Destroy(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if residue, err := b.verifyEpisodeEmpty(t.Context(), e); err != nil || residue["run_clean"] != true {
		t.Fatal("live governor leaked into episode residue", residue, err)
	}
	assertRunGovernor(true)
	want := []string{e.containerID, e.children[1].containerID, e.children[0].containerID, "disconnect", "network"}
	got := []string{}
	for _, a := range f.Calls(t)[before:] {
		if a[2] == "rm" {
			got = append(got, a[len(a)-1])
		}
		if a[2] == "network" && a[3] == "disconnect" {
			got = append(got, "disconnect")
		}
		if a[2] == "network" && a[3] == "rm" {
			got = append(got, "network")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got, want)
	}
	// A second episode reuses the same governor and cleanly disconnects again.
	h2, err := b.Prepare(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := b.episode(h2)
	if e2.governor != e.governor {
		t.Fatal("not run scoped")
	}
	if err = b.Destroy(t.Context(), h2); err != nil {
		t.Fatal(err)
	}
	assertRunGovernor(true)
	// Recovery must find the governor by its own episode label even if the
	// successful create response was lost, without inspecting other resources.
	gov.containerID = ""
	if err = b.finishGovernor(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRunGovernor(false)
	if err = b.FinishRun(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = b.FinishRun(t.Context()); err != nil {
		t.Fatal("not idempotent", err)
	}
	state, _ := os.ReadFile(filepath.Join(f.State, "state.json"))
	var table map[string]json.RawMessage
	json.Unmarshal(state, &table)
	var cs map[string]any
	json.Unmarshal(table["containers"], &cs)
	if len(cs) != 1 || cs["other-container"] == nil {
		t.Fatal(string(state))
	}
	bill, err := b.GovernorBilling()
	if err != nil || bill.Calls != 0 {
		t.Fatal(bill, err)
	}
	if b.Report(t.Context())["governor"] == nil {
		t.Fatal("missing disclosure")
	}
}
func TestGatewayFailureAndModes(t *testing.T) {
	for _, failure := range []string{"none-required", "open", "network connect", "ready", "disconnect", "residue", "volume"} {
		t.Run(failure, func(t *testing.T) {
			b, f := fixture(t)
			s := spec(f)
			s.Network = sandbox.NetworkGateway
			s.GatewayURL, _ = url.Parse("http://127.0.0.1:8317/v1")
			s.Env["OPENAI_BASE_URL"] = s.GatewayURL.String()
			if failure == "none-required" {
				s.Network = sandbox.NetworkNone
				s.RequiresLLM = true
				if _, err := b.Prepare(t.Context(), s); err == nil || !strings.Contains(err.Error(), "requires gateway or open") {
					t.Fatal(err)
				}
				return
			}
			if failure == "open" {
				s.Network = sandbox.NetworkOpen
			}
			if failure == "network connect" {
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": failure})
			}
			if failure == "ready" {
				f.Control(t, map[string]string{"FAKE_DOCKER_READY_FAIL": "1"})
			}
			h, err := b.Prepare(t.Context(), s)
			if failure == "network connect" || failure == "ready" {
				var denied *sandbox.Unenforceable
				if !errors.As(err, &denied) {
					t.Fatal(err)
				}
				f.Control(t, nil)
				if err = b.FinishRun(t.Context()); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ep, _ := b.episode(h)
			if failure == "open" && ep.governor != nil {
				t.Fatal("open started governor")
			}
			if failure == "disconnect" {
				f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "network disconnect"})
				if err = b.Destroy(t.Context(), h); err == nil {
					t.Fatal("disconnect failure ignored")
				}
				f.Control(t, nil)
			}
			if err = b.Destroy(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			if failure == "residue" {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_RELAY": "1"})
			}
			if failure == "volume" {
				v := "governor-anonymous"
				ep.governor.volumes[v] = true
				raw, _ := os.ReadFile(filepath.Join(f.State, "state.json"))
				var state map[string]any
				json.Unmarshal(raw, &state)
				state["volumes"].(map[string]any)[v] = map[string]any{"Name": v, "Labels": map[string]string{}}
				raw, _ = json.Marshal(state)
				os.WriteFile(filepath.Join(f.State, "state.json"), raw, 0600)
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_VOLUME": "1"})
			}
			err = b.FinishRun(t.Context())
			if failure == "residue" || failure == "volume" {
				var denied *sandbox.Unenforceable
				if !errors.As(err, &denied) {
					t.Fatal("residue counted clean", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
