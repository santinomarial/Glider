package discovery

import (
	"context"
	"github.com/santinomarial/glider/internal/api"
	"testing"
)

func BenchmarkLookup1000Endpoints(b *testing.B) {
	endpoints := make([]api.ServiceEndpoint, 1000)
	for i := range endpoints {
		endpoints[i] = api.ServiceEndpoint{Address: "10.64.0.2"}
	}
	d, _ := NewDNS(fakeLister{[]api.Service{{Metadata: api.Metadata{ID: "api"}, Status: api.ServiceStatus{Endpoints: endpoints}}}})
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := d.Lookup(ctx, "api.glider"); err != nil {
			b.Fatal(err)
		}
	}
}
