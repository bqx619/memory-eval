package sut

import (
	"encoding/json"
	"memeval/internal/sandbox/container"
	"memeval/internal/testoracle"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func lifecycleFixture(t *testing.T) (*container.Backend, testoracle.DockerFixture, string) {
	t.Helper()
	f := testoracle.FakeDocker(t)
	b := container.New(container.Config{CLI: f.CLI, LinuxBinary: f.LinuxBinary, Platform: f.Platform})
	if _, e := b.ImageCommand(t.Context(), "images", "--filter", "label=memeval.env", "-q"); e != nil {
		t.Fatal(e)
	}
	root, e := CacheRoot(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return b, f, root
}
func mutateState(t *testing.T, f testoracle.DockerFixture, fn func(map[string]map[string]any)) {
	t.Helper()
	p := filepath.Join(f.State, "state.json")
	raw, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var state map[string]map[string]any
	if e = json.Unmarshal(raw, &state); e != nil {
		t.Fatal(e)
	}
	fn(state)
	raw, _ = json.Marshal(state)
	if e = os.WriteFile(p, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func cleanupResource(kind, name string, labels map[string]string, created time.Time) map[string]any {
	r := map[string]any{"Id": strings.Repeat("a", 32) + name, "Name": name, "Created": created.Format(time.RFC3339Nano), "Labels": labels}
	if kind == "container" || kind == "image" {
		delete(r, "Labels")
		r["Config"] = map[string]any{"Labels": labels}
		r["State"] = map[string]any{"Running": false}
	}
	return r
}
func TestGCScopedPlanApplyAndAge(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	now := time.Now().UTC()
	ep := t.TempDir()
	start, _ := container.OwnerStart(t.Context(), os.Getpid())
	mutateState(t, f, func(s map[string]map[string]any) {
		for _, kind := range []string{"container", "network", "volume"} {
			for _, tc := range []struct {
				name   string
				age    time.Duration
				labels map[string]string
			}{
				{"old", 24 * time.Hour, map[string]string{"memeval.run": "abandoned"}},
				{"young", 24*time.Hour - time.Second, map[string]string{"memeval.run": "recent"}},
				{"alive", 48 * time.Hour, map[string]string{"memeval.run": "active", "memeval.owner_pid": strconv.Itoa(os.Getpid()), "memeval.owner_start": start}},
				{"dead", time.Minute, map[string]string{"memeval.run": "dead", "memeval.owner_pid": "999999999"}},
			} {
				n := "memeval-" + kind + "-" + tc.name
				s[kind+"s"][n] = cleanupResource(kind, n, tc.labels, now.Add(-tc.age))
			}
			s[kind+"s"]["memeval-unlabelled"] = cleanupResource(kind, "memeval-unlabelled", map[string]string{}, now.Add(-48*time.Hour))
		}
		s["images"]["memeval/env:orphan"] = cleanupResource("image", "orphan", map[string]string{"memeval.env": "orphan@aaaaaaaaaaaa"}, now)
	})
	o := GCOptions{Cache: cache, EPRoot: ep, Now: now, OlderThan: 24 * time.Hour}
	r, e := GarbageCollect(t.Context(), b, o)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, v := range r.Items {
		if v.Action == "DELETE" {
			count++
		}
		if strings.Contains(v.Name, "alive") && v.Action != "KEEP" {
			t.Fatal(v)
		}
	}
	if count != 7 {
		t.Fatal(count, r)
	}
	beforeCalls := f.Calls(t)
	for _, a := range beforeCalls {
		if a[2] == "rm" || a[2] == "rmi" {
			t.Fatal("dry run mutated", a)
		}
	}
	o.Apply = true
	r, e = GarbageCollect(t.Context(), b, o)
	if e != nil || len(r.Remaining) != 0 {
		t.Fatal(e, r)
	}
	for _, a := range f.Calls(t) {
		text := strings.Join(a, " ")
		if strings.Contains(text, "other-") {
			t.Fatal("unrelated resource inspected", a)
		}
		if strings.Contains(text, "inspect memeval-unlabelled") {
			t.Fatal(a)
		}
	}
	t.Logf("dry candidates=%d; apply remaining=%v; live owners retained", count, r.Remaining)
}
func TestGCHostPathsAndLocks(t *testing.T) {
	b, _, cache := lifecycleFixture(t)
	ep := t.TempDir()
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	for _, p := range []string{filepath.Join(ep, "abcdef12"), filepath.Join(ep, "build-old"), filepath.Join(ep, "keep-evidence")} {
		os.MkdirAll(p, 0700)
		os.Chtimes(p, old, old)
	}
	os.Symlink(t.TempDir(), filepath.Join(ep, "01234567"))
	gate := filepath.Join(cache, "env-gates", "orphan@a.lock")
	writeFixture(t, gate, "")
	os.Chtimes(gate, old, old)
	valid := filepath.Join(cache, "env-gates", "valid@a.lock")
	writeFixture(t, valid, "")
	os.Chtimes(valid, old, old)
	os.MkdirAll(filepath.Join(cache, "envs", "valid@a"), 0700)
	record := filepath.Join(cache, "selftests", "selftest-old.json")
	writeFixture(t, record, `{"created_at":"2000-01-01T00:00:00Z"}`)
	os.Chtimes(record, old, old)
	r, e := GarbageCollect(t.Context(), b, GCOptions{Cache: cache, EPRoot: ep, Now: now, OlderThan: 24 * time.Hour, Apply: true})
	if e != nil {
		t.Fatal(e, r)
	}
	for _, p := range []string{filepath.Join(ep, "abcdef12"), filepath.Join(ep, "build-old"), gate, record} {
		if _, e := os.Lstat(p); !os.IsNotExist(e) {
			t.Fatal("not deleted", p)
		}
	}
	for _, p := range []string{filepath.Join(ep, "01234567"), filepath.Join(ep, "keep-evidence"), valid} {
		if _, e := os.Lstat(p); e != nil {
			t.Fatal("must retain", p, e)
		}
	}
}
func TestDestroyEnvironmentImageVerificationAndGate(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(strconv.FormatBool(stuck), func(t *testing.T) {
			b, f, cache := lifecycleFixture(t)
			sha := strings.Repeat("a", 64)
			identity := "fixture@" + sha[:12]
			dir := filepath.Join(cache, "envs", "fixture@"+sha)
			os.MkdirAll(dir, 0700)
			mutateState(t, f, func(s map[string]map[string]any) {
				for _, kind := range []string{"container", "network", "volume", "image"} {
					name := "memeval-" + kind
					s[kind+"s"][name] = cleanupResource(kind, name, map[string]string{"memeval.env": identity}, time.Now())
				}
			})
			release, e := environmentGate(dir, false)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = DestroyEnvironments(t.Context(), b, cache, "fixture", false); e == nil || !strings.Contains(e.Error(), "gate busy") {
				t.Fatal(e)
			}
			release()
			if stuck {
				f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_IMAGE": "1"})
			}
			r, e := DestroyEnvironments(t.Context(), b, cache, "fixture", false)
			if stuck {
				if e == nil || r.Clean {
					t.Fatal(r, e)
				}
				if _, e = os.Stat(dir); e != nil {
					t.Fatal("failed cleanup removed lock", e)
				}
				return
			}
			if e != nil || !r.Clean {
				t.Fatal(e, r)
			}
			if _, e = os.Stat(dir); !os.IsNotExist(e) {
				t.Fatal(e)
			}
			again, e := DestroyEnvironments(t.Context(), b, cache, "fixture", false)
			if e != nil || !again.Clean {
				t.Fatal(e, again)
			}
			for _, a := range f.Calls(t) {
				if strings.Contains(strings.Join(a, " "), "other-") {
					t.Fatal(a)
				}
			}
			t.Logf("destroy steps=%v", r.Steps)
		})
	}
}
func TestGCFailedRemovalAndBaseImageProtection(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	ref := BaseImages()["node"].Ref + "@" + BaseImages()["node"].Digest
	mutateState(t, f, func(s map[string]map[string]any) {
		im := cleanupResource("image", "base", nil, time.Now())
		im["_preset"] = true
		im["RootFS"] = map[string]any{"Layers": []string{"base-layer"}}
		s["images"][ref] = im
		env := cleanupResource("image", "env", map[string]string{"memeval.env": "orphan@abc"}, time.Now())
		env["RootFS"] = map[string]any{"Layers": []string{"base-layer", "env-layer"}}
		s["images"]["memeval/env:orphan"] = env
	})
	o := GCOptions{Cache: cache, EPRoot: t.TempDir(), OlderThan: 24 * time.Hour, BaseImages: true}
	r, e := GarbageCollect(t.Context(), b, o)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, v := range r.Items {
		if v.Name == "base" {
			found = true
			if v.Action != "KEEP" {
				t.Fatal(v)
			}
		}
	}
	if !found {
		t.Fatal(r)
	}
	f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_IMAGE": "1"})
	o.Apply = true
	r, e = GarbageCollect(t.Context(), b, o)
	if e == nil || len(r.Remaining) == 0 {
		t.Fatal(e, r)
	}
}

func TestDestroyAllRetainsCurrentProbeAndRemovesRecords(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	sha, e := b.CurrentProbeSHA()
	if e != nil {
		t.Fatal(e)
	}
	hash := strings.Repeat("b", 64)
	dir := filepath.Join(cache, "envs", "fixture@"+hash)
	os.MkdirAll(dir, 0700)
	record := filepath.Join(cache, "selftests", "selftest-fixture-none-old.json")
	writeFixture(t, record, `{"sut":"fixture"}`)
	other := filepath.Join(cache, "selftests", "selftest-fixture-other-none-old.json")
	writeFixture(t, other, `{"sut":"fixture-other"}`)
	mutateState(t, f, func(s map[string]map[string]any) {
		for name, digest := range map[string]string{"memeval/probe:current": sha, "memeval/probe:old": strings.Repeat("0", 64)} {
			s["images"][name] = cleanupResource("image", name, map[string]string{"memeval.probe": digest, "memeval.env": "probe"}, time.Now())
		}
	})
	r, e := DestroyEnvironments(t.Context(), b, cache, "", true)
	if e != nil || !r.Clean {
		t.Fatal(r, e)
	}
	probes, e := b.Resources(t.Context(), "image", "memeval.probe", "")
	if e != nil || len(probes) != 1 || probes[0].Labels["memeval.probe"] != sha {
		t.Fatal(e, probes)
	}
	if _, e = os.Stat(record); !os.IsNotExist(e) {
		t.Fatal("selftest retained", e)
	}
	if _, e = os.Stat(other); e != nil {
		t.Fatal("prefix-colliding record removed", e)
	}
}
func TestGCValidLockAndUnusedBase(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	root := t.TempDir()
	hash := strings.Repeat("c", 64)
	dir := filepath.Join(cache, "envs", "fixture@"+hash)
	id := "sha256:" + strings.Repeat("1", 64)
	lock := Lock{Version: 3, Name: "fixture", LockSHA256: hash, EnvDir: dir}
	lock.Image.ID = id
	raw, _ := json.Marshal(lock)
	writeFixture(t, filepath.Join(dir, "env.lock.json"), string(raw))
	ref := BaseImages()["node"].Ref + "@" + BaseImages()["node"].Digest
	mutateState(t, f, func(s map[string]map[string]any) {
		im := cleanupResource("image", "locked", map[string]string{"memeval.env": "fixture@" + hash[:12]}, time.Now())
		im["Id"] = id
		im["RootFS"] = map[string]any{"Layers": []string{"unrelated-layer"}}
		s["images"]["memeval/env:locked"] = im
		base := cleanupResource("image", "unused-base", map[string]string{}, time.Now())
		base["RootFS"] = map[string]any{"Layers": []string{"node-base"}}
		base["_preset"] = true
		s["images"][ref] = base
	})
	r, e := GarbageCollect(t.Context(), b, GCOptions{Apply: true, Cache: cache, EPRoot: root, OlderThan: 24 * time.Hour, BaseImages: true})
	if e != nil {
		t.Fatal(e, r)
	}
	if _, e = b.Resource(t.Context(), "image", id); e != nil {
		t.Fatal("locked image removed", e)
	}
	found := false
	for _, v := range r.Items {
		if v.Reason == "unused-approved-image" && strings.Contains(v.Result, "verified absent") {
			found = true
		}
	}
	if !found {
		t.Fatal(r)
	}
	if _, e = os.Stat(filepath.Join(dir, "env.lock.json")); e != nil {
		t.Fatal(e)
	}
}

func TestGCRetainsEpisodeWhenContainerRemovalFails(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	epRoot := t.TempDir()
	ep := filepath.Join(epRoot, "1234abcd")
	os.Mkdir(ep, 0700)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(ep, old, old)
	mutateState(t, f, func(s map[string]map[string]any) {
		r := cleanupResource("container", "memeval-stuck", map[string]string{"memeval.owner_pid": "999999999", "memeval.run": "stuck"}, old)
		r["Mounts"] = []map[string]string{{"Type": "bind", "Source": ep}}
		s["containers"]["memeval-stuck"] = r
	})
	f.Control(t, map[string]string{"FAKE_DOCKER_LEAVE_CONTAINER": "1"})
	r, e := GarbageCollect(t.Context(), b, GCOptions{Apply: true, Cache: cache, EPRoot: epRoot, OlderThan: 24 * time.Hour})
	if e == nil || len(r.Remaining) == 0 {
		t.Fatal(e, r)
	}
	if _, e = os.Stat(ep); e != nil {
		t.Fatal("deleted live bind contents after failed container removal", e)
	}
}

func TestGCActiveContainerProtectsOrphanImage(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	now := time.Now()
	protected := map[string]bool{}
	mutateState(t, f, func(s map[string]map[string]any) {
		for _, name := range []string{"env-reference", "image-reference", "unused"} {
			im := cleanupResource("image", name, map[string]string{"memeval.env": name + "@aaaaaaaaaaaa"}, now)
			s["images"]["memeval/env:"+name] = im
			if name == "unused" {
				continue
			}
			// Missing owner metadata must not hide a running container reference.
			labels := map[string]string{"memeval.env": name + "@aaaaaaaaaaaa"}
			if name == "image-reference" {
				labels = map[string]string{"memeval.run": "another-run"}
			}
			c := cleanupResource("container", "memeval-"+name, labels, now)
			c["State"] = map[string]any{"Running": true}
			if name == "image-reference" {
				c["Image"] = im["Id"]
			}
			s["containers"]["memeval-"+name] = c
			protected[im["Id"].(string)] = true
		}
	})
	o := GCOptions{Cache: cache, EPRoot: t.TempDir(), Now: now, OlderThan: 24 * time.Hour}
	for _, apply := range []bool{false, true} {
		o.Apply = apply
		r, e := GarbageCollect(t.Context(), b, o)
		if e != nil {
			t.Fatal(e, r)
		}
		kept := 0
		for _, item := range r.Items {
			if item.Resource != nil && item.Kind == "image" && protected[item.Resource.ID] {
				if item.Action != "KEEP" || item.Reason != "active-container-reference" {
					t.Fatal(item)
				}
				kept++
			}
		}
		if kept != 2 {
			t.Fatal("active orphan images not protected", r)
		}
	}
	for id := range protected {
		if _, e := b.Resource(t.Context(), "image", id); e != nil {
			t.Fatal("active image deleted", e)
		}
		for _, call := range f.Calls(t) {
			if strings.Contains(strings.Join(call, " "), "rmi -f "+id) {
				t.Fatal("attempted active image removal", call)
			}
		}
	}
	t.Log("unlocked images referenced by running containers: KEEP in plan and apply (env label and image ID); unused orphan removed")
}

func TestGCFailedInstallDirectories(t *testing.T) {
	b, f, cache := lifecycleFixture(t)
	now := time.Now().Truncate(time.Second)
	old := now.Add(-24 * time.Hour)
	dirs := map[string]string{}
	for _, name := range []string{"expired", "recent", "recent-marker", "installed", "no-marker", "active", "symlink-marker"} {
		dir := filepath.Join(cache, "envs", name+"@"+strings.Repeat("a", 64))
		dirs[name] = dir
		writeFixture(t, filepath.Join(dir, "build.log"), "diagnostics")
		if name != "no-marker" && name != "symlink-marker" {
			marker := filepath.Join(dir, "install-failure.log")
			writeFixture(t, marker, "failed build")
			stamp := old
			if name == "recent-marker" {
				stamp = now
			}
			if e := os.Chtimes(marker, stamp, stamp); e != nil {
				t.Fatal(e)
			}
		}
		if name == "installed" {
			raw, _ := json.Marshal(Lock{Version: 3, Name: name, EnvDir: dir, LockSHA256: strings.Repeat("a", 64)})
			writeFixture(t, filepath.Join(dir, "env.lock.json"), string(raw))
		}
		if name == "symlink-marker" {
			if e := os.Symlink(filepath.Join(dirs["expired"], "install-failure.log"), filepath.Join(dir, "install-failure.log")); e != nil {
				t.Fatal(e)
			}
		}
		stamp := old
		if name == "recent" {
			stamp = old.Add(time.Second)
		}
		if e := os.Chtimes(dir, stamp, stamp); e != nil {
			t.Fatal(e)
		}
	}
	mutateState(t, f, func(s map[string]map[string]any) {
		c := cleanupResource("container", "memeval-active-install", map[string]string{"memeval.env": "active@aaaaaaaaaaaa"}, now)
		c["State"] = map[string]any{"Running": true}
		s["containers"]["memeval-active-install"] = c
	})
	o := GCOptions{Cache: cache, EPRoot: t.TempDir(), Now: now, OlderThan: 24 * time.Hour}
	plan, e := GarbageCollect(t.Context(), b, o)
	if e != nil {
		t.Fatal(e)
	}
	deletes := 0
	for _, item := range plan.Items {
		if item.Kind == "env-directory" && item.Action == "DELETE" {
			if item.Name != dirs["expired"] || item.Reason != "failed-install" {
				t.Fatal(item)
			}
			deletes++
		}
	}
	if deletes != 1 {
		t.Fatal(plan)
	}
	// dry-run retains every diagnostic, including the eligible directory.
	for _, dir := range dirs {
		if _, e := os.Stat(dir); e != nil {
			t.Fatal(e)
		}
	}
	o.Apply = true
	r, e := GarbageCollect(t.Context(), b, o)
	if e != nil || len(r.Remaining) != 0 {
		t.Fatal(e, r)
	}
	for name, dir := range dirs {
		_, e := os.Stat(dir)
		if name == "expired" {
			if !os.IsNotExist(e) {
				t.Fatal("expired failure not removed", e)
			}
		} else if e != nil {
			t.Fatal("protected diagnostics deleted", name, e)
		}
	}
	t.Log("only unlocked failed-install at age >= 24h removed; recent directory/log, lock, active container, absent/symlink marker retained")
}

func TestGCFailedInstallRechecksLockBeforeRemoval(t *testing.T) {
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	writeFixture(t, filepath.Join(dir, "install-failure.log"), "failed")
	stamp, e := os.Stat(dir)
	if e != nil {
		t.Fatal(e)
	}
	item := GCItem{Kind: "env-directory", Name: dir, root: filepath.Dir(dir), stamp: stamp}
	writeFixture(t, filepath.Join(dir, "env.lock.json"), `{"version":3}`)
	// Even unchanged directory timestamps cannot make a newly locked env eligible.
	if e = os.Chtimes(dir, stamp.ModTime(), stamp.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = removeGCHost(item); e == nil || !strings.Contains(e.Error(), "failed install state changed") {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(dir, "env.lock.json")); e != nil {
		t.Fatal(e)
	}
}
