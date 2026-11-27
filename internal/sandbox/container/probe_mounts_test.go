package container

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestProbeMountInfoDockerInit(t *testing.T) {
	raw, err := os.ReadFile("testdata/probe.mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.NewReplacer("{ep}", "/episode", "{binary}", "/probe-binary").Replace(string(raw))
	for _, tt := range []struct {
		name, old, replacement string
		init, regular, want    bool
	}{
		{"readonly file bind with init", "", "", true, true, true},
		{"missing init flag", "", "", false, true, false},
		{"writable", dockerInitPath + " ro,", dockerInitPath + " rw,", true, true, false},
		{"directory", "", "", true, false, false},
		{"filesystem root", "/usr/libexec/docker-init " + dockerInitPath, "/ " + dockerInitPath, true, true, false},
		{"nearby path", dockerInitPath, dockerInitPath + "-other", true, true, false},
		{"other guest mount", dockerInitPath, "/opt/orbstack-guest", true, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mountinfo := base
			if tt.old != "" {
				mountinfo = strings.Replace(mountinfo, tt.old, tt.replacement, 1)
			}
			ro, points, unexpected := probeMountInfo([]byte(mountinfo), ProbeOptions{EpisodeDir: "/episode", Init: tt.init}, func(path string) bool { return tt.regular && path == dockerInitPath })
			if !ro || !slices.Contains(points, "/episode") || (len(unexpected) == 0) != tt.want {
				t.Fatalf("rootRO=%v unexpected=%v", ro, unexpected)
			}
		})
	}
}

func TestProbeInspectDockerInit(t *testing.T) {
	base := `{"Mounts":[{"Type":"bind","Source":"/episode","Destination":"/episode","RW":true},{"Type":"bind","Source":"/binary","Destination":"/memeval","RW":false},{"Type":"bind","Source":"/engine/docker-init","Destination":"/usr/sbin/docker-init","RW":false}]}`
	for _, tt := range []struct {
		name, old, replacement string
		init, want             bool
	}{
		{"readonly init bind", "", "", true, true},
		{"init flag absent", "", "", false, false},
		{"writable init", `"Destination":"/usr/sbin/docker-init","RW":false`, `"Destination":"/usr/sbin/docker-init","RW":true`, true, false},
		{"volume not bind", `"Type":"bind","Source":"/engine/docker-init"`, `"Type":"volume","Source":"/engine/docker-init"`, true, false},
		{"different bind", dockerInitPath, "/unexpected", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := base
			if tt.old != "" {
				raw = strings.Replace(raw, tt.old, tt.replacement, 1)
			}
			var v inspection
			if err := json.Unmarshal([]byte(raw), &v); err != nil {
				t.Fatal(err)
			}
			if got := expectedProbeBinds(v, "/episode", "/binary", tt.init); got != tt.want {
				t.Fatal(got, tt.want)
			}
		})
	}
	// Docker may omit engine-injected init from the user Mounts array.
	var v inspection
	json.Unmarshal([]byte(base), &v)
	v.Mounts = v.Mounts[:2]
	if !expectedProbeBinds(v, "/episode", "/binary", true) {
		t.Fatal("engine-only mount must be validated in guest mountinfo")
	}
}
