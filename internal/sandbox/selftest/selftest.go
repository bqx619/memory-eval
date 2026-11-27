package selftest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/report"
	"memeval/internal/sandbox"
	"memeval/internal/sandbox/container"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"time"
)

// Expectations describes checks owned by a backend, not by the host evaluator.
type Expectations map[string]bool

// GatewayExpectations extends the internal-network expectations.
func GatewayExpectations() Expectations {
	return Expectations{"tcp4_control": false, "udp_control": false, "dns_external": false, "https_external": false, "gateway_connect": true, "relay_single_target": true}
}

// Selftester is optional. Absence never constitutes proof of isolation.
type Selftester interface {
	Selftest(context.Context, sandbox.Spec, container.ProbeOptions) (*container.SelftestResult, error)
}

type Check struct {
	Expected      bool         `json:"expected"`
	ControlPassed bool         `json:"control_passed"`
	Observed      *Observation `json:"observed"`
	Passed        bool         `json:"passed"`
}
type Record struct {
	Version         int                 `json:"version"`
	SUT             string              `json:"sut"`
	Network         sandbox.NetworkMode `json:"network"`
	CreatedAt       time.Time           `json:"created_at"`
	Signature       string              `json:"configuration_sha256"`
	Caps            sandbox.Caps        `json:"caps"`
	ProfileSHA256   string              `json:"profile_sha256"`
	Passed          bool                `json:"passed"`
	Status          string              `json:"status"`
	Reason          string              `json:"reason,omitempty"`
	Control         *ProbeResult        `json:"control"`
	Sandboxed       *ProbeResult        `json:"sandboxed"`
	Checks          map[string]Check    `json:"checks"`
	SHA256          string              `json:"sha256"`
	EffectiveConfig map[string]any      `json:"effective_config,omitempty"`
}
type Evidence struct {
	Path                string           `json:"path"`
	SHA256              string           `json:"sha256"`
	CreatedAt           time.Time        `json:"created_at"`
	ConfigurationSHA256 string           `json:"configuration_sha256"`
	Checks              map[string]Check `json:"checks,omitempty"`
}

func Signature(m *sut.Manifest, l *sut.Lock, caps sandbox.Caps) (string, error) {
	s, _, e := signature(m, l, caps)
	return s, e
}
func signature(m *sut.Manifest, l *sut.Lock, caps sandbox.Caps) (string, string, error) {
	exe, e := os.Executable()
	if e != nil {
		return "", "", e
	}
	binary, e := sut.FileHash(exe)
	if e != nil {
		return "", "", e
	}
	spec, e := m.Spec(l, "/__memeval_episode__", "{ns}", false)
	if e != nil {
		return "", "", e
	}
	interpreter := l.Image.ID
	if l.ImageRoot == "" {
		interpreter, e = sut.FileHash(l.Executable)
		if e != nil {
			return "", "", e
		}
	}
	linuxSHA := ""
	config := container.DefaultConfig()
	if m.Sandbox.Backend == "container" {
		_, linuxSHA, e = container.ProbeBinary(config)
		if e != nil {
			return "", "", e
		}
	}
	return sandbox.Hash([]any{m, l, spec, caps, binary, linuxSHA, interpreter, container.TemplateIdentity(config), dotenv.Value("MEMEVAL_EP_ROOT")}), linuxSHA, nil
}
func (r *Record) hash() string {
	copy := *r
	copy.SHA256 = ""
	// EffectiveConfig contains typed structs before persistence and maps after
	// decoding. Normalize object ordering without rounding integer observations.
	raw, _ := json.Marshal(copy)
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return ""
	}
	return sandbox.Hash(value)
}
func Recent(dir, signature string, now time.Time) (*Evidence, error) {
	matches, e := filepath.Glob(filepath.Join(dir, "selftest-*.json"))
	if e != nil {
		return nil, e
	}
	var best *Evidence
	for _, p := range matches {
		b, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil || r.Version != 3 || !r.Passed || r.Status != "passed" || r.Signature != signature || r.SHA256 != r.hash() {
			continue
		}
		age := now.Sub(r.CreatedAt)
		if age < 0 || age > 24*time.Hour {
			continue
		}
		if best == nil || r.CreatedAt.After(best.CreatedAt) {
			best = r.Evidence(p)
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no matching passing selftest within 24h; run sandbox selftest")
	}
	return best, nil
}
func (r *Record) Evidence(path string) *Evidence {
	return &Evidence{Path: path, SHA256: r.SHA256, CreatedAt: r.CreatedAt, ConfigurationSHA256: r.Signature, Checks: r.Checks}
}

func Run(ctx context.Context, b sandbox.Sandbox, m *sut.Manifest, l *sut.Lock, dir string) (*Record, string, error) {
	caps, err := b.Capabilities(ctx)
	if err != nil {
		return nil, "", err
	}
	if caps.Reason != "" {
		return nil, "", &sandbox.Unenforceable{Reason: caps.Reason}
	}
	signature, linuxSHA, err := signature(m, l, caps)
	if err != nil {
		return nil, "", err
	}
	r := &Record{Version: 3, SUT: m.Name, Network: m.Sandbox.Network, CreatedAt: time.Now().UTC(), Signature: signature, Caps: caps, Status: "not_applicable", Reason: "not applicable: backend has no Selftester", Checks: map[string]Check{}}
	if tester, ok := b.(Selftester); ok {
		observed, e := runContainer(ctx, tester, m, l, linuxSHA)
		err = e
		if observed == nil {
			return nil, "", fmt.Errorf("selftest returned no record: %v", e)
		}
		r = observed
		r.Version, r.SUT, r.Network, r.Signature, r.Caps = 3, m.Name, m.Sandbox.Network, signature, caps
		r.CreatedAt = time.Now().UTC()
		if err != nil {
			r.Passed = false
			r.Status = "failed"
			r.Reason = err.Error()
		}
		if !r.Passed && err == nil {
			err = fmt.Errorf("selftest %s: %s", r.Status, r.Reason)
		}
	}
	r.SHA256 = r.hash()
	path := filepath.Join(dir, "selftest-"+m.Name+"-"+string(m.Sandbox.Network)+"-"+r.CreatedAt.Format("20060102T150405.000000000")+".json")
	if e := report.WriteJSON(context.WithoutCancel(ctx), path, r); e != nil {
		return r, path, e
	}
	return r, path, err
}
