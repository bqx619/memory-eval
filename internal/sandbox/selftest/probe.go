package selftest

import (
	"encoding/json"
	"fmt"
	"io"
	"memeval/internal/sandbox/container"
)

type Observation = container.Observation
type ProbeResult = container.ProbeResult

// Probe snapshots descriptors before any cleanup or evaluator initialization.
func Probe(args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("fd probe takes no arguments")
	}
	fds, observed, err := openFDs()
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(ProbeResult{Items: map[string]Observation{"inherited_fds": {Success: len(fds) == 0}}, ExtraFDs: fds, ObservedFDs: observed})
}
