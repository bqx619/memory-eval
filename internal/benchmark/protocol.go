// Package benchmark loads declarative benchmark metadata. It does not replace
// the evaluator's scoring or fidelity algorithms.
package benchmark

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"memeval/internal/config/dotenv"
	"memeval/internal/jsonstrict"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Protocol struct {
	Notes        []string           `json:"notes,omitempty"`
	ProtocolID   string             `json:"protocol_id"`
	Version      string             `json:"version"`
	Upstream     Upstream           `json:"upstream"`
	Variants     map[string]Variant `json:"variants"`
	Scoring      Scoring            `json:"scoring"`
	Fidelity     FidelityRules      `json:"fidelity_rules"`
	ClusterBasis string             `json:"cluster_basis"`
	Overlaps     []string           `json:"overlaps,omitempty"`
}
type Upstream struct {
	Repo   string       `json:"repo"`
	Commit string       `json:"commit"`
	Files  []SourceFile `json:"files"`
}
type SourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Variant struct {
	Description string         `json:"description"`
	Suites      []string       `json:"suite_id_patterns,omitempty"`
	Params      map[string]any `json:"params"`
}
type Scoring struct {
	Primary       string       `json:"primary" jsonschema:"enum=judge,enum=exact_match,enum=token_f1,enum=mcq,enum=nugget,enum=assertions,enum=retrieval"`
	Judge         *TemplateRef `json:"judge,omitempty"`
	Reader        *TemplateRef `json:"reader,omitempty"`
	AnswerParsing string       `json:"answer_parsing"`
	Denominators  Denominators `json:"denominators"`
	Secondary     []string     `json:"secondary"`
}
type TemplateRef struct {
	File             string         `json:"file"`
	SHA256           string         `json:"sha256"`
	PerType          bool           `json:"per_type,omitempty"`
	ModelOfficial    string         `json:"model_official,omitempty"`
	Params           map[string]any `json:"params,omitempty"`
	LabelRule        string         `json:"label_rule,omitempty"`
	InputModeDefault string         `json:"input_mode_default,omitempty"`
}
type Denominators struct {
	Exclude       []string `json:"exclude"`
	PerTypeReport bool     `json:"per_type_report"`
}
type FidelityRules struct {
	Full       []string `json:"full"`
	Equivalent []string `json:"equivalent"`
	Else       string   `json:"else"`
}

func Root() string { cwd, _ := os.Getwd(); return dotenv.Root(cwd) }
func Load(path string) (*Protocol, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(b)
}
func decode(b []byte) (*Protocol, error) {
	var p Protocol
	if err := jsonstrict.Decode(b, &p); err != nil {
		return nil, err
	}
	if p.ProtocolID == "" || p.Version == "" || len(p.Variants) == 0 {
		return nil, fmt.Errorf("protocol_id, version and variants required")
	}
	if !slices.Contains([]string{"judge", "exact_match", "token_f1", "mcq", "nugget", "assertions", "retrieval"}, p.Scoring.Primary) {
		return nil, fmt.Errorf("unknown scoring.primary")
	}
	return &p, nil
}
func Resolve(ref string) (*Protocol, string, string, error) { return ResolveAt(Root(), ref) }
func ResolveAt(root, ref string) (*Protocol, string, string, error) {
	name, variant, ok := strings.Cut(ref, "#")
	if !ok || variant == "" || strings.Contains(variant, "#") || !filepath.IsLocal(name) || filepath.Dir(name) != "protocols" || filepath.Ext(name) != ".json" {
		return nil, "", "", fmt.Errorf("protocol ref must be protocols/<id>.json#<variant>")
	}
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return nil, "", "", err
	}
	p, err := decode(b)
	if err != nil {
		return nil, "", "", err
	}
	if filepath.Base(name) != p.ProtocolID+".json" {
		return nil, "", "", fmt.Errorf("protocol_id does not match filename")
	}
	if _, ok := p.Variants[variant]; !ok {
		return nil, "", "", fmt.Errorf("unknown protocol variant %q", variant)
	}
	for _, t := range []*TemplateRef{p.Scoring.Judge, p.Scoring.Reader} {
		if t == nil {
			continue
		}
		if !filepath.IsLocal(t.File) {
			return nil, "", "", fmt.Errorf("template file must be repository relative")
		}
		file, err := os.ReadFile(filepath.Join(root, t.File))
		if err != nil {
			if rel, ok := strings.CutPrefix(filepath.ToSlash(filepath.Clean(t.File)), "design/upstream/"); ok && os.IsNotExist(err) {
				name, _, _ := strings.Cut(rel, "/")
				return nil, "", "", fmt.Errorf("protocols reference an unavailable upstream copy: %s; restore with python3 -B scripts/fetch_upstream_refs.py --only %s: %w", t.File, name, err)
			}
			return nil, "", "", err
		}
		if digest(file) != t.SHA256 {
			return nil, "", "", fmt.Errorf("template sha256 mismatch: %s", t.File)
		}
	}
	return p, variant, digest(b), nil
}
func digest(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Schema() ([]byte, error) { return suite.SchemaJSON(Protocol{}) }
