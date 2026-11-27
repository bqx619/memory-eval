package container

import (
	"slices"
	"strings"
)

const dockerInitPath = "/usr/sbin/docker-init"

// mountinfo does not retain the MS_BIND flag. A non-root filesystem path
// mounted on a regular file identifies a file bind; the per-mount flags must
// independently be ro. Do not allow directories, tmpfs roots, or nearby paths.
func probeMountInfo(raw []byte, o ProbeOptions, regularFile func(string) bool) (rootRO bool, points, unexpected []string) {
	allowed := []string{"/", "/tmp", "/memeval", o.EpisodeDir, "/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}
	for _, v := range o.ExtraTmpfs {
		p, _, _ := strings.Cut(v, ":")
		allowed = append(allowed, p)
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		p := unescape.Replace(fields[4])
		points = append(points, p)
		opts := strings.Split(fields[5], ",")
		ro := slices.Contains(opts, "ro") && !slices.Contains(opts, "rw")
		if p == "/" {
			rootRO = ro
		}
		ok := slices.Contains(allowed, p)
		for _, base := range []string{"/proc", "/sys", "/dev"} {
			ok = ok || p == base || strings.HasPrefix(p, base+"/")
		}
		if p == dockerInitPath {
			// This exact path has stricter requirements even if a writable_tmpfs
			// declaration were to list it. Only Docker's injected file is allowed.
			root := unescape.Replace(fields[3])
			ok = o.Init && ro && strings.HasPrefix(root, "/") && root != "/" && regularFile(p)
		}
		if !ok {
			unexpected = append(unexpected, p)
		}
	}
	return
}

func expectedProbeBinds(v inspection, ep, binary string, initEnabled bool) bool {
	seen := map[string]bool{}
	for _, m := range v.Mounts {
		if m.Destination == dockerInitPath {
			if !initEnabled || m.Type != "bind" || m.RW {
				return false
			}
			continue
		}
		if m.Type != "bind" {
			continue
		}
		if !(m.Source == ep && m.Destination == ep && m.RW || m.Source == binary && m.Destination == "/memeval" && !m.RW) {
			return false
		}
		seen[m.Destination] = true
	}
	// Engine-injected mounts may be absent from inspect.Mounts; mountinfo is
	// always checked by the guest. Explicit user binds must match exactly.
	return seen[ep] && seen["/memeval"]
}
