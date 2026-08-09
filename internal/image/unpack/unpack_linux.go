//go:build linux

// Package unpack extracts verified OCI layer blobs into immutable layer
// directories. Archive paths are hostile input: every filesystem operation is
// constrained beneath the destination and refuses symlink traversal.
package unpack

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ErrUnsafeArchive = errors.New("unsafe OCI layer archive")
	ErrResourceLimit = errors.New("OCI layer extraction limit exceeded")
)

const (
	defaultMaxFiles = 1_000_000
	defaultMaxBytes = int64(64 << 30)
	maxPathBytes    = 4096
)

type Limits struct {
	MaxFiles int
	MaxBytes int64
}

type Unpacker struct {
	root   string
	limits Limits
}

type GCResult struct {
	Removed int
	Bytes   int64
}

func New(root string, limits Limits) (*Unpacker, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("layer root must be absolute")
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaultMaxFiles
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = defaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Join(root, "sha256"), 0o755); err != nil {
		return nil, fmt.Errorf("create layer store: %w", err)
	}
	return &Unpacker{root: root, limits: limits}, nil
}

// Collect removes immutable unpacked layers not referenced by an active
// snapshot and older than grace. The manager serializes this with Prepare.
func (u *Unpacker) Collect(ctx context.Context, keep map[string]struct{}, grace time.Duration) (GCResult, error) {
	var result GCResult
	dir := filepath.Join(u.root, "sha256")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, fmt.Errorf("list unpacked layers: %w", err)
	}
	cutoff := time.Now().Add(-grace)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path := filepath.Join(dir, entry.Name())
		if _, retained := keep[path]; retained {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return result, err
		}
		if !info.IsDir() || info.ModTime().After(cutoff) {
			continue
		}
		bytes, err := treeBytes(path)
		if err != nil {
			return result, fmt.Errorf("measure layer %s: %w", entry.Name(), err)
		}
		if err := os.RemoveAll(path); err != nil {
			return result, fmt.Errorf("remove layer %s: %w", entry.Name(), err)
		}
		result.Removed++
		result.Bytes += bytes
	}
	return result, nil
}

func treeBytes(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Unpack atomically publishes one immutable layer directory. blob must already
// have been verified against desc by the content store.
func (u *Unpacker) Unpack(ctx context.Context, desc v1.Descriptor, blob string) (string, error) {
	if desc.Digest.Algorithm() != digest.SHA256 || desc.Digest.Validate() != nil {
		return "", fmt.Errorf("invalid layer digest %q", desc.Digest)
	}
	final := filepath.Join(u.root, "sha256", desc.Digest.Encoded())
	if info, err := os.Stat(final); err == nil && info.IsDir() {
		return final, nil
	} else if err == nil {
		return "", fmt.Errorf("layer path exists and is not a directory: %s", final)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(final), ".unpack-")
	if err != nil {
		return "", fmt.Errorf("create temporary layer: %w", err)
	}
	defer os.RemoveAll(tmp)

	f, err := os.Open(blob)
	if err != nil {
		return "", fmt.Errorf("open layer blob: %w", err)
	}
	defer f.Close()
	reader, closeReader, err := layerReader(desc.MediaType, f)
	if err != nil {
		return "", err
	}
	if closeReader != nil {
		defer closeReader()
	}
	if err := u.extract(ctx, tar.NewReader(reader), tmp); err != nil {
		return "", err
	}
	if err := syncTree(tmp); err != nil {
		return "", err
	}
	if err := makeReadOnly(tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		if info, statErr := os.Stat(final); statErr == nil && info.IsDir() {
			return final, nil
		}
		return "", fmt.Errorf("publish unpacked layer: %w", err)
	}
	if d, err := os.Open(filepath.Dir(final)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return final, nil
}

func layerReader(mediaType string, r io.Reader) (io.Reader, func() error, error) {
	switch mediaType {
	case v1.MediaTypeImageLayerGzip, "application/vnd.docker.image.rootfs.diff.tar.gzip":
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("open gzip layer: %w", err)
		}
		return gz, gz.Close, nil
	case v1.MediaTypeImageLayer, "application/vnd.docker.image.rootfs.diff.tar":
		return r, nil, nil
	default:
		return nil, nil, fmt.Errorf("unsupported layer media type %q", mediaType)
	}
}

func (u *Unpacker) extract(ctx context.Context, tr *tar.Reader, root string) error {
	files := 0
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read layer tar: %w", err)
		}
		files++
		if files > u.limits.MaxFiles {
			return fmt.Errorf("%w: more than %d entries", ErrResourceLimit, u.limits.MaxFiles)
		}
		if h.Size < 0 || total > u.limits.MaxBytes-h.Size {
			return fmt.Errorf("%w: expanded bytes exceed %d", ErrResourceLimit, u.limits.MaxBytes)
		}
		total += h.Size
		clean, err := archivePath(h.Name)
		if err != nil {
			return err
		}
		if clean == "." {
			continue
		}
		parent := filepath.Dir(filepath.Join(root, filepath.FromSlash(clean)))
		if err := ensureParents(root, parent); err != nil {
			return err
		}

		base := path.Base(clean)
		if base == ".wh..wh..opq" {
			if err := syscall.Setxattr(parent, "trusted.overlay.opaque", []byte("y"), 0); err != nil {
				return fmt.Errorf("set opaque-directory marker for %s: %w", clean, err)
			}
			continue
		}
		if strings.HasPrefix(base, ".wh.") {
			target := filepath.Join(parent, strings.TrimPrefix(base, ".wh."))
			if err := removeExisting(target); err != nil {
				return err
			}
			if err := syscall.Mknod(target, syscall.S_IFCHR|0o600, 0); err != nil {
				return fmt.Errorf("create whiteout %s: %w", clean, err)
			}
			continue
		}

		target := filepath.Join(root, filepath.FromSlash(clean))
		if err := extractEntry(tr, h, root, target); err != nil {
			return fmt.Errorf("extract %s: %w", clean, err)
		}
	}
}

