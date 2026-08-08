package scheduler

import(
	"context"
	"testing"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/store/memory"
)

func TestControllerSchedulesAndReserves(t *testing.T){s:=memory.New();task:=s.PutTask(api.Task{Metadata:api.Metadata{ID:"t"},Spec:api.TaskSpec{Resources:api.Resources{CPUMilli:250}},Status:api.TaskStatus{Phase:api.TaskPending}});_ = task;s.PutNode(api.Node{Metadata:api.Metadata{ID:"n"},Spec:api.NodeSpec{Capacity:api.Resources{CPUMilli:1000}},Status:api.NodeStatus{Phase:api.NodeReady}});c,_:=NewController(s);a,err:=c.ScheduleOne(context.Background(),"t");if err!=nil{t.Fatal(err)};if a.NodeID!="n"||a.Generation!=1{t.Fatalf("assignment=%+v",a)};nodes,_:=s.ListNodes(context.Background());if nodes[0].Status.Reserved.CPUMilli!=250{t.Fatalf("reserved=%d",nodes[0].Status.Reserved.CPUMilli)}}
