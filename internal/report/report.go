package report

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"memeval/internal/config/redact"
	"memeval/internal/result"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func Write(ctx context.Context, path string, data []byte) error {
	data = redact.From(ctx).Encoded(data)
	return writeBytes(ctx, path, data)
}

// writeBytes receives data that has already crossed its typed redaction boundary.
// Never replace bytes in serialized numbers, CSV syntax, or embedded UI code.
func writeBytes(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".memeval-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func WriteJSON(ctx context.Context, path string, v any) error {
	safe := v
	if len(redact.From(ctx).Values) > 0 {
		clean, err := redact.From(ctx).Marshal(v)
		if err != nil {
			return err
		}
		d := json.NewDecoder(bytes.NewReader(clean))
		d.UseNumber()
		if err = d.Decode(&safe); err != nil {
			return err
		}
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(safe); err != nil {
		return err
	}
	return writeBytes(ctx, path, b.Bytes())
}
func Load(ctx context.Context, path string) (*result.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r result.Report
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.SuiteSHA256 == "" || r.Protocol.Version == "" || r.Cases == nil {
		return nil, fmt.Errorf("invalid report: missing identity, protocol or cases")
	}
	return &r, nil
}
func Save(ctx context.Context, r *result.Report, dir string) error {
	if len(redact.From(ctx).Values) > 0 {
		data, err := redact.From(ctx).Marshal(r)
		if err != nil {
			return err
		}
		var safe result.Report
		if err = json.Unmarshal(data, &safe); err != nil {
			return err
		}
		for i, c := range r.Cases {
			if len(c.PreservedJSON) > 0 {
				raw, e := redact.From(ctx).JSON(c.PreservedJSON)
				if e != nil {
					return e
				}
				safe.Cases[i].PreservedJSON = raw
			}
		}
		r = &safe
	}
	var lines bytes.Buffer
	e := json.NewEncoder(&lines)
	e.SetEscapeHTML(false)
	for _, c := range r.Cases {
		if err := e.Encode(c); err != nil {
			return err
		}
	}
	if r.Mode == "fixed_reader" {
		var predictions bytes.Buffer
		encoder := json.NewEncoder(&predictions)
		if r.Protocol.Benchmark != nil && r.Protocol.Benchmark.ProtocolID == "locomo" {
			if err := locomoPredictions(encoder, r.Cases); err != nil {
				return err
			}
		} else {
			for _, c := range r.Cases {
				if c.QA != nil && c.QA.Hypothesis != nil {
					if err := encoder.Encode(map[string]any{"question_id": c.CaseID, "hypothesis": *c.QA.Hypothesis, "autoeval_label": map[string]any{"label": c.QA.JudgeLabel}, "repeat_index": c.RepeatIndex}); err != nil {
						return err
					}
				}
			}
		}
		if err := writeBytes(ctx, filepath.Join(dir, "predictions.jsonl"), predictions.Bytes()); err != nil {
			return err
		}
	}
	var csvBytes bytes.Buffer
	csvBytes.WriteString("\ufeff")
	w := csv.NewWriter(&csvBytes)
	if err := w.Write([]string{"case_id", "sample_id", "repeat_index", "category", "status", "passed", "returned_chars", "query_ms", "failures"}); err != nil {
		return err
	}
	for _, c := range r.Cases {
		passed, chars, timing := "", "", ""
		if c.Passed != nil {
			passed = strconv.FormatBool(*c.Passed)
		}
		if c.ReturnedChars != nil {
			chars = strconv.Itoa(*c.ReturnedChars)
		}
		if v, ok := c.Timings["query_ms"]; ok {
			timing = strconv.FormatFloat(v, 'g', -1, 64)
		}
		if err := w.Write([]string{c.CaseID, c.SampleID, strconv.Itoa(c.RepeatIndex), c.Category, string(c.Status), passed, chars, timing, strings.Join(c.Failures, " | ")}); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{"cases.jsonl", lines.Bytes()}, {"cases.csv", csvBytes.Bytes()}} {
		if err := writeBytes(ctx, filepath.Join(dir, f.name), f.data); err != nil {
			return err
		}
	}
	if err := HTML(ctx, []*result.Report{r}, filepath.Join(dir, "report.html")); err != nil {
		return err
	}
	return WriteJSON(ctx, filepath.Join(dir, "report.json"), r)
}
func SaveComparison(ctx context.Context, c *result.Comparison, path string) error {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	for _, row := range c.ResampleIndices {
		if err := e.Encode(row); err != nil {
			return err
		}
	}
	if err := writeBytes(ctx, filepath.Join(filepath.Dir(path), "resample_indices.jsonl"), b.Bytes()); err != nil {
		return err
	}
	return WriteJSON(ctx, path, c)
}

// WriteJSONL redacts structured strings before serialization and writes an empty
// regular file for an empty review queue.
func WriteJSONL(ctx context.Context, path string, rows []any) error {
	var b bytes.Buffer
	for _, row := range rows {
		wire, err := redact.From(ctx).Marshal(row)
		if err != nil {
			return err
		}
		b.Write(wire)
		b.WriteByte('\n')
	}
	return writeBytes(ctx, path, b.Bytes())
}

func locomoPredictions(enc *json.Encoder, cases []result.Case) error {
	type group struct {
		SampleID    string           `json:"sample_id"`
		QA          []map[string]any `json:"qa"`
		RepeatIndex int              `json:"repeat_index"`
	}
	groups := []group{}
	indexes := map[string]int{}
	for _, c := range cases {
		if c.QA == nil || c.QA.Hypothesis == nil || c.PredictionInput == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d", c.EpisodeID, c.RepeatIndex)
		i, ok := indexes[key]
		if !ok {
			i = len(groups)
			indexes[key] = i
			groups = append(groups, group{SampleID: c.EpisodeID, QA: []map[string]any{}, RepeatIndex: c.RepeatIndex})
		}
		p := c.PredictionInput
		groups[i].QA = append(groups[i].QA, map[string]any{"question": p.Question, "answer": p.Answer, "category": p.Category, "prediction": *c.QA.Hypothesis})
	}
	for _, g := range groups {
		if err := enc.Encode(g); err != nil {
			return err
		}
	}
	return nil
}
