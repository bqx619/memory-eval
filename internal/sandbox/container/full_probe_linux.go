//go:build linux

package container

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

func fullProbe(args []string, out io.Writer) error {
	f := flag.NewFlagSet("__container-probe", flag.ContinueOnError)
	f.SetOutput(out)
	dest := f.String("out", "", "episode result JSON")
	options := f.String("options", "", "public probe options JSON")
	control := f.String("control", "", "host control address")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *dest == "" || *options == "" {
		return fmt.Errorf("container probe requires --out and --options")
	}
	raw, err := os.ReadFile(*options)
	if err != nil {
		return err
	}
	var o ProbeOptions
	if err = json.Unmarshal(raw, &o); err != nil {
		return err
	}
	if *control != o.Control {
		return fmt.Errorf("control endpoint differs from probe options")
	}
	if !filepath.IsAbs(o.EpisodeDir) || filepath.Dir(*dest) != o.EpisodeDir {
		return fmt.Errorf("probe output must be in episode")
	}
	r := &ProbeResult{Items: map[string]Observation{}, Facts: map[string]any{}}
	add := func(k string, fn func() error) { r.Items[k] = observe(fn) }
	add("gold_absent", func() error {
		return filepath.WalkDir(o.EpisodeDir, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			name := strings.ToLower(d.Name())
			if strings.HasPrefix(name, "gold") || name == "suite.json" || strings.HasPrefix(name, "suite.") {
				return fmt.Errorf("private suite material present: %s", p)
			}
			return nil
		})
	})
	add("host_paths_absent", func() error {
		for _, p := range o.HostPaths {
			if _, e := os.Stat(p); !os.IsNotExist(e) {
				return fmt.Errorf("host path not provably absent: %s", p)
			}
		}
		return nil
	})
	write := func(dir string, readonly bool) error {
		p := filepath.Join(dir, ".memeval-write-"+nonce())
		e := os.WriteFile(p, []byte("probe"), 0600)
		if e == nil {
			os.Remove(p)
		}
		if readonly {
			if errors.Is(e, syscall.EROFS) {
				return nil
			}
			return fmt.Errorf("expected EROFS, got %v", e)
		}
		return e
	}
	add("write_episode", func() error { return write(o.EpisodeDir, false) })
	add("write_rootfs_fails", func() error { return write("/", true) })
	add("write_tmp_ok", func() error { return write("/tmp", false) })
	add("env_read", func() error {
		if _, e := os.ReadDir(o.EnvRoot); e != nil {
			return e
		}
		return write(o.EnvRoot, true)
	})
	status, e := os.ReadFile("/proc/self/status")
	if e != nil {
		return e
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(status), "\n") {
		k, v, _ := strings.Cut(line, ":")
		values[k] = strings.TrimSpace(v)
	}
	uid, gid := os.Getuid(), os.Getgid()
	r.Facts["uid"], r.Facts["gid"] = uid, gid
	r.Facts["cap_eff_hex"], r.Facts["no_new_privs"] = values["CapEff"], values["NoNewPrivs"]
	r.Items["uid_nonroot"] = Observation{Success: uid == 65532 && gid == 65532, Detail: fmt.Sprintf("uid=%d gid=%d", uid, gid)}
	cap, capErr := strconv.ParseUint(values["CapEff"], 16, 64)
	r.Items["caps_dropped"] = Observation{Success: capErr == nil && cap == 0}
	r.Items["no_new_privs"] = Observation{Success: values["NoNewPrivs"] == "1"}
	add("pids_limit", func() error {
		for _, p := range []string{"/sys/fs/cgroup/pids.max", "/sys/fs/cgroup/pids/pids.max"} {
			if raw, e := os.ReadFile(p); e == nil {
				value := strings.TrimSpace(string(raw))
				r.Facts["pids_max"] = value
				if value == strconv.Itoa(o.PidsLimit) {
					return nil
				}
				return fmt.Errorf("pids.max=%s, expected %d", value, o.PidsLimit)
			}
		}
		return fmt.Errorf("pids.max unavailable")
	})
	keys := []string{}
	add("environment", func() error {
		var unexpected []string
		for _, entry := range os.Environ() {
			k, _, _ := strings.Cut(entry, "=")
			keys = append(keys, k)
			if strings.HasPrefix(k, "MEMEVAL_") || !slices.Contains(o.AllowedEnv, k) {
				unexpected = append(unexpected, k)
			}
		}
		if len(unexpected) > 0 {
			return fmt.Errorf("unapproved environment keys: %v", unexpected)
		}
		if os.Getenv("HOME") != filepath.Join(o.EpisodeDir, "home") || os.Getenv("TMPDIR") != "/tmp" {
			return fmt.Errorf("HOME/TMPDIR mismatch")
		}
		return nil
	})
	slices.Sort(keys)
	r.Facts["env_keys"] = keys
	mounts, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return e
	}
	rootRO, points, unexpected := probeMountInfo(mounts, o, func(p string) bool {
		info, err := os.Lstat(p)
		return err == nil && info.Mode().IsRegular()
	})
	r.Facts["mounts"] = points
	r.Items["rootfs_readonly"] = Observation{Success: rootRO && r.Items["write_rootfs_fails"].Success}
	r.Items["mounts_expected"] = Observation{Success: len(unexpected) == 0, Detail: fmt.Sprintf("unexpected mounts: %v", unexpected)}
	for k, v := range networkObservations(o) {
		r.Items[k] = v
	}
	raw, err = json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*dest, append(raw, '\n'), 0600)
}
