package sut

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"memeval/internal/sandbox"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

type archiveEntry struct {
	header tar.Header
	body   string
}

func writeArchiveEntries(t *testing.T, entries ...archiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		h := entry.header
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []io.Closer{tw, gz, f} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func linkedArchiveEntries(prefix string) []archiveEntry {
	return []archiveEntry{
		{tar.Header{Name: prefix + "share/timezone", Typeflag: tar.TypeDir, Mode: 0755}, ""},
		{tar.Header{Name: prefix + "bin/postgres", Typeflag: tar.TypeReg, Mode: 0751}, "postgres fixture\n"},
		{tar.Header{Name: prefix + "bin/initdb", Typeflag: tar.TypeReg, Mode: 0755}, "init fixture\n"},
		{tar.Header{Name: prefix + "lib/vector.so", Typeflag: tar.TypeReg, Mode: 0644}, "extension fixture\n"},
		{tar.Header{Name: prefix + "share/timezone/UTC", Typeflag: tar.TypeReg, Mode: 0640}, "timezone bytes\x00\xff\n"},
		// Link mode is deliberately different: the target inode's mode wins.
		{tar.Header{Name: prefix + "share/timezone/Etc/UTC", Typeflag: tar.TypeLink, Linkname: prefix + "share/timezone/UTC", Mode: 0777}, ""},
		{tar.Header{Name: prefix + "share/timezone/Universal", Typeflag: tar.TypeLink, Linkname: prefix + "share/timezone/Etc/UTC"}, ""},
		{tar.Header{Name: prefix + "share/timezone/Local", Typeflag: tar.TypeSymlink, Linkname: "Etc/UTC"}, ""},
	}
}

func TestArchiveHardLinksInventoryAndFallback(t *testing.T) {
	for _, strip := range []int{0, 1} {
		t.Run(fmt.Sprint(strip), func(t *testing.T) {
			prefix := ""
			if strip == 1 {
				prefix = "postgres-lite/"
			}
			archive := writeArchiveEntries(t, linkedArchiveEntries(prefix)...)
			var want map[string]string
			for _, fallback := range []bool{false, true} {
				dst := t.TempDir()
				dst, err := sandbox.Real(dst) // inventory receives canonical env roots.
				if err != nil {
					t.Fatal(err)
				}
				link := os.Link
				if fallback {
					link = func(string, string) error { return syscall.EXDEV }
				}
				if err := unpackArchiveWithLink(archive, dst, strip, link); err != nil {
					t.Fatal(err)
				}
				original, err := os.Stat(filepath.Join(dst, "share/timezone/UTC"))
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"share/timezone/Etc/UTC", "share/timezone/Universal"} {
					st, err := os.Lstat(filepath.Join(dst, name))
					if err != nil {
						t.Fatal(err)
					}
					if !st.Mode().IsRegular() || st.Mode().Perm() != original.Mode().Perm() || os.SameFile(original, st) == fallback {
						t.Fatal(name, fallback, st)
					}
					b, err := os.ReadFile(filepath.Join(dst, name))
					if err != nil || string(b) != "timezone bytes\x00\xff\n" {
						t.Fatal(name, err, b)
					}
				}
				linkname, err := os.Readlink(filepath.Join(dst, "share/timezone/Local"))
				if err != nil || linkname != "Etc/UTC" {
					t.Fatal(err, linkname)
				}
				files, err := inventory(dst)
				if err != nil {
					t.Fatal(err)
				}
				if files["share/timezone/UTC"] != files["share/timezone/Etc/UTC"] || files["share/timezone/UTC"] != files["share/timezone/Universal"] {
					t.Fatal(files)
				}
				if want == nil {
					want = files
				} else if !reflect.DeepEqual(want, files) {
					t.Fatal("link and byte-copy inventories differ", want, files)
				}
			}
		})
	}
}

