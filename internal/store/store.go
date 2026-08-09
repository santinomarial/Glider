// Package store defines the control-plane persistence operations whose atomic
// boundaries are part of Glider's correctness contract.
package store

import (
	"context"
	"errors"

	"github.com/santinomarial/glider/internal/api"
)

var (
	ErrConflict             = errors.New("store revision conflict")
	ErrNotFound             = errors.New("resource not found")
	ErrAlreadyAssigned      = errors.New("task already assigned")
	ErrInsufficientCapacity = errors.New("node reservation no longer fits")
	ErrQuotaExceeded        = errors.New("cluster quota exceeded")
	ErrNodeActive           = errors.New("node is active or not safely drained")
)

type BindRequest struct {
	TaskID       string
	TaskRevision int64
	NodeID       string
	NodeRevision int64
}
type ControlPlane interface {
	GetTask(context.Context, string) (api.Task, error)
	ListNodes(context.Context) ([]api.Node, error)
	ListAssignments(context.Context) ([]api.Assignment, error)
	Bind(context.Context, BindRequest) (api.Assignment, error)
}
