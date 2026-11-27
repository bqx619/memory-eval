package sut

import (
	"context"
	"errors"
	"fmt"
	"io"
	"memeval/internal/sandbox/container"
	"strings"
)

func verifySidecarImage(v imageInfo, ref, platform string) error {
	_, digest, _ := strings.Cut(ref, "@")
	matched := false
	for _, r := range v.RepoDigests {
		if strings.HasSuffix(r, "@"+digest) {
			matched = true
		}
	}
	if !matched || v.Os+"/"+v.Architecture != platform || v.ID == "" {
		return fmt.Errorf("sidecar image digest/platform mismatch: %s", ref)
	}
	return nil
}
func installSidecarImages(ctx context.Context, b *container.Backend, m *Manifest, l *Lock, log io.Writer) error {
	for _, s := range m.Sidecars {
		if s.Image == "" {
			continue
		}
		info, err := inspectImage(ctx, b, s.Image)
		var absent *NotInstalled
		if errors.As(err, &absent) && absent.Reason == "image_missing" {
			// Pull only a declared digest, never a mutable tag. The build logger redacts
			// credentials and captures errors together with the image build diagnostics.
			if err = b.Build(ctx, log, "pull", "--platform", b.Platform(), s.Image); err != nil {
				return err
			}
			info, err = inspectImage(ctx, b, s.Image)
		}
		if err != nil {
			return err
		}
		if err = verifySidecarImage(info, s.Image, b.Platform()); err != nil {
			return err
		}
		_, digest, _ := strings.Cut(s.Image, "@")
		l.SidecarImages = append(l.SidecarImages, SidecarImage{Name: s.Name, Ref: s.Image, ID: info.ID, Digest: digest, Platform: b.Platform()})
	}
	return nil
}
func checkSidecarImages(ctx context.Context, b *container.Backend, l *Lock) error {
	for _, s := range l.SidecarImages {
		info, err := inspectImage(ctx, b, s.Ref)
		if err != nil {
			return err
		}
		if err = verifySidecarImage(info, s.Ref, s.Platform); err != nil {
			return &NotInstalled{Manifest: l.Name, Reason: "image_missing", Detail: err.Error()}
		}
		_, digest, _ := strings.Cut(s.Ref, "@")
		if info.ID != s.ID || s.Digest != digest || s.Platform != l.Platform.OS+"/"+l.Platform.Arch {
			return &NotInstalled{Manifest: l.Name, Reason: "image_missing", Detail: "sidecar lock identity changed: " + s.Name}
		}
	}
	return nil
}
