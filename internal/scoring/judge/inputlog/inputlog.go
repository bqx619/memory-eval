// Package inputlog stores evaluator-only, versioned recipes for judge re-evaluation.
// It deliberately has no endpoint, header, or credential fields.
package inputlog

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"memeval/internal/config/redact"
	"memeval/internal/llm/decision"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const Version = "judge-inputs-v2"
const Filename = "judge_inputs.jsonl"

type Recipe struct {
	Kind       string              `json:"kind"` // binary, score, choices
	State      string              `json:"state"`
	Questions  []decision.Question `json:"questions"`
	TemplateID string              `json:"template_id"`
}
type Verdict struct {
	Deviations          []string                      `json:"deviations,omitempty"`
	Kind                string                        `json:"kind"`
	Provider            string                        `json:"provider"`
	Model               string                        `json:"model"`
	Valid               bool                          `json:"valid"`
	Label               *bool                         `json:"label,omitempty"`
	Score               *float64                      `json:"score,omitempty"`
	Labels              map[string]string             `json:"labels,omitempty"`
	P                   *float64                      `json:"p,omitempty"`
	Expectation         *float64                      `json:"expectation,omitempty"`
	Probabilities       map[string]float64            `json:"probabilities,omitempty"`
	Confidence          *float64                      `json:"confidence,omitempty"`
	ChoiceProbabilities map[string]map[string]float64 `json:"choice_probabilities,omitempty"`
	ChoiceConfidence    map[string]float64            `json:"choice_confidence,omitempty"`
	Raw                 json.RawMessage               `json:"raw,omitempty"`
	ModelReturned       string                        `json:"model_returned,omitempty"`
	RequestSHA          string                        `json:"request_sha,omitempty"`
	Error               string                        `json:"error,omitempty"`
}
type Record struct {
	SchemaVersion string         `json:"schema_version"`
	ID            string         `json:"id"`
	CaseID        string         `json:"case_id"`
	EpisodeID     string         `json:"episode_id,omitempty"`
	ClusterID     string         `json:"cluster_id,omitempty"`
	RepeatIndex   int            `json:"repeat_index"`
	SampleID      string         `json:"sample_id"`
	Family        string         `json:"family"`
	Category      string         `json:"category"`
	Fields        map[string]any `json:"fields"`
	Recipe        Recipe         `json:"recipe"`
	Decision      Verdict        `json:"decision"`
}
type Artifact struct {
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Rows          int    `json:"rows"`
	SchemaVersion string `json:"schema_version"`
}
type caseInfo struct {
	ID, Episode, Cluster string
	Repeat               int
}
type recorderKey struct{}
type caseKey struct{}
type fieldsKey struct{}
type Recorder struct {
	mu        sync.Mutex
	file      *os.File
	digest    hash.Hash
	rows      int
	templates map[string]Template
	seen      map[string]bool
	err       error
	redactor  redact.Redactor
}

func New(ctx context.Context, dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// Match run's existing --out reuse semantics without following a preexisting
	// artifact symlink or inheriting permissive modes. Subsequent rows stream
	// directly to this newly published file, including on interrupted runs.
	f, err := os.CreateTemp(dir, ".judge-inputs-*")
	if err != nil {
		return nil, err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, Filename)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &Recorder{file: f, digest: sha256.New(), seen: map[string]bool{}, templates: map[string]Template{}, redactor: redact.From(ctx)}, nil
}
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}
func From(ctx context.Context) *Recorder { r, _ := ctx.Value(recorderKey{}).(*Recorder); return r }
func WithCase(ctx context.Context, id, episode, cluster string, repeat int) context.Context {
	return context.WithValue(ctx, caseKey{}, caseInfo{id, episode, cluster, repeat})
}

// Fields are copied per invocation, so subquestions do not contaminate each other.
func WithFields(ctx context.Context, fields map[string]any) context.Context {
	m := map[string]any{}
	if parent, ok := ctx.Value(fieldsKey{}).(map[string]any); ok {
		for k, v := range parent {
			m[k] = v
		}
	}
	for k, v := range fields {
		m[k] = v
	}
	return context.WithValue(ctx, fieldsKey{}, m)
}

