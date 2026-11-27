// Package registry binds immutable suite IDs to separately versioned protocols.
package registry

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/benchmark"
	"memeval/internal/importers"
	"memeval/internal/jsonstrict"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

type License = suite.License
type Entry struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	Repository           string         `json:"repository"`
	DatasetURL           string         `json:"dataset_url"`
	DataNature           string         `json:"data_nature" jsonschema:"enum=,enum=real-user-logs,enum=real-derived,enum=human-authored,enum=model-generated,enum=structured-diagnostic,enum=agent-trajectory"`
	TaskKinds            []string       `json:"task_kinds"`
	ProtocolID           string         `json:"protocol_id"`
	Variants             []string       `json:"variants"`
	ReportedScale        map[string]any `json:"reported_scale"`
	License              License        `json:"license"`
	PinnedRevision       string         `json:"pinned_revision"`
	ImplementationStatus string         `json:"implementation_status" jsonschema:"enum=registered,enum=importer,enum=imported,enum=run"`
	Importer             string         `json:"importer"`
	LockPath             string         `json:"lock_path,omitempty"`
	Suites               []SuiteRef     `json:"suites"`
	Overlaps             []string       `json:"overlaps"`
	Notes                []string       `json:"notes"`
	VerificationDate     string         `json:"verification_date"`
}
type SuiteRef struct {
	SuiteID         string `json:"suite_id"`
	Path            string `json:"path"`
	ProtocolRef     string `json:"protocol_ref"`
	ImportStatsPath string `json:"import_stats_path,omitempty"`
	SuiteSHA256     string `json:"suite_sha256,omitempty"`
}
type Registry []Entry

func LoadDir(dir string) (Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	r := Registry{}
	ids, suites := map[string]bool{}, map[string]bool{}
	for _, file := range entries {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, file.Name()))
		if err != nil {
			return nil, err
		}
		var e Entry
		if err = jsonstrict.Decode(b, &e); err != nil {
			return nil, fmt.Errorf("registry %s: %w", file.Name(), err)
		}
		if e.ID == "" || e.ID+".json" != file.Name() || ids[e.ID] {
			return nil, fmt.Errorf("registry duplicate/invalid id: %s", file.Name())
		}
		ids[e.ID] = true
		if !slices.Contains([]string{"registered", "importer", "imported", "run"}, e.ImplementationStatus) {
			return nil, fmt.Errorf("registry %s: invalid implementation_status", e.ID)
		}
		for _, s := range e.Suites {
			if suites[s.SuiteID] {
				return nil, fmt.Errorf("registry duplicate suite_id %s", s.SuiteID)
			}
			suites[s.SuiteID] = true
		}
		r = append(r, e)
	}
	return r, nil
}
func (r Registry) LookupSuite(id string) (*Entry, *SuiteRef, bool) {
	for i := range r {
		for j := range r[i].Suites {
			if r[i].Suites[j].SuiteID == id {
				return &r[i], &r[i].Suites[j], true
			}
		}
	}
	return nil, nil, false
}