func archivePath(name string) (string, error) {
	if name == "" || len(name) > maxPathBytes || strings.ContainsRune(name, '\x00') || path.IsAbs(name) {
		return "", fmt.Errorf("%w: invalid path %q", ErrUnsafeArchive, name)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: path escapes root: %q", ErrUnsafeArchive, name)
	}
	return clean, nil
}

func ensureParents(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: parent escapes root", ErrUnsafeArchive)
	}
	cur := root
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		cur = filepath.Join(cur, component)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: parent %s is not a directory", ErrUnsafeArchive, cur)
		}
	}
	return nil
}

func extractEntry(tr *tar.Reader, h *tar.Header, root, target string) error {
	mode := os.FileMode(h.Mode) & os.ModePerm
	if h.Typeflag == tar.TypeDir {
		info, err := os.Lstat(target)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%w: directory replaces non-directory", ErrUnsafeArchive)
			}
			return os.Chmod(target, mode)
		}
		if !os.IsNotExist(err) {
			return err
		}
	} else if err := removeExisting(target); err != nil {
		return err
	}
	switch h.Typeflag {
	case tar.TypeDir:
		if err := os.Mkdir(target, mode); err != nil && !os.IsExist(err) {
			return err
		}
	case tar.TypeReg, tar.TypeRegA:
		f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		written, copyErr := io.CopyN(f, tr, h.Size)
		closeErr := f.Close()
		if copyErr != nil || written != h.Size {
			return fmt.Errorf("truncated regular file: %w", copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
	case tar.TypeSymlink:
		if strings.ContainsRune(h.Linkname, '\x00') {
			return fmt.Errorf("%w: invalid symlink target", ErrUnsafeArchive)
		}
		if err := os.Symlink(h.Linkname, target); err != nil {
			return err
		}
	case tar.TypeLink:
		link, err := archivePath(h.Linkname)
		if err != nil {
			return err
		}
		source := filepath.Join(root, filepath.FromSlash(link))
		if err := ensureParents(root, filepath.Dir(source)); err != nil {
			return fmt.Errorf("%w: unsafe hardlink parent: %v", ErrUnsafeArchive, err)
		}
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: hardlink target must be an existing regular file", ErrUnsafeArchive)
		}
		if err := os.Link(source, target); err != nil {
			return err
		}
	case tar.TypeFifo:
		if err := syscall.Mkfifo(target, uint32(mode)); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported tar entry type %d", ErrUnsafeArchive, h.Typeflag)
	}
	if h.Typeflag != tar.TypeSymlink {
		if err := os.Chown(target, h.Uid, h.Gid); err != nil {
			return err
		}
	} else if err := os.Lchown(target, h.Uid, h.Gid); err != nil {
		return err
	}
	return nil
}

func removeExisting(target string) error {
	if err := os.RemoveAll(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func syncTree(root string) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() || info.IsDir() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			syncErr := f.Sync()
			closeErr := f.Close()
			if syncErr != nil {
				return syncErr
			}
			return closeErr
		}
		return nil
	})
}

func makeReadOnly(root string) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return os.Chmod(p, info.Mode().Perm()&^0o222)
	})
}
