package sut

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ioCopy = io.Copy

func copyFile(src, dst string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	st, e := in.Stat()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
		return e
	}
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm()&0777)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	ce := out.Close()
	if e != nil {
		return e
	}
	return ce
}
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0700)
		}
		if d.Type()&os.ModeSymlink != 0 {
			v, e := os.Readlink(p)
			if e != nil {
				return e
			}
			resolved := v
			if !filepath.IsAbs(v) {
				resolved = filepath.Join(filepath.Dir(p), v)
			}
			r, e := filepath.Rel(src, resolved)
			if e != nil || r == ".." || strings.HasPrefix(r, "../") {
				return fmt.Errorf("source link escapes runtime: %s", p)
			}
			v, e = filepath.Rel(filepath.Dir(target), filepath.Join(dst, r))
			if e != nil {
				return e
			}
			if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
				return e
			}
			return os.Symlink(v, target)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported install file type: %s", p)
		}
		return copyFile(p, target)
	})
}
func inventory(root string) (map[string]string, error) {
	out := map[string]string{}
	e := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "env.lock.json" {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		prefix := fmt.Sprintf("%o:", st.Mode().Perm())
		if d.Type()&os.ModeSymlink != 0 {
			v, e := os.Readlink(p)
			if e != nil {
				return e
			}
			r, e := filepath.EvalSymlinks(p)
			if e != nil {
				return e
			}
			if !within(r, root) {
				return fmt.Errorf("environment symlink escapes root: %s", rel)
			}
			out[rel] = prefix + "symlink:" + v
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular installed file: %s", rel)
		}
		h, e := FileHash(p)
		if e == nil {
			out[rel] = prefix + h
		}
		return e
	})
	return out, e
}
func within(p, root string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, "../")
}
func fetchArtifact(ctx context.Context, raw, dst string, offline bool) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.Scheme == "file" {
		if u.Host != "" && u.Host != "localhost" {
			return fmt.Errorf("non-local file URL")
		}
		return copyFile(u.Path, dst)
	}
	if u.Scheme == "" {
		return copyFile(raw, dst)
	}
	if offline {
		return fmt.Errorf("offline install cannot fetch %s", u.Host)
	}
	if u.Scheme != "https" || u.User != nil {
		return fmt.Errorf("artifact requires HTTPS without credentials")
	}
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 10 || req.URL.Scheme != "https" {
			return fmt.Errorf("unsafe artifact redirect")
		}
		return nil
	}}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return e
	}
	r, e := client.Do(req)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("artifact HTTP %d", r.StatusCode)
	}
	f, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	n, e := io.Copy(f, io.LimitReader(r.Body, 4<<30))
	ce := f.Close()
	if e != nil {
		return e
	}
	if n == 4<<30 {
		return fmt.Errorf("artifact exceeds 4 GiB")
	}
	return ce
}
func runtimeArchive(ctx context.Context, m *Manifest, o InstallOptions, temp string, hashes map[string]string) (string, error) {
	a, ok := m.Runtime.Sources[platform()]
	if o.RuntimeArchive != "" {
		a = Artifact{URL: o.RuntimeArchive, SHA256: o.RuntimeSHA256}
		ok = true
	}
	if !ok && m.Runtime.Kind == "node" {
		osName := map[string]string{"darwin": "darwin", "linux": "linux"}[strings.Split(platform(), "-")[0]]
		arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[strings.Split(platform(), "-")[1]]
		if osName == "" || arch == "" {
			return "", fmt.Errorf("unsupported node platform")
		}
		file := "node-v" + m.Runtime.Version + "-" + osName + "-" + arch + ".tar.gz"
		base := "https://nodejs.org/dist/v" + m.Runtime.Version + "/"
		sums := filepath.Join(temp, "SHASUMS256.txt")
		if e := fetchArtifact(ctx, base+"SHASUMS256.txt", sums, o.Offline); e != nil {
			return "", e
		}
		raw, e := os.ReadFile(sums)
		if e != nil {
			return "", e
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == file {
				a = Artifact{URL: base + file, SHA256: fields[0]}
				ok = true
				break
			}
		}
		h, e := FileHash(sums)
		if e != nil {
			return "", e
		}
		hashes["node-official-checksums"] = h
	}
	if !ok || !shaPattern.MatchString(a.SHA256) {
		return "", fmt.Errorf("platform artifact with verified SHA256 required")
	}
	dst := filepath.Join(temp, "runtime.tar.gz")
	if e := fetchArtifact(ctx, a.URL, dst, o.Offline); e != nil {
		return "", e
	}
	h, e := FileHash(dst)
	if e != nil {
		return "", e
	}
	if h != a.SHA256 {
		return "", fmt.Errorf("runtime SHA256 mismatch")
	}
	hashes["runtime"] = h
	return dst, nil
}
func unpackRuntime(src, dst string) error { return unpackArchive(src, dst, 1) }
func unpackArchive(src, dst string, strip int) error {
	return unpackArchiveWithLink(src, dst, strip, os.Link)
}

