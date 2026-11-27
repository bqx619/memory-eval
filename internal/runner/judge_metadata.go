package runner

import (
	"fmt"
	"memeval/internal/result"
	"memeval/internal/scoring/judge/inputlog"
	"memeval/internal/scoring/prefeval"
)

func decisionJudgeInfo(f *FixedReaderOptions) *result.JudgeInfo {
	return &result.JudgeInfo{Kind: "decision", Provider: "typesafe", Model: f.JudgeDecisionModel, Cutoff: .5, MultiValued: "argmax", Templates: map[string]inputlog.Template{}}
}
func decisionJudgeMetadata(r *result.Report, o Options) {
	if r.Protocol.Judge == nil {
		return
	}
	r.FrameworkVersion = "0.17.0-p5"
	official := map[string]string{"longmemeval": "gpt-4o-2024-08-06", "beam": "gpt-4.1-mini", "memora": "official three-judge majority", "memoryagentbench": "gpt-4o", "halumem": "gpt-4o", "convomem": "Gemini 2.5 Flash", "prefeval": "Claude 3 Sonnet"}[o.benchmarkID]
	officialScore := r.Scores.Official.(result.Official)
	compatible := false
	officialScore.JudgeCompatible = &compatible
	officialScore.Fidelity = "partial"
	officialScore.Deviations = appendUnique(officialScore.Deviations, fmt.Sprintf("official judge %s replaced by TypeSafe %s decision model; P(true)>=0.5 / argmax", official, o.FixedReader.JudgeDecisionModel))
	if o.benchmarkID == "prefeval" {
		officialScore.Deviations = appendUnique(officialScore.Deviations, prefeval.HallucinateDeviation)
	}
	r.Scores.Official = officialScore
}
