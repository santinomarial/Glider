// Package content implements Glider's verified, content-addressed OCI blob
// store. Publication is atomic and per-digest serialized across processes.
package content

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	ErrInvalidDescriptor = errors.New("invalid content descriptor")
	ErrDigestMismatch    = errors.New("content digest mismatch")
	ErrSizeMismatch      = errors.New("content size mismatch")
	ErrCorruptContent    = errors.New("corrupt content")
)

type Store struct{ root string }

func NewStore(root string) (*Store, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("content root must be an absolute path")
	}
	for _, dir := range []string{filepath.Join(root, "blobs", "sha256"), filepath.Join(root, "locks", "sha256")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create content store directory %s: %w", dir, err)
		}
	}
	return &Store{root: root}, nil
}

func validateDescriptor(desc v1.Descriptor) error {
	if desc.Digest == "" || desc.Digest.Algorithm() != digest.SHA256 || desc.Digest.Validate() != nil {
		return fmt.Errorf("%w: require a valid sha256 digest", ErrInvalidDescriptor)
	}
	if desc.Size < 0 {
		return fmt.Errorf("%w: negative size", ErrInvalidDescriptor)
	}
	return nil
}

func (s *Store) BlobPath(d digest.Digest) (string, error) {
	if d.Algorithm() != digest.SHA256 || d.Validate() != nil {
		return "", fmt.Errorf("%w: invalid sha256 digest %q", ErrInvalidDescriptor, d)
	}
	return filepath.Join(s.root, "blobs", "sha256", d.Encoded()), nil
}

// Put consumes exactly desc.Size bytes, verifies the sha256 digest, fsyncs the
// temporary file, and atomically publishes it. A failed write is never visible
// at the final digest path.
func (s *Store) Put(ctx context.Context, desc v1.Descriptor, src io.Reader) (string, error) {
	if err := validateDescriptor(desc); err != nil {
		return "", err
	}
	lock, err := s.lock(ctx, desc.Digest)
	if err != nil {
		return "", err
	}
	defer lock.close()

	path, _ := s.BlobPath(desc.Digest)
	if err := verifyFile(path, desc); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		if removeErr := os.Remove(path); removeErr != nil {
			return "", fmt.Errorf("remove corrupt blob %s: %w", desc.Digest, removeErr)
		}
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".incoming-")
	if err != nil {
		return "", fmt.Errorf("create blob temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(src, desc.Size+1))
	if copyErr != nil {
		tmp.Close()
		return "", fmt.Errorf("write blob %s: %w", desc.Digest, copyErr)
	}
	if written != desc.Size {
		tmp.Close()
		return "", fmt.Errorf("%w: blob %s: got %d, want %d", ErrSizeMismatch, desc.Digest, written, desc.Size)
	}
	got := digest.NewDigestFromBytes(digest.SHA256, h.Sum(nil))
	if got != desc.Digest {
		tmp.Close()
		return "", fmt.Errorf("%w: got %s, want %s", ErrDigestMismatch, got, desc.Digest)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("fsync blob temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close blob temporary file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o444); err != nil {
		return "", fmt.Errorf("make blob immutable to ordinary writers: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("publish blob %s: %w", desc.Digest, err)
	}
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return path, nil
}

// Verify hashes the stored bytes instead of trusting the filename.
func (s *Store) Verify(desc v1.Descriptor) error {
	if err := validateDescriptor(desc); err != nil {
		return err
	}
	path, _ := s.BlobPath(desc.Digest)
	return verifyFile(path, desc)
}

func verifyFile(path string, desc v1.Descriptor) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != desc.Size {
		return fmt.Errorf("%w: blob %s has unexpected type or size", ErrCorruptContent, desc.Digest)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash stored blob: %w", err)
	}
	got := digest.NewDigestFromBytes(digest.SHA256, h.Sum(nil))
	if got != desc.Digest {
		return fmt.Errorf("%w: blob %s hashes to %s", ErrCorruptContent, desc.Digest, got)
	}
	return nil
}

type fileLock struct{ f *os.File }

func (s *Store) lock(ctx context.Context, d digest.Digest) (*fileLock, error) {
	path := filepath.Join(s.root, "locks", "sha256", d.Encoded()+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open content lock: %w", err)
	}
	backoff := time.Millisecond
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &fileLock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("lock content %s: %w", d, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("lock content %s: %w", d, ctx.Err())
		case <-time.After(backoff):
		}
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func (l *fileLock) close() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
