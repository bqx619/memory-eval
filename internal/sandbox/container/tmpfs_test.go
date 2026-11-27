package container

import (
	"encoding/json"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTmpfsDefaultModeAndDisclosure(t *testing.T) {
	b, f := fixture(t)
	s := spec(f)
	s.Container.Tmpfs = []string{"/plain", "/empty:", "/cache:rw,size=16m", "/db:rw,uid=999,gid=999,mode=0700", "/other:mode=0750,uid=1000", "/mode=in-path"}
	want := []string{
		"/tmp:rw,nosuid,nodev,size=1g,mode=1777", "/plain:mode=1777", "/empty:mode=1777", "/cache:rw,size=16m,mode=1777",
		"/db:rw,uid=999,gid=999,mode=0700", "/other:mode=0750,uid=1000", "/mode=in-path:mode=1777",
	}
	h, err := b.Prepare(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Destroy(t.Context(), h)
	e, _ := b.episode(h)
	args, err := b.runArgs(e.config)
	if err != nil {
		t.Fatal(err)
	}
	var mounts []string
	for i, arg := range args {
		if arg == "--tmpfs" {
			mounts = append(mounts, args[i+1])
		}
		if arg == "--shm-size" && args[i+1] != "512m" {
			t.Fatal("shm-size changed", args)
		}
	}
	if !reflect.DeepEqual(mounts, want) {
		t.Fatal(mounts, want)
	}
	disclosure := h.(*sandbox.Prepared).Extra["container"].(map[string]any)
	if !reflect.DeepEqual(disclosure["tmpfs"], mounts) {
		t.Fatal("reported tmpfs differs from create", disclosure["tmpfs"], mounts)
	}
	// Implicit and explicit default permissions mean the same effective policy.
	r := e.config
	r.ExtraTmpfs = []string{"/cache:rw"}
	a, err := b.profile(r, nil, sandbox.NetworkNone)
	if err != nil {
		t.Fatal(err)
	}
	r.ExtraTmpfs = []string{"/cache:rw,mode=1777"}
	z, err := b.profile(r, nil, sandbox.NetworkNone)
	if err != nil || a != z {
		t.Fatal("default mode not normalized in profile", a, z, err)
	}
	r.ExtraTmpfs = []string{"/cache:rw,mode=0700"}
	z, err = b.profile(r, nil, sandbox.NetworkNone)
	if err != nil || a == z {
		t.Fatal("explicit permission missing from profile", a, z, err)
	}
}

func TestScratchProbeTmpfsPermissions(t *testing.T) {
	b, _ := fixture(t)
	image, err := b.ensureProbeImage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"create", "run"} {
		for _, tc := range []struct {
			options  string
			writable bool
		}{
			{"rw,nosuid,nodev,size=1g", false},
			{"rw,nosuid,nodev,size=1g,mode=1777", true},
			{"rw,mode=0700", false},
			{"rw,uid=65532,gid=65532,mode=0700", true},
		} {
			t.Run(op+"/"+tc.options, func(t *testing.T) {
				r := runConfig{Name: "memeval-tmpfs-test", Network: "none", Image: image, User: "65532:65532", EP: t.TempDir(), Cwd: "/tmp", Labels: b.labels("tmpfs-test", "probe", "probe"), Command: []string{"/memeval", "__container-probe", "caps"}, ProbeMount: true}
				args, err := b.runArgs(r)
				if err != nil {
					t.Fatal(err)
				}
				args[0] = op
				for i, v := range args {
					if v == "--tmpfs" {
						args[i+1] = "/tmp:" + tc.options
					}
				}
				raw, err := b.call(t.Context(), args...)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := b.call(t.Context(), "rm", "-f", r.Name); err != nil {
						t.Error(err)
					}
				}()
				if op == "create" {
					raw, err = b.call(t.Context(), "start", "--attach", strings.TrimSpace(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
				}
				var got probeResult
				if err := json.Unmarshal(raw, &got); err != nil || got.TmpWritable != tc.writable || !got.ProbeWritable {
					t.Fatal(string(raw), err)
				}
			})
		}
	}
}

func TestCapabilitiesTmpfsModeRegression(t *testing.T) {
	b, f := fixture(t)
	// Reproduce the previous create argv against the same cached scratch image.
	wrapper := filepath.Join(t.TempDir(), "docker-without-tmpfs-mode")
	quoted, _ := json.Marshal(f.CLI)
	body := "#!" + f.Python + "\nimport os, sys\ntarget = " + string(quoted) + "\nargs = sys.argv[1:]\nfor i in range(len(args)-1):\n    if args[i] == '--tmpfs': args[i+1] = args[i+1].replace(',mode=1777', '')\nos.execv(target, [target] + args)\n"
	if err := os.WriteFile(wrapper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	b.cfg.CLI = wrapper
	caps, err := b.Capabilities(t.Context())
	if err != nil || caps.Enforced || !strings.Contains(caps.Reason, "bind_writable") {
		t.Fatal(caps, err)
	}
	probe := caps.Runtime["probe"].(map[string]any)
	if probe["tmp_writable"] != false || probe["probe_writable"] != true || caps.UserMode != "uid-remapped" {
		t.Fatal(caps)
	}
	for name, ok := range caps.Runtime["checks"].(map[string]bool) {
		if ok != (name != "bind_writable") {
			t.Fatal(name, ok)
		}
	}
	b.cfg.CLI = f.CLI
	caps, err = b.Capabilities(t.Context())
	if err != nil || !caps.Enforced || caps.Runtime["checks"].(map[string]bool)["bind_writable"] != true {
		t.Fatal(caps, err)
	}
	builds := 0
	for _, args := range f.Calls(t) {
		if args[2] == "build" {
			builds++
		}
	}
	if builds != 1 {
		t.Fatal("permission fix must work with the cached probe image", builds)
	}
	t.Log("cached scratch image: omitted mode -> bind_writable=false; mode=1777 -> all five checks true")
}
