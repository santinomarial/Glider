//go:build linux

package unpack

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestUnpackRegularDirectorySymlinkAndHardlink(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	data := tarBytes(t,
		entry{"etc", tar.TypeDir, nil, "", uid, gid},
		entry{"etc/config", tar.TypeReg, []byte("value"), "", uid, gid},
		entry{"config-link", tar.TypeSymlink, nil, "etc/config", uid, gid},
		entry{"config-hard", tar.TypeLink, nil, "etc/config", uid, gid},
	)
	blob := filepath.Join(t.TempDir(), "layer.tar")
	if err := os.WriteFile(blob, data, 0o600); err != nil { t.Fatal(err) }
	u, err := New(t.TempDir(), Limits{})
	if err != nil { t.Fatal(err) }
	desc := v1.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data)), MediaType: v1.MediaTypeImageLayer}
	root, err := u.Unpack(context.Background(), desc, blob)
	if err != nil { t.Fatal(err) }
	got, err := os.ReadFile(filepath.Join(root, "etc", "config"))
	if err != nil || string(got) != "value" { t.Fatalf("file = %q, %v", got, err) }
	if target, err := os.Readlink(filepath.Join(root, "config-link")); err != nil || target != "etc/config" { t.Fatalf("symlink = %q, %v", target, err) }
	base, _ := os.Stat(filepath.Join(root, "etc", "config")); hard, _ := os.Stat(filepath.Join(root, "config-hard"))
	if !os.SameFile(base, hard) { t.Fatal("hardlink did not share inode") }
}

func TestUnpackRejectsTraversal(t *testing.T) {
	assertUnsafeLayer(t, tarBytes(t, entry{"../../escape", tar.TypeReg, []byte("bad"), "", os.Getuid(), os.Getgid()}))
}

func TestUnpackRejectsSymlinkParentEscape(t *testing.T) {
	assertUnsafeLayer(t, tarBytes(t,
		entry{"escape", tar.TypeSymlink, nil, "../../outside", os.Getuid(), os.Getgid()},
		entry{"escape/pwn", tar.TypeReg, []byte("bad"), "", os.Getuid(), os.Getgid()},
	))
}

func TestUnpackEnforcesExpandedSizeLimit(t *testing.T) {
	data := tarBytes(t, entry{"large", tar.TypeReg, []byte("12345"), "", os.Getuid(), os.Getgid()})
	blob := filepath.Join(t.TempDir(), "layer.tar"); _ = os.WriteFile(blob, data, 0o600)
	u, _ := New(t.TempDir(), Limits{MaxBytes: 4})
	desc := v1.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data)), MediaType: v1.MediaTypeImageLayer}
	if _, err := u.Unpack(context.Background(), desc, blob); !errors.Is(err, ErrResourceLimit) { t.Fatalf("error = %v, want ErrResourceLimit", err) }
}

func assertUnsafeLayer(t *testing.T, data []byte) {
	t.Helper()
	blob := filepath.Join(t.TempDir(), "layer.tar"); _ = os.WriteFile(blob, data, 0o600)
	u, _ := New(t.TempDir(), Limits{})
	desc := v1.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data)), MediaType: v1.MediaTypeImageLayer}
	if _, err := u.Unpack(context.Background(), desc, blob); !errors.Is(err, ErrUnsafeArchive) { t.Fatalf("error = %v, want ErrUnsafeArchive", err) }
}

type entry struct { name string; kind byte; data []byte; link string; uid, gid int }
func tarBytes(t *testing.T, entries ...entry) []byte {
	t.Helper(); var buf bytes.Buffer; tw := tar.NewWriter(&buf)
	for _, e := range entries { h := &tar.Header{Name: e.name, Typeflag: e.kind, Size: int64(len(e.data)), Mode: 0o755, Linkname: e.link, Uid: e.uid, Gid: e.gid}; if e.kind == tar.TypeReg { h.Mode = 0o644 }; if err := tw.WriteHeader(h); err != nil { t.Fatal(err) }; if len(e.data) > 0 { if _, err := tw.Write(e.data); err != nil { t.Fatal(err) } } }
	if err := tw.Close(); err != nil { t.Fatal(err) }; return buf.Bytes()
}
