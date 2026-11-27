package sut

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// P8 appendix A contains the verified multi-platform image digests.
//
//go:embed sidecar_presets.json
var presetData []byte

func sidecarPreset(name string) (Sidecar, error) {
	var all map[string]Sidecar
	if err := json.Unmarshal(presetData, &all); err != nil {
		return Sidecar{}, err
	}
	p, ok := all[name]
	if !ok {
		return Sidecar{}, fmt.Errorf("sidecar preset %q is not locked; supply a digest-pinned image", name)
	}
	return p, nil
}