func TestArchiveHardLinkRejections(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		strip        int
		prefix       []archiveEntry
	}{
		{"outside-relative", "../outside", 0, nil},
		{"outside-absolute", "/outside", 0, nil},
		{"forward-reference", "future", 0, nil},
		{"directory", "dir", 0, []archiveEntry{{tar.Header{Name: "dir", Typeflag: tar.TypeDir}, ""}}},
		{"symlink", "alias", 0, []archiveEntry{{tar.Header{Name: "file", Typeflag: tar.TypeReg}, "ok"}, {tar.Header{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "file"}, ""}}},
		{"symlink-parent", "alias/file", 0, []archiveEntry{{tar.Header{Name: "dir/file", Typeflag: tar.TypeReg}, "ok"}, {tar.Header{Name: "alias", Typeflag: tar.TypeSymlink, Linkname: "dir"}, ""}}},
		{"strip-wrong-root", "other/file", 1, []archiveEntry{{tar.Header{Name: "root/file", Typeflag: tar.TypeReg}, "ok"}}},
		{"strip-missing-root", "file", 1, []archiveEntry{{tar.Header{Name: "root/file", Typeflag: tar.TypeReg}, "ok"}}},
		{"strip-outside", "root/../../outside", 1, []archiveEntry{{tar.Header{Name: "root/file", Typeflag: tar.TypeReg}, "ok"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "hard"
			if tc.strip == 1 {
				name = "root/hard"
			}
			entries := append(tc.prefix, archiveEntry{tar.Header{Name: name, Typeflag: tar.TypeLink, Linkname: tc.target}, ""})
			// Materializing the named file later must not make a forward link valid.
			if tc.name == "forward-reference" {
				entries = append(entries, archiveEntry{tar.Header{Name: "future", Typeflag: tar.TypeReg}, "later"})
			}
			if err := unpackArchive(writeArchiveEntries(t, entries...), t.TempDir(), tc.strip); err == nil {
				t.Fatal("unsafe hard link accepted")
			}
		})
	}
	t.Run("preexisting-not-from-archive", func(t *testing.T) {
		dst := t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, "existing"), []byte("existing"), 0600); err != nil {
			t.Fatal(err)
		}
		archive := writeArchiveEntries(t, archiveEntry{tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "existing"}, ""})
		if err := unpackArchive(archive, dst, 0); err == nil {
			t.Fatal("existing file accepted as archive member")
		}
	})
	t.Run("filesystem-symlink-parent", func(t *testing.T) {
		dst, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(dst, "dir")); err != nil {
			t.Fatal(err)
		}
		archive := writeArchiveEntries(t, archiveEntry{tar.Header{Name: "dir/file", Typeflag: tar.TypeReg}, "bad"}, archiveEntry{tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "dir/file"}, ""})
		if err := unpackArchive(archive, dst, 0); err == nil {
			t.Fatal("write through existing symlink accepted")
		}
		if files, _ := os.ReadDir(outside); len(files) != 0 {
			t.Fatal("outside tree modified")
		}
	})
	t.Run("duplicate-cannot-overwrite", func(t *testing.T) {
		entries := []archiveEntry{{tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600}, "original"}, {tar.Header{Name: "hard", Typeflag: tar.TypeReg, Mode: 0600}, "preserved"}, {tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "file"}, ""}}
		dst := t.TempDir()
		if err := unpackArchive(writeArchiveEntries(t, entries...), dst, 0); err == nil {
			t.Fatal("duplicate accepted")
		}
		b, err := os.ReadFile(filepath.Join(dst, "hard"))
		if err != nil || string(b) != "preserved" {
			t.Fatal("fallback overwrote existing file", err, string(b))
		}
	})
}

func TestArchiveExtendedHeadersAndLongLinks(t *testing.T) {
	for _, format := range []tar.Format{tar.FormatPAX, tar.FormatGNU} {
		t.Run(format.String(), func(t *testing.T) {
			name := "root/share/" + strings.Repeat("long-name-", 16)
			entries := []archiveEntry{
				{tar.Header{Name: "GlobalHead", Typeflag: tar.TypeXGlobalHeader, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "global metadata"}}, ""},
				{tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0644, Format: format, PAXRecords: map[string]string{}}, "long fixture"},
				{tar.Header{Name: "root/share/hard", Typeflag: tar.TypeLink, Linkname: name, Format: format}, ""},
				{tar.Header{Name: "root/share/symbolic", Typeflag: tar.TypeSymlink, Linkname: strings.TrimPrefix(name, "root/share/"), Format: format}, ""},
			}
			archive := writeArchiveEntries(t, entries...)
			dst := t.TempDir()
			if err := unpackArchive(archive, dst, 1); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"share/hard", "share/symbolic", strings.TrimPrefix(name, "root/")} {
				b, err := os.ReadFile(filepath.Join(dst, path))
				if err != nil || string(b) != "long fixture" {
					t.Fatal(path, err, string(b))
				}
			}
			if _, err := os.Stat(filepath.Join(dst, "GlobalHead")); !os.IsNotExist(err) {
				t.Fatal("global header became file")
			}
			// The long-name decoder must not bypass our post-decoding checks.
			bad := writeArchiveEntries(t, archiveEntry{tar.Header{Name: "../" + strings.Repeat("escape", 30), Typeflag: tar.TypeReg, Format: format}, "bad"})
			if err := unpackArchive(bad, t.TempDir(), 0); err == nil {
				t.Fatal("decoded long traversal accepted")
			}
		})
	}
}

func TestArchiveHardLinkInventoryComparable(t *testing.T) {
	archive := writeArchiveEntries(t, linkedArchiveEntries("postgres-lite/")...)
	var first map[string]string
	for i := 0; i < 2; i++ {
		dir, _ := sandbox.Real(t.TempDir())
		if err := unpackArchive(archive, dir, 1); err != nil {
			t.Fatal(err)
		}
		files, err := inventory(dir)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = files
		} else if !reflect.DeepEqual(first, files) {
			t.Fatal("inventory is not deterministic")
		}
		if err = os.WriteFile(filepath.Join(dir, "share/timezone/Etc/UTC"), []byte("tampered"), 0600); err != nil {
			t.Fatal(err)
		}
		if got, e := inventory(dir); e != nil || reflect.DeepEqual(got, files) {
			t.Fatal("hard-link tampering not detected")
		}
	}
}
