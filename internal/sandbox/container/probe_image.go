package container

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"strings"
)

func ProbeBinary(c Config) (path, sha string, err error) {
	path, err = repositoryPath(c.LinuxBinary)
	if err != nil {
		return "", "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", fmt.Errorf("Linux probe binary missing: %s; run scripts/build_linux_memeval.sh", c.LinuxBinary)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("Linux probe binary missing: %s; run scripts/build_linux_memeval.sh", path)
	}
	hash := sha256.Sum256(raw)
	sha = hex.EncodeToString(hash[:])
	if c.LinuxBinarySHA256 != "" && sha != c.LinuxBinarySHA256 {
		return "", "", fmt.Errorf("Linux probe binary SHA256 mismatch")
	}
	f, err := elf.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("probe must be a static Linux ELF: %w", err)
	}
	defer f.Close()
	arch := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[f.Machine]
	if "linux/"+arch != c.Platform {
		return "", "", fmt.Errorf("Linux probe architecture does not match %s", c.Platform)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "", "", fmt.Errorf("Linux probe binary must be static")
		}
	}
	return path, sha, nil
}

func (b *Backend) ensureProbeImage(ctx context.Context) (string, error) {
	path, sha, err := ProbeBinary(b.cfg)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tag := b.cfg.ProbeRepository + ":" + sha[:12]
	var images []struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	err = b.decode(ctx, &images, "image", "inspect", tag)
	if err == nil {
		if len(images) != 1 || images[0].Config.Labels["memeval.probe"] != sha {
			return "", fmt.Errorf("probe image label mismatch")
		}
		return tag, nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no such image") {
		return "", err
	}
	dir, err := sandbox.NewEpisode()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err = privateWrite(filepath.Join(dir, "memeval"), raw); err != nil {
		return "", err
	}
	if err = os.Chmod(filepath.Join(dir, "memeval"), 0755); err != nil {
		return "", err
	}
	// Keep the binary-only image/cache identity. runArgs explicitly mounts /tmp
	// with mode=1777; it does not depend on a COPY-created directory or inherited
	// permissions, and also fixes already cached scratch probe images.
	if err = privateWrite(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY memeval /memeval\n")); err != nil {
		return "", err
	}
	labels := b.labels("probe-image-"+sha[:12], "probe", "probe")
	labels["memeval.probe"] = sha
	args := []string{"build", "--network", "none", "--pull=false", "--platform", b.cfg.Platform, "--tag", tag}
	args = append(args, labelArgs(labels)...)
	args = append(args, dir)
	if _, err = b.call(ctx, args...); err != nil {
		return "", err
	}
	return tag, nil
}
