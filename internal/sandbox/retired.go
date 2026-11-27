package sandbox

import "fmt"

// RetiredTier identifies an explicitly selected backend that is no longer available.
type RetiredTier struct{ Tier string }

func (e *RetiredTier) Error() string {
	return fmt.Sprintf("sandbox tier %q was retired on 2026-10-18; see design/p8-container-platform.md", e.Tier)
}

// CheckTier rejects retired execution paths before installation or lock lookup.
func CheckTier(tier string) error {
	if tier == "microvm" || tier == "process" {
		return &RetiredTier{Tier: tier}
	}
	return nil
}
