// Package halumem mirrors aggregate_eval_results over evaluator-only records.
package halumem

import (
	"slices"
	"strings"
)

type Integrity struct {
	Source     string  `json:"memory_source"`
	Type       string  `json:"memory_type"`
	Importance float64 `json:"importance"`
	Score      *int    `json:"memory_integrity_score"`
}
type Accuracy struct {
	Score    *int `json:"memory_accuracy_score"`
	Included any  `json:"is_included_in_golden_memories"`
}
type Update struct {
	Type       string  `json:"memory_type"`
	Importance float64 `json:"importance"`
	Label      *string `json:"memory_update_type"`
}
type QA struct {
	Type  string  `json:"question_type"`
	Label *string `json:"result_type"`
}
type Records struct {
	Integrity []Integrity `json:"memory_integrity_records"`
	Accuracy  []Accuracy  `json:"memory_accuracy_records"`
	Update    []Update    `json:"memory_update_records"`
	QA        []QA        `json:"question_answering_records"`
}

func NewRecords() Records { return Records{[]Integrity{}, []Accuracy{}, []Update{}, []QA{}} }
func (r *Records) Append(v Records) {
	r.Integrity = append(r.Integrity, v.Integrity...)
	r.Accuracy = append(r.Accuracy, v.Accuracy...)
	r.Update = append(r.Update, v.Update...)
	r.QA = append(r.QA, v.QA...)
}
func Aggregate(r Records) map[string]*float64 {
	out := map[string]*float64{}
	count := func(k string, v float64) { out["halumem."+k] = &v }
	ratio := func(k string, a, b float64) {
		out["halumem."+k] = nil
		if b != 0 {
			count(k, a/b)
		}
	}
	var hits, weighted, n, nv, w, wv, inter, interv, interhits float64
	types := map[string][3]float64{"Persona Memory": {}, "Relationship Memory": {}, "Event Memory": {}}
	for _, v := range r.Integrity {
		t := types[v.Type]
		t[2]++
		if v.Score != nil && *v.Score == 2 {
			t[0]++
		}
		types[v.Type] = t
		interference := v.Source == "interference"
		if interference {
			inter++
		} else {
			n++
			w += v.Importance
		}
		if v.Score == nil {
			continue
		}
		if interference {
			interv++
			if *v.Score == 0 {
				interhits++
			}
		} else {
			nv++
			wv += v.Importance
			weighted += .5 * float64(*v.Score) * v.Importance
			if *v.Score == 2 {
				hits++
			}
		}
	}
	ratio("memory_integrity.recall(all)", hits, n)
	ratio("memory_integrity.recall(valid)", hits, nv)
	ratio("memory_integrity.weighted_recall(all)", weighted, w)
	ratio("memory_integrity.weighted_recall(valid)", weighted, wv)
	count("memory_integrity.memory_valid_importance_sum", wv)
	count("memory_integrity.memory_importance_sum", w)
	count("memory_integrity.memory_valid_num", nv)
	count("memory_integrity.memory_num", n)
	ratio("memory_accuracy.interference_accuracy(all)", interhits, inter)
	ratio("memory_accuracy.interference_accuracy(valid)", interhits, interv)
	count("memory_accuracy.interference_memory_valid_num", interv)
	count("memory_accuracy.interference_memory_num", inter)
	var target, targetv, targets, all, valid, sum float64
	for _, v := range r.Accuracy {
		all++
		s, _ := v.Included.(string)
		included := s == "true" || s == "True"
		if included {
			target++
		}
		if v.Score == nil {
			continue
		}
		valid++
		sum += .5 * float64(*v.Score)
		if included {
			targetv++
			targets += .5 * float64(*v.Score)
		}
	}
	ratio("memory_accuracy.target_accuracy(all)", targets, target)
	ratio("memory_accuracy.target_accuracy(valid)", targets, targetv)
	ratio("memory_accuracy.weighted_accuracy(all)", sum, all)
	ratio("memory_accuracy.weighted_accuracy(valid)", sum, valid)
	count("memory_accuracy.target_memory_valid_num", targetv)
	count("memory_accuracy.target_memory_num", target)
	count("memory_accuracy.memory_valid_num", valid)
	count("memory_accuracy.memory_num", all)
	out["halumem.memory_extraction_f1"] = nil
	p, rec := out["halumem.memory_accuracy.target_accuracy(all)"], out["halumem.memory_integrity.recall(all)"]
	if p != nil && rec != nil {
		f := 0.
		if *p+*rec != 0 {
			f = 2 * *p * *rec / (*p + *rec)
		}
		count("memory_extraction_f1", f)
	}
	labels := func(prefix, unit string, options []string, records []*string) {
		counts := map[string]float64{}
		valid := 0.
		for _, v := range records {
			if v != nil && slices.Contains(options, *v) {
				valid++
				counts[*v]++
			}
		}
		for _, label := range options {
			ratio(prefix+"."+strings.ToLower(label)+"_"+unit+"_ratio(all)", counts[label], float64(len(records)))
			ratio(prefix+"."+strings.ToLower(label)+"_"+unit+"_ratio(valid)", counts[label], valid)
		}
		count(prefix+"."+unit+"_valid_num", valid)
		count(prefix+"."+unit+"_num", float64(len(records)))
	}
	us := []*string{}
	for _, v := range r.Update {
		us = append(us, v.Label)
		t := types[v.Type]
		t[2]++
		if v.Label != nil && *v.Label == "Correct" {
			t[1]++
		}
		types[v.Type] = t
	}
	labels("memory_update", "update_memory", []string{"Correct", "Hallucination", "Omission", "Other"}, us)
	qs := []*string{}
	for _, v := range r.QA {
		qs = append(qs, v.Label)
	}
	labels("question_answering", "qa", []string{"Correct", "Hallucination", "Omission"}, qs)
	for typ, t := range types {
		prefix := "memory_type_accuracy." + typ + "."
		ratio(prefix+"memory_integrity_acc", t[0], t[2])
		ratio(prefix+"memory_update_acc", t[1], t[2])
		ratio(prefix+"memory_acc", t[0]+t[1], t[2])
		count(prefix+"total_num", t[2])
	}
	return out
}