type CheckResult struct {
	OK       bool     `json:"ok"`
	Entries  int      `json:"entries"`
	Problems []string `json:"problems"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r Registry) Check(ctx context.Context, root string, verifyFiles bool) CheckResult {
	out := CheckResult{Entries: len(r), Problems: []string{}, Warnings: []string{}}
	problem := func(id string, err any) { out.Problems = append(out.Problems, fmt.Sprintf("%s: %v", id, err)) }
	entries := map[string]Entry{}
	for _, e := range r {
		entries[e.ID] = e
	}
	for _, e := range r {
		if err := ctx.Err(); err != nil {
			problem(e.ID, err)
			break
		}
		protocol, err := benchmark.Load(filepath.Join(root, "protocols", e.ProtocolID+".json"))
		if err != nil {
			if e.ImplementationStatus == "registered" && os.IsNotExist(err) {
				out.Warnings = append(out.Warnings, e.ID+": protocol not implemented yet")
			} else {
				problem(e.ID, err)
			}
		} else {
			if protocol.ProtocolID != e.ProtocolID {
				problem(e.ID, "protocol_id differs")
			}
			for _, v := range e.Variants {
				if !declaredVariant(protocol, v) {
					problem(e.ID, "unknown protocol variant "+v)
				}
			}
		}
		imported := e.ImplementationStatus == "imported" || e.ImplementationStatus == "run"
		if imported {
			if e.LockPath == "" && hasNote(e.Notes, "lock:") {
				out.Warnings = append(out.Warnings, e.ID+": no fetch lock; see declared acquisition limitation")
			} else if err := checkLock(ctx, root, e.LockPath, verifyFiles); err != nil {
				problem(e.ID, err)
			}
		}
		for _, ref := range e.Suites {
			path, err := localPath(root, ref.Path)
			if err != nil {
				problem(e.ID, err)
				continue
			}
			s, err := suite.Load(ctx, path)
			if err != nil {
				problem(e.ID, err)
				continue
			}
			if s.SuiteID != ref.SuiteID {
				problem(e.ID, "suite_id differs: "+ref.Path)
			}
			p, v, _, err := benchmark.ResolveAt(root, ref.ProtocolRef)
			if err != nil {
				problem(e.ID, err)
			} else if p.ProtocolID != e.ProtocolID || !registeredProtocolVariant(p, v, e.Variants) {
				problem(e.ID, "suite protocol not declared in entry")
			}
			if s.ProtocolRef != "" && s.ProtocolRef != ref.ProtocolRef {
				problem(e.ID, "suite protocol_ref differs")
			}
			if s.Container() == "bundle" && ref.SuiteSHA256 == "" {
				problem(e.ID, "bundle requires trusted suites[].suite_sha256")
				continue
			}
			counts, err := suite.Validate(ctx, s, suite.ValidateOptions{SkipLeakScan: s.Container() == "single", ExpectedSHA256: ref.SuiteSHA256})
			if err != nil {
				problem(e.ID, err)
				continue
			}
			if imported && ref.ImportStatsPath != "" {
				p, err := localPath(root, ref.ImportStatsPath)
				if err == nil {
					var b []byte
					b, err = os.ReadFile(p)
					if err == nil {
						var stats importers.Stats
						err = json.Unmarshal(b, &stats)
						if err == nil && !reflect.DeepEqual(stats.Counts, counts) {
							err = fmt.Errorf("import stats counts differ")
						}
					}
				}
				if err != nil {
					problem(e.ID, err)
				}
			}
			observed := map[string]int{"questions": counts.Cases, "cases": counts.Cases, "episodes": counts.Episodes, "events": counts.Events, "seeds": counts.Events}
			scaleMismatch := false
			for key, n := range observed {
				if declared, ok := e.ReportedScale[key]; ok {
					b, _ := json.Marshal(declared)
					var value float64
					if json.Unmarshal(b, &value) == nil && value != float64(n) {
						scaleMismatch = true
					}
				}
			}
			if scaleMismatch && !hasNote(e.Notes, "scale:") {
				problem(e.ID, "reported_scale differs from import counts without scale: explanation")
			}
		}
		for _, other := range e.Overlaps {
			entry, ok := entries[other]
			if !ok || !slices.Contains(entry.Overlaps, e.ID) {
				problem(e.ID, "overlaps not symmetric: "+other)
			}
		}
	}
	out.OK = len(out.Problems) == 0
	return out
}
func hasNote(notes []string, prefix string) bool {
	for _, n := range notes {
		if strings.HasPrefix(n, prefix) && strings.TrimSpace(strings.TrimPrefix(n, prefix)) != "" {
			return true
		}
	}
	return false
}
func localPath(root, p string) (string, error) {
	if !filepath.IsLocal(p) {
		return "", fmt.Errorf("expected repository-relative path")
	}
	return filepath.Join(root, p), nil
}

type lockFile struct {
	Path   string `json:"path"`
	Bytes  *int64 `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func checkLock(ctx context.Context, root, path string, verify bool) error {
	p, err := localPath(root, path)
	if err != nil {
		return fmt.Errorf("source lock required: %w", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var lock struct {
		DatasetID string     `json:"dataset_id"`
		SourceURL string     `json:"source_url"`
		Revision  string     `json:"revision"`
		Files     []lockFile `json:"files"`
		Manifest  string     `json:"files_manifest"`
		Total     int        `json:"files_total"`
	}
	if err = json.Unmarshal(b, &lock); err != nil {
		return err
	}
	if lock.Manifest != "" {
		if len(lock.Files) > 0 || !filepath.IsLocal(lock.Manifest) || lock.Total < 1 {
			return fmt.Errorf("invalid external lock manifest")
		}
		b, err := os.ReadFile(filepath.Join(filepath.Dir(p), lock.Manifest))
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(bytes.NewReader(b))
		for scanner.Scan() {
			var file lockFile
			if err = json.Unmarshal(scanner.Bytes(), &file); err != nil {
				return err
			}
			lock.Files = append(lock.Files, file)
		}
		if err = scanner.Err(); err != nil {
			return err
		}
		if len(lock.Files) != lock.Total {
			return fmt.Errorf("lock manifest count mismatch")
		}
	}
	if lock.DatasetID == "" || lock.SourceURL == "" || lock.Revision == "" || len(lock.Files) == 0 {
		return fmt.Errorf("invalid generic source lock")
	}
	seen := map[string]bool{}
	for _, file := range lock.Files {
		if seen[file.Path] {
			return fmt.Errorf("duplicate source lock file")
		}
		seen[file.Path] = true
		if err = ctx.Err(); err != nil {
			return err
		}
		digest, decodeErr := hex.DecodeString(file.SHA256)
		if !filepath.IsLocal(file.Path) || file.Bytes == nil || *file.Bytes < 0 || decodeErr != nil || len(digest) != 32 {
			return fmt.Errorf("invalid source lock file")
		}
		if !verify {
			continue
		}
		f, err := os.Open(filepath.Join(filepath.Dir(p), file.Path))
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		if n != *file.Bytes || hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
			return fmt.Errorf("source lock size/sha256 mismatch: %s", file.Path)
		}
	}
	return nil
}
func Schema() ([]byte, error) { return suite.SchemaJSON(Entry{}) }

// Dataset size variants and evaluation protocol variants are separate axes.
// A protocol may declare supported dataset sizes in variant.params.dataset_variants.
func declaredVariant(p *benchmark.Protocol, name string) bool {
	if _, ok := p.Variants[name]; ok {
		return true
	}
	for _, v := range p.Variants {
		if values, ok := v.Params["dataset_variants"].([]any); ok {
			for _, value := range values {
				if s, ok := value.(string); ok && s == name {
					return true
				}
			}
		}
	}
	return false
}

func registeredProtocolVariant(p *benchmark.Protocol, name string, registered []string) bool {
	if slices.Contains(registered, name) {
		return true
	}
	variant, ok := p.Variants[name]
	if !ok {
		return false
	}
	if sizes, ok := variant.Params["dataset_variants"].([]any); ok {
		for _, size := range sizes {
			if value, ok := size.(string); ok && slices.Contains(registered, value) {
				return true
			}
		}
	}
	return false
}
