package container

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The fixture must retain labels at build time, inherit them via either a tag
// or image ID, and overlay create labels without modifying the original image.
func TestImageLabelInheritance(t *testing.T) {
	b, _ := fixture(t)
	probe, err := b.ensureProbeImage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"project", "bridge", "assets"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch AS env\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := "memeval/env:label-fixture"
	if _, err := b.call(t.Context(), "build", "--target", "env", "--platform", b.cfg.Platform,
		"--tag", env, "--iidfile", filepath.Join(dir, "iid"),
		"--label", "memeval.env=image-env@lock", "--label", "memeval.input=image-input", dir); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{probe, env} {
		image, err := b.Resource(t.Context(), "image", tag)
		if err != nil {
			t.Fatal(err)
		}
		if image.ID == tag || len(image.Labels) == 0 {
			t.Fatal("fixture must expose a distinct image ID and baked labels", image)
		}
		if tag == probe && (!strings.HasPrefix(image.Labels["memeval.episode"], "probe-image-") || image.Labels["memeval.probe"] == "") {
			t.Fatal("probe build lost its image labels", image)
		}
		for _, ref := range []string{tag, image.ID} {
			for _, override := range []bool{false, true} {
				name := "memeval-label-" + nonce()
				args := []string{"create", "--name", name}
				want := maps.Clone(image.Labels)
				if override {
					labels := map[string]string{"memeval.episode": "container-episode", "memeval.env": "", "memeval.role": "fixture"}
					maps.Copy(want, labels)
					args = append(args, labelArgs(labels)...)
				}
				if _, err := b.call(t.Context(), append(args, ref)...); err != nil {
					t.Fatal(err)
				}
				v, err := b.inspectContainer(t.Context(), name)
				if err != nil || !reflect.DeepEqual(v.Config.Labels, want) {
					t.Fatal(ref, override, v.Config.Labels, want, err)
				}
				if _, err := b.call(t.Context(), "rm", "-f", name); err != nil {
					t.Fatal(err)
				}
			}
		}
		after, err := b.Resource(t.Context(), "image", tag)
		if err != nil || !reflect.DeepEqual(image.Labels, after.Labels) {
			t.Fatal("create modified baked image labels", image, after, err)
		}
	}
}
