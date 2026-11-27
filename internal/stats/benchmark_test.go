package stats

import (
	"memeval/internal/result"
	"strings"
	"testing"
)

func TestBenchmarkComparison(t *testing.T) {
	ref := result.BenchmarkRef{ProtocolID: "test", Version: "1", Variant: "s", SHA256: "same", Ref: "protocols/test.json#s", Source: "registry"}
	for _, mode := range []string{"neither", "same", "source differs", "left only", "right only", "id", "version", "variant", "sha"} {
		t.Run(mode, func(t *testing.T) {
			a, b := pair()
			x, y := ref, ref
			a.Protocol.Benchmark = &x
			b.Protocol.Benchmark = &y
			mismatch := false
			switch mode {
			case "neither":
				a.Protocol.Benchmark = nil
				b.Protocol.Benchmark = nil
			case "source differs":
				y.Source = "suite"
				y.Ref = "different-location"
			case "left only":
				b.Protocol.Benchmark = nil
			case "right only":
				a.Protocol.Benchmark = nil
			case "id":
				y.ProtocolID = "other"
				mismatch = true
			case "version":
				y.Version = "2"
				mismatch = true
			case "variant":
				y.Variant = "m"
				mismatch = true
			case "sha":
				y.SHA256 = "different"
				mismatch = true
			}
			o := DefaultOptions()
			o.Metric = "recall"
			c, err := Compare(t.Context(), a, b, o)
			if mismatch {
				if err == nil || err.Error() != "protocol differs: benchmark" {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			one := mode == "left only" || mode == "right only"
			if one != (len(c.ProtocolDifferences) == 1) {
				t.Fatal(c.ProtocolDifferences)
			}
			if one && !strings.Contains(c.ProtocolDifferences[0], "benchmark declaration present on one side only (test#s)") {
				t.Fatal(c.ProtocolDifferences)
			}
		})
	}
}
