package container

import (
	"context"
	"errors"
	"fmt"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"time"
)

// FinishRun performs one recovery pass, then independently verifies the run
// labels and every anonymous volume recorded at create time. A cleanup failure
// remains an isolation failure even if a later recovery removes the resource.
func (b *Backend) FinishRun(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	govErr := b.finishGovernor(ctx)
	bindingErr := b.saveGovernorBindings()
	if b.gov != nil && b.gov.temporary {
		defer os.RemoveAll(b.gov.dir)
	}
	b.mu.Lock()
	id := b.runID
	eps := append([]*episode{}, b.order...)
	b.mu.Unlock()
	steps, left, err := b.CleanupScope(ctx, "memeval.run", id)
	residue := map[string]any{"run_clean": err == nil && len(left) == 0, "cleanup_steps": steps, "remaining": left, "containers_remaining": 0, "networks_remaining": 0, "volumes_remaining": 0, "recorded_volumes_remaining": 0, "kept_eps": []string{}, "ep_removed": true}
	for _, r := range left {
		key := r.Kind + "s_remaining"
		residue[key] = residue[key].(int) + 1
	}
	var errs []error
	errs = append(errs, err, govErr, bindingErr)
	roots := map[string]bool{}
	kept := []string{}
	unexpected := []string{}
	for _, ep := range eps {
		roots[filepath.Dir(ep.handle.Dir())] = true
		for volume := range ep.volumes {
			r := Resource{Kind: "volume", ID: volume, Name: volume, Anonymous: true}
			if e := b.ResourceGone(ctx, r); e != nil {
				errs = append(errs, e)
				residue["recorded_volumes_remaining"] = residue["recorded_volumes_remaining"].(int) + 1
			}
		}
		_, e := os.Lstat(ep.handle.Dir())
		if ep.handle.Spec.KeepDir {
			if e == nil {
				kept = append(kept, ep.handle.Dir())
			}
			continue
		}
		if !os.IsNotExist(e) {
			unexpected = append(unexpected, ep.handle.Dir())
			errs = append(errs, fmt.Errorf("episode directory remains: %s", ep.handle.Dir()))
		}
	}
	residue["kept_eps"] = kept
	residue["ep_removed"] = len(unexpected) == 0 && len(kept) == 0
	residue["unkept_eps_removed"] = len(unexpected) == 0
	residue["unexpected_eps"] = unexpected
	// Build directories may belong to another live installation: disclose only.
	builds := []string{}
	for root := range roots {
		paths, e := filepath.Glob(filepath.Join(root, "build-*"))
		errs = append(errs, e)
		builds = append(builds, paths...)
	}
	residue["build_dirs_observed"] = builds
	residue["system_df"] = map[string]any{"before": b.dfBefore, "after": b.systemDF(ctx), "scope": "shared daemon aggregate; no cache cleanup"}
	err = errors.Join(errs...)
	residue["run_clean"] = err == nil
	b.mu.Lock()
	b.runResidue = residue
	b.mu.Unlock()
	if err != nil {
		return &sandbox.Unenforceable{Reason: err.Error()}
	}
	return nil
}
