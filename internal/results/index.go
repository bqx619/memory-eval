package results

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"memeval/internal/benchmark"
	"memeval/internal/result"
	"memeval/internal/scoring"
	"memeval/internal/sut"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

type IndexOptions struct {
	RepoRoot string
	Stderr   io.Writer
	// An explicit clock makes two builds reproducible; only the index has a time.
	Now    func() time.Time
	Commit string
}
type Index struct {
	Version     int                 `json:"index_version"`
	GeneratedAt string              `json:"generated_at"`
	Commit      any                 `json:"memeval_commit"`
	Glossary    []scoring.MetricDef `json:"glossary"`
	Cards       []Card              `json:"cards"`
}

func warn(o IndexOptions, format string, args ...any) {
	if o.Stderr != nil {
		fmt.Fprintf(o.Stderr, "warning: "+format+"\n", args...)
	}
}
func BuiltCommit() string {
	if v, ok := debug.ReadBuildInfo(); ok {
		for _, s := range v.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return ""
}

// LoadCard reads metadata only: neither a manifest env_file nor dataset cases
// are reopened. Missing catalog metadata stays null; invalid UPSTREAM fails.
func LoadCard(path string, o IndexOptions) (Card, error) {
	if info, e := os.Stat(path); e == nil && info.IsDir() {
		path = filepath.Join(path, "report.json")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var src map[string]any
	if e = decode(raw, &src); e != nil {
		return nil, e
	}
	if object(src["summary"]) == nil {
		return nil, fmt.Errorf("report rejected or missing summary: %s", path)
	}
	var r result.Report
	if e = json.Unmarshal(raw, &r); e != nil {
		return nil, e
	}
	opts := CardOptions{RepoRoot: o.RepoRoot, ReportPath: path, ReportJSON: raw}
	var upstream *Upstream
	// Use only the declared name/variant; never splice a variant name to guess
	// its package. This also supports two manifests sharing one SUT directory.
	names, _ := filepath.Glob(filepath.Join(o.RepoRoot, "suts", "*", "manifest*.json"))
	dirs := map[string]bool{}
	notes := map[string]bool{}
	for _, p := range names {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var m struct{ Name, Variant, Notes string }
		if err = json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("manifest metadata %s: %w", p, err)
		}
		if m.Name == r.SUT.Name {
			dirs[filepath.Dir(p)] = true
			if m.Variant == r.SUT.Variant {
				notes[m.Notes] = true
			}
		}
	}
	if len(notes) == 1 {
		for s := range notes {
			opts.VariantNotes = s
		}
	}
	if len(dirs) == 0 && filepath.IsLocal(r.SUT.Name) && filepath.Base(r.SUT.Name) == r.SUT.Name {
		dirs[filepath.Join(o.RepoRoot, "suts", r.SUT.Name)] = true
	}
	if len(dirs) == 1 {
		for dir := range dirs {
			upstream, e = sut.LoadUpstream(filepath.Join(dir, "UPSTREAM.json"))
			if e != nil && !os.IsNotExist(e) {
				return nil, e
			}
		}
	}
	if upstream == nil {
		warn(o, "%s: UPSTREAM.json unavailable or ambiguous for %s; upstream=null", path, r.SUT.Name)
	}
	if ref := r.Protocol.Benchmark; ref != nil {
		name, _, ok := strings.Cut(ref.Ref, "#")
		if ok && filepath.IsLocal(name) && filepath.Dir(name) == "protocols" {
			b, err := os.ReadFile(filepath.Join(o.RepoRoot, name))
			if err == nil && digest(b) == ref.SHA256 {
				p, err := benchmark.Load(filepath.Join(o.RepoRoot, name))
				if err != nil {
					return nil, err
				}
				opts.Primary = p.Scoring.Primary
			} else {
				warn(o, "%s: benchmark metadata missing or sha256 differs; fixed_reader headline=null", path)
			}
		}
	}
	return BuildCard(&r, upstream, opts)
}

func BuildIndex(o IndexOptions, dirs ...string) (*Index, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("at least one reports directory required")
	}
	paths := map[string]bool{}
	for _, dir := range dirs {
		// Follow an explicitly supplied directory alias, without following
		// arbitrary nested symlinks or cycling through an entire filesystem.
		dir, e := filepath.EvalSymlinks(dir)
		if e != nil {
			return nil, e
		}
		info, e := os.Stat(dir)
		if e != nil {
			return nil, e
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("reports path must be a directory: %s", dir)
		}
		e = filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			if d.Name() == "report.json" && d.Type().IsRegular() {
				abs, e := filepath.Abs(p)
				if e != nil {
					return e
				}
				paths[abs] = true
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	sorted := []string{}
	for p := range paths {
		sorted = append(sorted, p)
	}
	slices.Sort(sorted)
	byID := map[string]Card{}
	for _, p := range sorted {
		raw, e := os.ReadFile(p)
		if e != nil {
			return nil, e
		}
		var hdr map[string]any
		if e = decode(raw, &hdr); e != nil {
			return nil, fmt.Errorf("%s: %w", p, e)
		}
		if object(hdr["summary"]) == nil {
			warn(o, "skipping rejected report without summary: %s", p)
			continue
		}
		c, e := LoadCard(p, o)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", p, e)
		}
		id := str(c["id"])
		old := byID[id]
		// Lexical path order is the stable tie breaker for equal resume generations.
		if old == nil || resumeCount(c) > resumeCount(old) {
			byID[id] = c
		}
	}
	cards := []Card{}
	for _, c := range byID {
		cards = append(cards, c)
	}
	slices.SortFunc(cards, func(a, b Card) int {
		ta, _ := time.Parse(time.RFC3339Nano, str(at(a, "run", "started_at_utc")))
		tb, _ := time.Parse(time.RFC3339Nano, str(at(b, "run", "started_at_utc")))
		if c := tb.Compare(ta); c != 0 {
			return c
		}
		return strings.Compare(str(a["id"]), str(b["id"]))
	})
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	return &Index{1, now().UTC().Format(time.RFC3339Nano), nonempty(o.Commit), scoring.Glossary(), cards}, nil
}
func resumeCount(c Card) int64 {
	v, _ := at(c, "run", "resume_count").(json.Number)
	n, _ := v.Int64()
	return n
}

var scriptEnd = regexp.MustCompile(`(?i)</script`)

func IndexJS(b []byte) []byte {
	safe := scriptEnd.ReplaceAllFunc(b, func(v []byte) []byte { return append([]byte{'<', '\\'}, v[1:]...) })
	return append(append([]byte("window.MEMEVAL_RESULTS = "), bytesTrimNewline(safe)...), ';', '\n')
}
func bytesTrimNewline(b []byte) []byte { return []byte(strings.TrimSuffix(string(b), "\n")) }
func WriteFile(ctx context.Context, path string, b []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".memeval-results-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func WriteIndex(ctx context.Context, dir string, index *Index) error {
	b, e := JSON(index)
	if e != nil {
		return e
	}
	for _, f := range []struct {
		name string
		b    []byte
	}{{"index.json", b}, {"index.js", IndexJS(b)}} {
		if e = WriteFile(ctx, filepath.Join(dir, f.name), f.b); e != nil {
			return e
		}
	}
	return nil
}
