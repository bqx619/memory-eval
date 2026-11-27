package container

import (
	"encoding/json"
	"errors"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCleanupRecoveryAndFailure(t *testing.T) {
	for _, leave := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovered", true: "refused"}[leave], func(t *testing.T) {
			b, f := fixture(t)
			b.BeginRun("run-cleanup-test")
			s := spec(f)
			s.Network = sandbox.NetworkOpen
			h, e := b.Prepare(t.Context(), s)
			if e != nil {
				t.Fatal(e)
			}
			if e = b.Destroy(t.Context(), h); e != nil {
				t.Fatal(e)
			}
			cfg := h.EffectiveConfig()
			if cfg["destroyed"] != true || cfg["destroy_ms"] == nil {
				t.Fatal(cfg)
			}
			// A crashed auxiliary process left an owned container after episode cleanup.
			raw, e := os.ReadFile(filepath.Join(f.State, "state.json"))
			if e != nil {
				t.Fatal(e)
			}
			var state map[string]map[string]any
			if e = json.Unmarshal(raw, &state); e != nil {
				t.Fatal(e)
			}
			state["containers"]["memeval-orphan"] = map[string]any{"Id": strings.Repeat("a", 64), "Name": "/memeval-orphan", "Config": map[string]any{"Labels": b.labels("orphan", "fixture@lock", "sidecar")}, "State": map[string]any{"Running": false}}
			raw, _ = json.Marshal(state)
			os.WriteFile(filepath.Join(f.State, "state.json"), raw, 0600)
			if leave {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_CONTAINER": "1"})
			}
			e = b.FinishRun(t.Context())
			var isolation *sandbox.Unenforceable
			if leave != errors.As(e, &isolation) {
				t.Fatal(e)
			}
			r := b.Report(t.Context())["residue"].(map[string]any)
			if r["run_clean"] == leave || r["system_df"] == nil {
				t.Fatal(r)
			}
			if leave && !strings.Contains(e.Error(), "memeval-orphan") {
				t.Fatal(e)
			}
			for _, a := range f.Calls(t) {
				if strings.Contains(strings.Join(a, " "), "other-") {
					t.Fatal("touched unrelated resource", a)
				}
			}
		})
	}
}
func TestDestroyFailureDisclosesTiming(t *testing.T) {
	b, f := fixture(t)
	h, e := b.Prepare(t.Context(), spec(f))
	if e != nil {
		t.Fatal(e)
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_VOLUME": "1"})
	// A direct failed rm is an isolation error, never a destroyed=true claim.
	f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "rm"})
	e = b.Destroy(t.Context(), h)
	var isolation *sandbox.Unenforceable
	if !errors.As(e, &isolation) || h.EffectiveConfig()["destroyed"] != false || h.EffectiveConfig()["destroy_ms"] == nil {
		t.Fatal(e, h.EffectiveConfig())
	}
	f.Control(t, nil)
	if e = b.Destroy(t.Context(), h); e != nil {
		t.Fatal(e)
	}
}

func TestVolumeNamesAreNotTruncatedIDs(t *testing.T) {
	b, f := fixture(t)
	if _, e := b.Resources(t.Context(), "volume", "memeval.run", "v"); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(f.State, "state.json")
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var state map[string]map[string]any
	json.Unmarshal(raw, &state)
	for _, name := range []string{"memeval-volume-prefix", "memeval-volume-prefix-more"} {
		state["volumes"][name] = map[string]any{"Name": name, "Labels": map[string]string{"memeval.run": "v"}}
	}
	raw, _ = json.Marshal(state)
	os.WriteFile(path, raw, 0600)
	rows, e := b.Resources(t.Context(), "volume", "memeval.run", "v")
	if e != nil || len(rows) != 2 {
		t.Fatal(e, rows)
	}
	_, left, e := b.CleanupScope(t.Context(), "memeval.run", "v")
	if e != nil || len(left) != 0 {
		t.Fatal(e, left)
	}
}
