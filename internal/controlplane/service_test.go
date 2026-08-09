package controlplane

import (
	"testing"

	"github.com/santinomarial/glider/internal/transport"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestNodeIdentityCannotImpersonatePeer(t *testing.T) {
	node := transport.Principal{Name: "node-a", Roles: map[string]bool{"node": true}}
	if !nodeCanMutate(node, "node-a") {
		t.Fatal("node denied itself")
	}
	if nodeCanMutate(node, "node-b") {
		t.Fatal("node allowed to impersonate peer")
	}
	admin := transport.Principal{Name: "admin", Roles: map[string]bool{"admin": true}}
	if !nodeCanMutate(admin, "node-b") {
		t.Fatal("non-node administrative principal denied")
	}
}

func TestRequiredRevisionRejectsMissingFractionalAndNonPositive(t *testing.T) {
	for _, value := range []any{nil, 0.0, -1.0, 1.5, "1"} {
		request, _ := structpb.NewStruct(map[string]any{"revision": value})
		if _, err := requiredRevision(request, "revision"); err == nil {
			t.Fatalf("accepted revision %#v", value)
		}
	}
	request, _ := structpb.NewStruct(map[string]any{"revision": float64(42)})
	if got, err := requiredRevision(request, "revision"); err != nil || got != 42 {
		t.Fatalf("revision = %d, %v", got, err)
	}
}
