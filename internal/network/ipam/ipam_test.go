package ipam

import (
	"errors"
	"sync"
	"testing"
)

func TestEnsureIsIdempotentAndReleaseReusable(t *testing.T) {
	p, err := Open(t.TempDir(), "10.64.1.0/29")
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Ensure("one")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Ensure("one")
	if a.Address != b.Address {
		t.Fatalf("address changed: %s -> %s", a.Address, b.Address)
	}
	if a.Address.String() != "10.64.1.2" {
		t.Fatalf("first=%s", a.Address)
	}
	if err := p.Release("one"); err != nil {
		t.Fatal(err)
	}
	c, _ := p.Ensure("two")
	if c.Address != a.Address {
		t.Fatalf("released address not reused: %s", c.Address)
	}
}

func TestConcurrentEnsureAllocatesUniqueAddresses(t *testing.T) {
	p, _ := Open(t.TempDir(), "10.64.2.0/24")
	const n = 64
	var wg sync.WaitGroup
	addresses := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := p.Ensure(string(rune('a' + i)))
			if err != nil {
				errs <- err
				return
			}
			addresses <- a.Address.String()
		}(i)
	}
	wg.Wait()
	close(addresses)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[string]bool{}
	for addr := range addresses {
		if seen[addr] {
			t.Fatalf("duplicate %s", addr)
		}
		seen[addr] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d allocations", len(seen))
	}
}

func TestExhaustion(t *testing.T) {
	p, _ := Open(t.TempDir(), "192.0.2.0/30")
	if _, err := p.Ensure("only"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ensure("extra"); !errors.Is(err, ErrExhausted) {
		t.Fatalf("error=%v", err)
	}
}
func TestRejectsUnsafeOwner(t *testing.T) {
	p, _ := Open(t.TempDir(), "192.0.2.0/24")
	if _, err := p.Ensure("../bad"); !errors.Is(err, ErrInvalidOwner) {
		t.Fatalf("error=%v", err)
	}
}