func Append(ctx context.Context, sample, category string, recipe Recipe, v Verdict) error {
	r := From(ctx)
	if r == nil {
		return nil
	}
	if v.Kind != "decision" || v.Provider != "typesafe" {
		return fmt.Errorf("judge inputs require TypeSafe decision verdicts")
	}
	c, _ := ctx.Value(caseKey{}).(caseInfo)
	if c.ID == "" {
		c.ID = sample
	}
	fields := map[string]any{}
	// Structured state is always available, including future judge families.
	if err := json.Unmarshal([]byte(recipe.State), &fields); err != nil || fields == nil {
		fields = map[string]any{"state": recipe.State}
	}
	if m, ok := ctx.Value(fieldsKey{}).(map[string]any); ok {
		for k, v := range m {
			fields[k] = v
		}
	}
	family := category
	for _, prefix := range []string{"beam", "memora", "prefeval", "convomem", "halumem", "mab"} {
		if len(category) > len(prefix) && category[:len(prefix)+1] == prefix+"_" {
			family = prefix
			break
		}
	}
	if len(recipe.TemplateID) >= 12 && recipe.TemplateID[:12] == "longmemeval/" {
		family = "longmemeval"
	}
	idBytes, _ := json.Marshal([]any{c.ID, c.Repeat, sample, category, recipe.TemplateID})
	id := fmt.Sprintf("%x", sha256.Sum256(idBytes))
	row := Record{Version, id, c.ID, c.Episode, c.Cluster, c.Repeat, sample, family, category, fields, recipe, v}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	if r.seen[id] {
		r.err = fmt.Errorf("duplicate judge input %s", id)
		return r.err
	}
	b, e := r.redactor.Marshal(row)
	if e == nil {
		b = append(b, '\n')
		_, e = io.MultiWriter(r.file, r.digest).Write(b)
	}
	if e != nil {
		r.err = e
		return e
	}
	idName, sha, _ := strings.Cut(recipe.TemplateID, "@")
	r.templates[idName] = Template{ID: idName, SHA256: sha}
	r.rows++
	r.seen[id] = true
	return nil
}
func (r *Recorder) Snapshot() (*Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	if e := r.file.Sync(); e != nil {
		return nil, e
	}
	return &Artifact{Filename, hex.EncodeToString(r.digest.Sum(nil)), r.rows, Version}, nil
}
func (r *Recorder) Close() error { r.mu.Lock(); defer r.mu.Unlock(); return r.file.Close() }

// Read checks the report's trusted digest before exposing any inputs to a client.
func Read(dir string, meta Artifact) ([]Record, error) {
	if meta.Path != Filename || meta.SchemaVersion != Version || len(meta.SHA256) != 64 {
		return nil, fmt.Errorf("invalid judge input artifact metadata")
	}
	f, e := os.Open(filepath.Join(dir, Filename))
	if e != nil {
		return nil, e
	}
	defer f.Close()
	h := sha256.New()
	s := bufio.NewScanner(io.TeeReader(f, h))
	s.Buffer(make([]byte, 4096), 64<<20)
	rows := []Record{}
	seen := map[string]bool{}
	for s.Scan() {
		var r Record
		if e = json.Unmarshal(s.Bytes(), &r); e != nil {
			return nil, e
		}
		if r.Decision.Kind != "decision" || r.Decision.Provider != "typesafe" || r.SchemaVersion != Version || r.ID == "" || seen[r.ID] || r.CaseID == "" || r.SampleID == "" || r.Category == "" || r.RepeatIndex < 0 {
			return nil, fmt.Errorf("invalid or duplicate judge input row")
		}
		if e = r.Recipe.Validate(); e != nil {
			return nil, e
		}
		seen[r.ID] = true
		rows = append(rows, r)
	}
	if e = s.Err(); e != nil {
		return nil, e
	}
	if hex.EncodeToString(h.Sum(nil)) != meta.SHA256 || len(rows) != meta.Rows {
		return nil, fmt.Errorf("judge input hash/count mismatch")
	}
	return rows, nil
}
func (r Recipe) Validate() error {
	if r.TemplateID == "" || len(r.Questions) == 0 {
		return fmt.Errorf("missing judge recipe")
	}
	switch r.Kind {
	case "binary":
		if len(r.Questions) != 1 || r.Questions[0].Type != "noul" || r.Questions[0].Name != "correct" {
			return fmt.Errorf("invalid binary recipe")
		}
	case "score":
		if len(r.Questions) != 1 || r.Questions[0].Type != "score" || r.Questions[0].Levels < 2 {
			return fmt.Errorf("invalid score recipe")
		}
	case "choices":
		for _, q := range r.Questions {
			if q.Type != "choice" || len(q.Options) < 2 {
				return fmt.Errorf("invalid choice recipe")
			}
		}
	default:
		return fmt.Errorf("unknown judge recipe kind %q", r.Kind)
	}
	return nil
}

// Template identifies the immutable decision instructions used by a run.
type Template struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

func (r *Recorder) Templates() map[string]Template {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]Template{}
	for k, v := range r.templates {
		m[k] = v
	}
	return m
}
