package container

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"memeval/internal/procexec"
	"memeval/internal/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSameID(t *testing.T) {
	full := strings.Repeat("a", 64)
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{full, full, true}, {full, full[:12], true}, {full[:12], full, true},
		{full, full[:11], false}, {full[:11], full, false},
		{full, full[:12] + "b", false}, {full, "", false}, {"", "", false},
	} {
		if got := sameID(tc.a, tc.b); got != tc.want {
			t.Errorf("sameID(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
	if got := uniqueIDs([]string{full[:12], full, strings.Repeat("b", 64)}); len(got) != 2 {
		t.Fatal("residue IDs not normalized", got)
	}
}

func TestRecordedContainerIdentityAndVolumeNames(t *testing.T) {
	id := strings.Repeat("a", 64)
	e := &episode{id: "episode", name: "owned", containerID: id[:12], volumes: map[string]bool{}}
	e.root = e
	var v inspection
	if err := json.Unmarshal([]byte(`{"Id":"`+id+`","Name":"/owned","Config":{"Labels":{"memeval.episode":"episode"}},"Mounts":[{"Type":"volume","Name":"same-prefix-volume"},{"Type":"volume","Name":"same-prefix-volume-other"}]}`), &v); err != nil {
		t.Fatal(err)
	}
	if err := e.recordContainer(v); err != nil || e.containerID != id || len(e.volumes) != 2 {
		t.Fatal("canonical ID or exact volume names lost", e, err)
	}
	v.ID = strings.Repeat("b", 64)
	if err := e.recordContainer(v); err == nil {
		t.Fatal("different inspected ID accepted")
	}
	v.ID = id
	v.Config.Labels["memeval.episode"] = "other"
	if err := e.recordContainer(v); err == nil {
		t.Fatal("unowned container accepted")
	}
}

// Simulate a caller using the historical list syntax, not a daemon that
// violates --no-trunc: the wrapper removes that flag before invoking the fake.
func legacyListCLI(t *testing.T, python, target string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy-docker")
	quoted, _ := json.Marshal(target)
	body := "#!" + python + "\nimport os, sys\ntarget = " + string(quoted) + "\nos.execv(target, [target] + [x for x in sys.argv[1:] if x != '--no-trunc'])\n"
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDestroyWithLegacyShortIDs(t *testing.T) {
	for _, lostCreateResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded", true: "recovered"}[lostCreateResponse], func(t *testing.T) {
			b, f := fixture(t)
			// Exercise caps cleanup, SUT, init, sidecar and network through
			// actual short-ID list output; all create responses remain full.
			b.cfg.CLI = legacyListCLI(t, f.Python, f.CLI)
			s := spec(f)
			s.Services = []sandbox.Service{{Name: "fixture"}}
			f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_VOLUME": "1"})
			h, err := b.Prepare(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			e, _ := b.episode(h)
			for _, role := range []string{"init", "sidecar"} {
				if _, err := b.PrepareServiceCommand(t.Context(), h, sandbox.Service{Name: role}, s.Command, s.Env, role); err != nil {
					t.Fatal(err)
				}
			}
			if lostCreateResponse {
				e.containerID = ""
				e.volumes = map[string]bool{}
				for _, child := range e.children {
					child.containerID = ""
				}
			}
			f.Control(t, nil)
			if err := b.Destroy(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			residue, err := b.verifyEpisodeEmpty(t.Context(), e)
			if err != nil || residue["run_clean"] != true || len(e.volumes) != 3 {
				t.Fatal(residue, e.volumes, err)
			}
			for _, args := range f.Calls(t) {
				if contains(args, "--no-trunc") {
					t.Fatal("legacy list regression did not use short IDs", args)
				}
			}
			// The fixture rejects inspect/rm of these resources as well.
			raw, err := os.ReadFile(filepath.Join(f.State, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]map[string]any
			if err := json.Unmarshal(raw, &state); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"container", "network", "volume"} {
				if len(state[kind+"s"]) != 1 || state[kind+"s"]["other-"+kind] == nil {
					t.Fatal("residue or unrelated resource removed", kind, state[kind+"s"])
				}
			}
			t.Logf("short-ID caps/Destroy/recovery: clean=%v; 3 anonymous volumes removed", residue["run_clean"])
		})
	}
}

func TestRemoveContainerMissingFromList(t *testing.T) {
	for _, mode := range []string{"present", "already-absent", "rm-fails"} {
		t.Run(mode, func(t *testing.T) {
			b, f := fixture(t)
			h, err := b.Prepare(t.Context(), spec(f))
			if err != nil {
				t.Fatal(err)
			}
			e, _ := b.episode(h)
			if mode == "already-absent" {
				if _, err := b.call(t.Context(), "rm", "-f", e.containerID); err != nil {
					t.Fatal(err)
				}
			}
			control := map[string]string{"FAKE_DOCKER_HIDE_LIST": "ps"}
			if mode == "rm-fails" {
				control["FAKE_DOCKER_FAIL"] = "rm"
			}
			f.Control(t, control)
			before := len(f.Calls(t))
			err = b.removeContainer(t.Context(), e)
			if mode == "rm-fails" {
				if err == nil || e.removed {
					t.Fatal("failed rm claimed removal", err, e.removed)
				}
			} else if err != nil || !e.removed {
				t.Fatal(err, e.removed)
			}
			calls := f.Calls(t)[before:]
			last := calls[len(calls)-1]
			if strings.Join(last[2:], " ") != "rm -f "+e.containerID {
				t.Fatal("missing scoped list entry skipped rm", calls)
			}
			f.Control(t, nil)
			if err := b.Destroy(t.Context(), h); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFakeDockerWireContract(t *testing.T) {
	b, f := fixture(t)
	call := func(args ...string) string {
		t.Helper()
		raw, err := b.call(t.Context(), args...)
		if err != nil {
			t.Fatal(args, err)
		}
		return string(raw)
	}
	fullID := func(raw string) string {
		t.Helper()
		id := strings.TrimSuffix(raw, "\n")
		if _, err := hex.DecodeString(id); err != nil || len(id) != 64 || raw != id+"\n" {
			t.Fatalf("expected full hex ID and newline, got %q", raw)
		}
		return id
	}
	const ep = "wire-contract"
	filter := "label=memeval.episode=" + ep
	networkID := fullID(call("network", "create", "--label", "memeval.episode="+ep, "memeval-wire-net"))
	dir := t.TempDir()
	args, err := b.runArgs(runConfig{Name: "memeval-wire", Network: "memeval-wire-net", Image: "fixture:image", User: "65532:65532", EP: dir, Cwd: dir, Labels: b.labels(ep, "fixture", "sut"), Command: []string{f.Python, "-c", "pass"}})
	if err != nil {
		t.Fatal(err)
	}
	id := fullID(call(args...))
	for _, tc := range []struct {
		kind, id string
		args     []string
	}{
		{"container", id, []string{"ps", "-a", "--filter", filter, "-q"}},
		{"network", networkID, []string{"network", "ls", "--filter", filter, "-q"}},
	} {
		if got := call(tc.args...); got != tc.id[:12]+"\n" {
			t.Fatalf("short %s list = %q", tc.kind, got)
		}
		if got := call(append(tc.args, "--no-trunc")...); got != tc.id+"\n" {
			t.Fatalf("full %s list = %q", tc.kind, got)
		}
		ids, err := b.list(t.Context(), tc.kind, ep)
		if err != nil || len(ids) != 1 || ids[0] != tc.id {
			t.Fatal("production list must request full IDs", ids, err)
		}
		for _, ref := range []string{tc.id, tc.id[:12]} {
			args := []string{tc.kind, "inspect", ref}
			var entries []inspection
			if err := json.Unmarshal([]byte(call(args...)), &entries); err != nil || len(entries) != 1 || entries[0].ID != tc.id {
				t.Fatal("inspect must accept IDs and return array", entries, err)
			}
		}
	}
	call("start", "--attach", id[:12])
	if got := call("wait", id); got != "0\n" {
		t.Fatalf("wait output = %q", got)
	}
	// Seed a named volume; its name must never be abbreviated like an ID.
	statePath := filepath.Join(f.State, "state.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	const volume = "memeval-volume-name-longer-than-twelve"
	state["volumes"][volume] = map[string]any{"Name": volume, "Labels": map[string]string{"memeval.episode": ep}}
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if got := call("volume", "ls", "--filter", filter, "-q"); got != volume+"\n" {
		t.Fatalf("volume name truncated: %q", got)
	}
	call("rm", "-f", id[:12])
	call("network", "rm", networkID[:12])
	call("volume", "rm", volume)
	var stderr bytes.Buffer
	cmd := b.command(t.Context(), "rm", "-f", id)
	cmd.Stderr = &stderr
	if raw, err := procexec.Output(cmd); err != nil || len(raw) != 0 || stderr.Len() != 0 {
		t.Fatal("already absent forced remove must succeed silently", err, stderr.String(), string(raw))
	}
}

func TestDocker29InspectMissingWire(t *testing.T) {
	b, f := fixture(t)
	for _, kind := range []string{"", "container", "network", "volume", "image"} {
		name := kind
		if name == "" {
			name = "generic"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"inspect", "missing-resource"}
			want := "error: no such object: missing-resource\n"
			if kind != "" {
				args = append([]string{kind}, args...)
				want = "Error response from daemon: No such " + kind + ": missing-resource\n"
			}
			var stderr bytes.Buffer
			cmd := b.command(t.Context(), args...)
			cmd.Stderr = &stderr
			raw, err := procexec.Output(cmd)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || string(raw) != "[]\n" || stderr.String() != want {
				t.Fatalf("args=%v err=%v stdout=%q stderr=%q", args, err, raw, stderr.String())
			}
			_, err = b.call(t.Context(), args...)
			var diagnostic *CommandError
			if !errors.As(err, &diagnostic) || diagnostic.Stderr != want || !notFound(err) || !containerNotFound(err) {
				t.Fatal("missing-resource diagnostic lost", diagnostic, err)
			}
			for _, text := range []string{strings.ToLower(want), strings.ToUpper(want)} {
				if !notFound(errors.New(text)) || !containerNotFound(errors.New(text)) {
					t.Fatal("case-dependent absence check", text)
				}
			}
			t.Logf("stdout=%q stderr=%q exit=1", raw, diagnostic.Stderr)
		})
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_FAIL": "container inspect"})
	_, err := b.inspectContainer(t.Context(), "missing-resource")
	if err == nil || notFound(err) || containerNotFound(err) || notFound(nil) || containerNotFound(nil) {
		t.Fatal("an inspect failure is not proof of absence", err)
	}
}
