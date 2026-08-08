package lease

import (
	"context"
	"time"
	clientv3 "go.etcd.io/etcd/client/v3"
	"github.com/santinomarial/glider/internal/api"
)
type NodeStore interface{ListNodes(context.Context)([]api.Node,error);PutNode(context.Context,api.Node,int64)(api.Node,error)}
type Monitor struct{client *clientv3.Client;cluster string;store NodeStore;grace,period time.Duration}
func NewMonitor(client *clientv3.Client,cluster string,store NodeStore,grace,period time.Duration)*Monitor{if grace<=0{grace=20*time.Second};if period<=0{period=2*time.Second};return &Monitor{client:client,cluster:cluster,store:store,grace:grace,period:period}}
func(m *Monitor)Run(ctx context.Context)error{ticker:=time.NewTicker(m.period);defer ticker.Stop();for{nodes,err:=m.store.ListNodes(ctx);if err==nil{now:=time.Now().UTC();for _,node:=range nodes{alive,checkErr:=NodeAlive(ctx,m.client,m.cluster,node.Metadata.ID);if checkErr!=nil{continue};changed:=false;if alive&&(node.Status.Phase==api.NodeJoining||node.Status.Phase==api.NodeSuspect||node.Status.Phase==api.NodeUnreachable){node.Status.Phase=api.NodeReady;changed=true}else if !alive&&node.Status.Phase==api.NodeReady{node.Status.Phase=api.NodeSuspect;changed=true}else if !alive&&node.Status.Phase==api.NodeSuspect&&now.Sub(node.Status.UpdatedAt)>=m.grace{node.Status.Phase=api.NodeUnreachable;changed=true};if changed{node.Status.UpdatedAt=now;_,_=m.store.PutNode(ctx,node,node.Metadata.Revision)}}};select{case<-ctx.Done():return ctx.Err();case<-ticker.C:}}}
