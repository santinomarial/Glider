package controlplane

import (
	"github.com/santinomarial/glider/internal/transport"
	"testing"
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
