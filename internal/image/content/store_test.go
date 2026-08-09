package content

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

func descriptor(data []byte) v1.Descriptor {
	return v1.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))}
}

func TestCollectRetainsReferencedAndRecentBlobs(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	old := descriptor([]byte("old"))
	kept := descriptor([]byte("kept"))
	recent := descriptor([]byte("recent"))
	for _, item := range []struct {
		desc v1.Descriptor
		data string
	}{{old, "old"}, {kept, "kept"}, {recent, "recent"}} {
		if _, err := store.Put(context.Background(), item.desc, bytes.NewBufferString(item.data)); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	for _, desc := range []v1.Descriptor{old, kept} {
		path, _ := store.BlobPath(desc.Digest)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Collect(context.Background(), map[digest.Digest]struct{}{kept.Digest: {}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || result.Bytes != old.Size {
		t.Fatalf("result = %+v", result)
	}
	oldPath, _ := store.BlobPath(old.Digest)
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old blob still exists: %v", err)
	}
	for _, desc := range []v1.Descriptor{kept, recent} {
		if err := store.Verify(desc); err != nil {
			t.Fatalf("retained blob %s: %v", desc.Digest, err)
		}
	}
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
