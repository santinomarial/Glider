package discovery
import("context";"net";"testing";"github.com/santinomarial/glider/internal/api")
type fakeLister struct{ services []api.Service }
func(f fakeLister)ListServices(context.Context)([]api.Service,error){return f.services,nil}
func TestLookupUsesServiceNameAndReadySnapshot(t *testing.T){ d,_:=NewDNS(fakeLister{[]api.Service{{Metadata:api.Metadata{ID:"svc-1",Name:"api"},Status:api.ServiceStatus{Endpoints:[]api.ServiceEndpoint{{Address:"10.64.0.2"},{Address:"bad"}}}}}}); ips,err:=d.Lookup(context.Background(),"api.glider.");if err!=nil{t.Fatal(err)};if len(ips)!=1||!ips[0].Equal(net.ParseIP("10.64.0.2")){t.Fatalf("ips=%v",ips)} }
func TestDNSResponseContainsARecord(t *testing.T){ d,_:=NewDNS(fakeLister{[]api.Service{{Metadata:api.Metadata{ID:"api"},Status:api.ServiceStatus{Endpoints:[]api.ServiceEndpoint{{Address:"10.64.0.2"}}}}}}); q:=[]byte{0x12,0x34,1,0,0,1,0,0,0,0,0,0,3,'a','p','i',6,'g','l','i','d','e','r',0,0,1,0,1}; r:=d.response(context.Background(),q);if len(r)<4||r[len(r)-4]!=10||r[len(r)-1]!=2{t.Fatalf("response=%v",r)} }
