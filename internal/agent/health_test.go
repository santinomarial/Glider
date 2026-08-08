package agent
import("context";"errors";"testing";"time";"github.com/santinomarial/glider/internal/api")
type healthFakeStore struct{assignments []api.Assignment;reported []bool;restarts int}
func(s *healthFakeStore)Snapshot(context.Context,string)([]api.Assignment,error){return s.assignments,nil}
func(s *healthFakeStore)ReportTaskHealth(_ context.Context,_ string,_ int64,ready bool)error{s.reported=append(s.reported,ready);return nil}
func(s *healthFakeStore)RestartTask(context.Context,string,int64)error{s.restarts++;return nil}
type healthFakeChecker struct{fail bool}
func(c healthFakeChecker)CheckProbe(context.Context,api.Assignment,api.Probe)error{if c.fail{return errors.New("unhealthy")};return nil}
func TestHealthDaemonReportsReadinessAndDefersRestart(t *testing.T){readiness:=api.Probe{Kind:api.ProbeTCP,SuccessThreshold:1};a:=api.Assignment{TaskID:"task",Generation:1,RestartPolicy:api.RestartAlways,Health:api.HealthSpec{Readiness:&readiness}};store:=&healthFakeStore{assignments:[]api.Assignment{a}};daemon:=NewHealthDaemon("node",store,healthFakeChecker{},time.Millisecond);daemon.reconcile(context.Background(),store.assignments);if len(store.reported)!=1||!store.reported[0]{t.Fatalf("readiness=%v",store.reported)};liveness:=api.Probe{Kind:api.ProbeTCP,FailureThreshold:1};store.assignments[0].Health=api.HealthSpec{Liveness:&liveness};daemon.checker=healthFakeChecker{fail:true};key:=containerID(a);daemon.next[key]=time.Time{};daemon.reconcile(context.Background(),store.assignments);if store.restarts!=0||!daemon.pending[key]{t.Fatal("restart was not deferred by backoff")};daemon.next[key]=time.Now().Add(-time.Second);daemon.reconcile(context.Background(),store.assignments);if store.restarts!=1{t.Fatalf("restarts=%d",store.restarts)}}
