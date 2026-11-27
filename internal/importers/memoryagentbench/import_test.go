package memoryagentbench

import (
	"context"
	"encoding/json"
	"flag"
	"memeval/internal/importers"
	"memeval/internal/importers/conformance"
	"memeval/internal/suite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "update reviewed MAB conformance goldens")

func TestConformance(t *testing.T) {
	for _, split := range sorted(Splits) {
		t.Run(split, func(t *testing.T) {
			conformance.Run(t, conformance.Spec{Name: split, Import: func(ctx context.Context) (*suite.Suite, importers.Stats, error) {
				rs, e := ImportFile(ctx, "testdata/"+split+".json", split, filepath.Join(t.TempDir(), "bundle"), 4096, true)
				if e != nil {
					return nil, importers.Stats{}, e
				}
				if len(rs) != 1 {
					t.Fatal(len(rs))
				}
				s, e := suite.Open(ctx, rs[0].Path)
				if e == nil {
					e = s.Iter(ctx, func(ep *suite.Episode) error {
						if len(ep.Events) > 3 {
							t.Fatal("fixture over 3 blocks")
						}
						for _, c := range ep.Cases {
							if c.Cutoff != len(ep.Events) || c.ClusterID != ep.ID {
								t.Fatal("cutoff/cluster")
							}
						}
						return nil
					})
				}
				return s, rs[0].Statistics, e
			}, GoldenSuite: "testdata/" + split + ".golden.json", GoldenStats: "testdata/" + split + ".golden.import.json", Update: *update})
		})
	}
}
func TestChunkSemantics(t *testing.T) {
	c, e := NewChunker()
	if e != nil {
		t.Fatal(e)
	}
	if c.Tokens("<|endoftext|>") != 1 {
		t.Fatal("special token")
	}
	for _, limit := range []int{512, 4096} {
		text := strings.Repeat("word ", limit+20) + "."
		cs, e := c.Chunk(text, limit)
		if e != nil || len(cs) != 2 || cs[0].Text != "" || cs[1].Tokens <= limit {
			t.Fatalf("oversized upstream sentence: %d %v %v", limit, len(cs), e)
		}
	}
	if cs, e := c.Chunk("", 512); e != nil || len(cs) != 0 {
		t.Fatal(cs, e)
	}
}
func TestOverlapEvidencePrivate(t *testing.T) {
	ch, _ := NewChunker()
	r := Row{Context: `['Chat Time: yesterday', [{'role':'user','content':'fact X'}, {'role':'assistant','content':'okay'}]]`, Questions: []string{"Which fact?"}, Answers: [][]string{{"X"}}, Metadata: Metadata{Source: "longmemeval_s*", QuestionIDs: []string{"original-id"}, QuestionTypes: []string{"single-session-user"}, HaystackSessions: [][][]Turn{{{{Role: "user", Content: "fact X", HasAnswer: true}, {Role: "assistant", Content: "okay"}}}}}}
	ep, _, ns, e := episode(r, 0, ch, 4096)
	if e != nil || ns != 1 {
		t.Fatal(e)
	}
	if len(ep.Cases[0].Gold.EvidenceIDs) != 1 || ep.Cases[0].Gold.EvidenceIDs[0] != ep.Events[0].SourceID {
		t.Fatal(ep.Cases)
	}
	b, _ := json.Marshal(ep.Events)
	if strings.Contains(string(b), "has_answer") || strings.Contains(string(b), "original-id") || strings.Contains(string(b), "yesterday") {
		t.Fatal("private evidence/date exposed")
	}
	r.Metadata.HaystackSessions[0][0][0].Content = "missing"
	if _, _, _, e = episode(r, 0, ch, 4096); e == nil {
		t.Fatal("silently discarded labeled evidence")
	}
}
func TestInvalidInputsAndAtomicity(t *testing.T) {
	for _, tokens := range []int{0, 4000} {
		if _, e := Import(t.Context(), Options{Split: "accurate_retrieval", ChunkTokens: tokens}); e == nil {
			t.Fatal(tokens)
		}
	}
	p := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(p, []byte(`[{"context":"text.","questions":["Q"],"answers":[],"metadata":{"source":"eventqa_full"}}]`), 0600)
	out := filepath.Join(t.TempDir(), "bundle")
	if _, e := ImportFile(t.Context(), p, "accurate_retrieval", out, 4096, true); e == nil {
		t.Fatal("bad row accepted")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("partial destination")
	}
}