func unpackArchiveWithLink(src, dst string, strip int, link func(string, string) error) error {
	if strip != 0 && strip != 1 {
		return fmt.Errorf("unsupported archive strip_components: %d", strip)
	}
	f, e := os.Open(src)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return fmt.Errorf("runtime must be a gzip tar archive: %w", e)
	}
	defer gz.Close()
	if e = os.MkdirAll(dst, 0700); e != nil {
		return e
	}
	dst, e = filepath.EvalSymlinks(dst)
	if e != nil {
		return e
	}
	dst, e = filepath.Abs(dst)
	if e != nil {
		return e
	}
	tr := tar.NewReader(gz)
	top := ""
	links := map[string]string{}
	// Only files successfully materialized by this archive may be link targets.
	regular := map[string]os.FileInfo{}
	total := int64(0)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		// Reader.Next absorbs per-file PAX and GNU long-name/link headers, but
		// exposes PAX global headers. They describe metadata, not output paths.
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, e := archiveName(h.Name)
		if e != nil {
			return e
		}
		parts := strings.SplitN(name, "/", 2)
		rel := name
		if strip == 1 {
			if top == "" {
				top = parts[0]
			}
			if parts[0] != top {
				return fmt.Errorf("archive must have one root directory")
			}
			if len(parts) == 1 {
				if h.Typeflag != tar.TypeDir {
					return fmt.Errorf("archive requires root directory")
				}
				continue
			}
			rel = parts[1]
		} else if name == "." {
			continue
		}

		target := filepath.Join(dst, rel)
		if e := archivePath(dst, target, links); e != nil {
			return e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			e = os.MkdirAll(target, 0700)
		case tar.TypeReg:
			total += h.Size
			if total > 4<<30 {
				return fmt.Errorf("expanded runtime exceeds 4 GiB")
			}
			if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
				return e
			}
			out, er := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777)
			if er != nil {
				return er
			}
			_, e = io.Copy(out, tr)
			ce := out.Close()
			if e == nil {
				e = ce
			}
		case tar.TypeLink:
			name, err := archiveName(h.Linkname)
			if err != nil {
				return fmt.Errorf("unsafe archive hard link: %w", err)
			}
			// tar hard-link names are relative to the archive root, unlike
			// symlinks. Apply strip_components to both entry and link names.
			if strip == 1 {
				parts := strings.SplitN(name, "/", 2)
				if len(parts) != 2 || parts[0] != top {
					return fmt.Errorf("hard link target must share archive root")
				}
				name = parts[1]
			}
			source := filepath.Join(dst, name)
			if err = archivePath(dst, source, links); err != nil {
				return err
			}
			previous, ok := regular[source]
			st, err := os.Lstat(source)
			if !ok || err != nil || !st.Mode().IsRegular() || !os.SameFile(previous, st) {
				return fmt.Errorf("hard link target must be a previously extracted regular file: %q", h.Linkname)
			}
			// Count logical bytes so link support and copy fallback have the same
			// expansion limit (many links must not amplify fallback beyond it).
			total += st.Size()
			if total > 4<<30 {
				return fmt.Errorf("expanded runtime exceeds 4 GiB")
			}
			if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
				return e
			}
			e = linkOrCopyArchiveFile(source, target, link)
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || !within(filepath.Clean(filepath.Join(filepath.Dir(target), h.Linkname)), dst) {
				return fmt.Errorf("archive symlink escapes runtime")
			}
			links[target] = h.Linkname
		default:
			return fmt.Errorf("unsupported runtime archive entry type")
		}
		if e != nil {
			return e
		}
		if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeLink {
			regular[target], e = os.Lstat(target)
			if e != nil {
				return e
			}
		}
	}
	for p, v := range links {
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		if e = os.Symlink(v, p); e != nil {
			return e
		}
	}
	return nil
}

func archiveName(raw string) (string, error) {
	name := filepath.ToSlash(filepath.Clean(raw))
	if raw == "" || strings.ContainsRune(raw, 0) || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("unsafe archive path")
	}
	return name, nil
}

// Deferred archive symlinks and pre-existing filesystem symlinks must never
// become an extraction path or a hard-link source, including through parents.
func archivePath(root, target string, links map[string]string) error {
	if !within(target, root) {
		return fmt.Errorf("archive path escapes runtime")
	}
	for p := target; p != root; p = filepath.Dir(p) {
		if _, ok := links[p]; ok {
			return fmt.Errorf("archive path uses a symlink")
		}
		st, err := os.Lstat(p)
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive path uses a symlink")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func linkOrCopyArchiveFile(src, dst string, link func(string, string) error) error {
	if err := link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		// OpenFile's mode is affected by umask; a hard link preserves the
		// source's actual mode, so the fallback must do so explicitly too.
		err = out.Chmod(st.Mode().Perm())
	}
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(dst)
	}
	return closeErr
}

func isGzip(path string) bool {
	f, e := os.Open(path)
	if e != nil {
		return false
	}
	defer f.Close()
	var b [2]byte
	_, e = io.ReadFull(f, b[:])
	return e == nil && b == [2]byte{0x1f, 0x8b}
}
