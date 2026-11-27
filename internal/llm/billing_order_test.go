package llm

import (
	"reflect"
	"slices"
	"testing"
)

func TestBillingIndependentOfCompletionOrder(t *testing.T) {
	entries := []LedgerEntry{}
	for _, v := range []float64{1, 1e-16, 1e-16, 1e-16, .25} {
		entries = append(entries, LedgerEntry{Purpose: "reader", CostNew: new(v), CostOriginal: new(v), PromptTokens: 3, CompletionTokens: 2})
	}
	want := BillingEntries(entries, "reader")
	slices.Reverse(entries)
	if got := BillingEntries(entries, "reader"); !reflect.DeepEqual(got, want) {
		t.Fatal(got, want)
	}
	entries[0].CostOriginal = nil
	if BillingEntries(entries, "").CostOriginal != nil {
		t.Fatal("unknown cost must stay unknown")
	}
}
