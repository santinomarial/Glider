package content

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func descriptor(data []byte) v1.Descriptor {
	return v1.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))}
}

func TestPutPublishesVerifiedBlob(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("verified content")
	desc := descriptor(data)
	path, err := store.Put(context.Background(), desc, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("blobs", "sha256", desc.Digest.Encoded()); filepath.ToSlash(path[len(store.root)+1:]) != want {
		t.Fatalf("path = %q, want suffix %q", path, want)
	}
	if err := store.Verify(desc); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestPutRejectsDigestAndSizeMismatchWithoutPublishing(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	desc := descriptor([]byte("expected"))
	if _, err := store.Put(context.Background(), desc, bytes.NewReader([]byte("tampered"))); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	path, _ := store.BlobPath(desc.Digest)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed blob was published: %v", err)
	}

	short := desc
	short.Size++
	if _, err := store.Put(context.Background(), short, bytes.NewReader([]byte("expected"))); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("size mismatch error = %v", err)
	}
}

func TestPutConcurrentSameDigestConverges(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	data := bytes.Repeat([]byte("x"), 128*1024)
	desc := descriptor(data)
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Put(context.Background(), desc, bytes.NewReader(data))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Put: %v", err)
		}
	}
	if err := store.Verify(desc); err != nil {
		t.Fatal(err)
	}
}

func TestPutRepairsCorruptExistingBlob(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	data := []byte("good")
	desc := descriptor(data)
	path, _ := store.BlobPath(desc.Digest)
	if err := os.WriteFile(path, []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), desc, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(desc); err != nil {
		t.Fatal(err)
	}
}
