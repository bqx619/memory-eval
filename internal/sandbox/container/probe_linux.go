//go:build linux

package container

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
)

func Probe(args []string, out io.Writer) error {
	if len(args) != 1 || args[0] != "caps" {
		return fullProbe(args, out)
	}
	r := probeResult{EnvKeys: []string{}, UID: os.Getuid(), GID: os.Getgid(), PID: os.Getpid()}
	r.Hostname, _ = os.Hostname()
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, _ := strings.Cut(line, ":")
		switch k {
		case "CapEff":
			r.CapEff = strings.TrimSpace(v)
		case "NoNewPrivs":
			r.NoNewPrivs = strings.TrimSpace(v) == "1"
		}
	}
	path := "/.memeval-probe-" + nonce()
	err = os.WriteFile(path, []byte("probe"), 0600)
	r.RootReadonly = errors.Is(err, syscall.EROFS)
	if err == nil {
		os.Remove(path)
	}
	err = os.WriteFile("/probe/memeval-probe-owner", []byte("probe"), 0600)
	r.ProbeWritable = err == nil
	f, err := os.CreateTemp("/tmp", "memeval-probe-")
	r.TmpWritable = err == nil
	if err == nil {
		f.Close()
		os.Remove(f.Name())
	}
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		r.EnvKeys = append(r.EnvKeys, k)
	}
	sort.Strings(r.EnvKeys)
	return json.NewEncoder(out).Encode(r)
}
