package scheduler

import(
	"context"
	"errors"
	"fmt"

	"github.com/santinomarial/glider/internal/api"
	"github.com/santinomarial/glider/internal/store"
)

const maxBindRetries=8
type Controller struct{store store.ControlPlane}
func NewController(s store.ControlPlane)(*Controller,error){if s==nil{return nil,errors.New("scheduler store is required")};return &Controller{store:s},nil}

// ScheduleOne reloads every input after a lost transaction. Selection never
// becomes authority until Store.Bind's CAS succeeds.
func(c *Controller)ScheduleOne(ctx context.Context,taskID string)(api.Assignment,error){var last error;for attempt:=0;attempt<maxBindRetries;attempt++{if err:=ctx.Err();err!=nil{return api.Assignment{},err};task,err:=c.store.GetTask(ctx,taskID);if err!=nil{return api.Assignment{},err};nodes,err:=c.store.ListNodes(ctx);if err!=nil{return api.Assignment{},err};assigned,err:=c.store.ListAssignments(ctx);if err!=nil{return api.Assignment{},err};decision,err:=Schedule(task,nodes,assigned);if err!=nil{return api.Assignment{},err};assignment,err:=c.store.Bind(ctx,store.BindRequest{TaskID:task.Metadata.ID,TaskRevision:task.Metadata.Revision,NodeID:decision.Node.Metadata.ID,NodeRevision:decision.Node.Metadata.Revision});if err==nil{return assignment,nil};if !errors.Is(err,store.ErrConflict){return api.Assignment{},err};last=err};return api.Assignment{},fmt.Errorf("scheduler bind retry budget exhausted: %w",last)}
