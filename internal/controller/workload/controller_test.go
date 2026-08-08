package workload

import("context";"testing";"github.com/santinomarial/glider/internal/api")
type fakeStore struct{revision int64;workloads map[string]api.Workload;tasks map[string]api.Task}
func(f *fakeStore)next()int64{f.revision++;return f.revision}
func(f *fakeStore)ListWorkloads(context.Context)([]api.Workload,error){var out []api.Workload;for _,v:=range f.workloads{out=append(out,v)};return out,nil}
func(f *fakeStore)PutWorkload(_ context.Context,w api.Workload,_ int64)(api.Workload,error){w.Metadata.Revision=f.next();f.workloads[w.Metadata.ID]=w;return w,nil}
func(f *fakeStore)ListTasks(context.Context)([]api.Task,error){var out []api.Task;for _,v:=range f.tasks{out=append(out,v)};return out,nil}
func(f *fakeStore)PutTask(_ context.Context,v api.Task,_ int64)(api.Task,error){v.Metadata.Revision=f.next();f.tasks[v.Metadata.ID]=v;return v,nil}
func(f *fakeStore)DeleteTask(_ context.Context,id string,_ int64)error{delete(f.tasks,id);return nil}
func TestReplicaScaleUpAndDownDeterministically(t *testing.T){s:=&fakeStore{workloads:map[string]api.Workload{},tasks:map[string]api.Task{}};w:=api.Workload{Metadata:api.Metadata{ID:"api",Revision:1},Spec:api.WorkloadSpec{Replicas:3,Template:api.TaskSpec{Image:"example/app"}}};s.workloads[w.Metadata.ID]=w;c,_:=New(s);if err:=c.Reconcile(context.Background(),w);err!=nil{t.Fatal(err)};for _,id:=range []string{"api-000000","api-000001","api-000002"}{if _,ok:=s.tasks[id];!ok{t.Fatalf("missing %s",id)}};w=s.workloads["api"];w.Spec.Replicas=1;if err:=c.Reconcile(context.Background(),w);err!=nil{t.Fatal(err)};if len(s.tasks)!=1{t.Fatalf("tasks=%d",len(s.tasks))};if _,ok:=s.tasks["api-000000"];!ok{t.Fatal("scale down did not retain oldest replica")}}
