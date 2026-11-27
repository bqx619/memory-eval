// Package memora ports the pinned Memora per-subquestion parser and FAMA formula.
package memora

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
)

//go:embed templates.json
var raw []byte

//go:embed context_limits.json
var ContextLimitsJSON []byte
var Templates = func() map[string]string {
	var v map[string]string
	if e := json.Unmarshal(raw, &v); e != nil {
		panic(e)
	}
	return v
}()

func SHA256() string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

type Score struct {
	MPA    float64 `json:"mpa"`
	FAA    float64 `json:"faa"`
	FAMA   float64 `json:"fama"`
	Lambda float64 `json:"lambda"`
}

func FAMA(p, np, f, nf int) Score {
	mpa, faa, lam := 0., 1., 0.
	if np > 0 {
		mpa = float64(p) / float64(np)
	}
	if nf > 0 {
		faa = float64(f) / float64(nf)
	}
	if np+nf > 0 {
		lam = float64(nf) / float64(np+nf)
	}
	penalty := float64(lam * float64(1-faa)) // explicit rounding matches Python, prevents fused multiply-add
	return Score{mpa, faa, math.Max(0, float64(mpa-penalty)), lam}
}
