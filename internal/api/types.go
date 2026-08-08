// Package api contains Glider's versioned control-plane resource model. These
// types are internal representations; api/proto is the eventual wire contract.
package api

import "time"

const Version = "glider.dev/v1"

type Metadata struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Revision   int64  `json:"revision,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}
type Resources struct {
	CPUMilli    int64 `json:"cpu_milli"`
	MemoryBytes int64 `json:"memory_bytes"`
}

func (r Resources) Add(o Resources) Resources {
	return Resources{r.CPUMilli + o.CPUMilli, r.MemoryBytes + o.MemoryBytes}
}
func (r Resources) Sub(o Resources) Resources {
	return Resources{r.CPUMilli - o.CPUMilli, r.MemoryBytes - o.MemoryBytes}
}
func (r Resources) Fits(req Resources) bool {
	return req.CPUMilli >= 0 && req.MemoryBytes >= 0 && r.CPUMilli >= req.CPUMilli && r.MemoryBytes >= req.MemoryBytes
}

type NodePhase string

const (
	NodeJoining     NodePhase = "JOINING"
	NodeReady       NodePhase = "READY"
	NodeSuspect     NodePhase = "SUSPECT"
	NodeUnreachable NodePhase = "UNREACHABLE"
	NodeDraining    NodePhase = "DRAINING"
	NodeRemoved     NodePhase = "REMOVED"
)

type Node struct {
	APIVersion string     `json:"apiVersion"`
	Metadata   Metadata   `json:"metadata"`
	Spec       NodeSpec   `json:"spec"`
	Status     NodeStatus `json:"status"`
}
type NodeSpec struct {
	Unschedulable  bool              `json:"unschedulable"`
	Labels         map[string]string `json:"labels,omitempty"`
	Capacity       Resources         `json:"capacity"`
	SystemReserved Resources         `json:"system_reserved"`
}
type NodeStatus struct {
	Phase         NodePhase `json:"phase"`
	Reserved      Resources `json:"reserved"`
	ObservedUsage Resources `json:"observed_usage"`
	Images        []string  `json:"images,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (n Node) Allocatable() Resources { return n.Spec.Capacity.Sub(n.Spec.SystemReserved) }
func (n Node) Available() Resources   { return n.Allocatable().Sub(n.Status.Reserved) }

type TaskPhase string

const (
	TaskPending     TaskPhase = "PENDING"
	TaskScheduled   TaskPhase = "SCHEDULED"
	TaskRunning     TaskPhase = "RUNNING"
	TaskTerminating TaskPhase = "TERMINATING"
	TaskTerminated  TaskPhase = "TERMINATED"
)

type Task struct {
	APIVersion string     `json:"apiVersion"`
	Metadata   Metadata   `json:"metadata"`
	Spec       TaskSpec   `json:"spec"`
	Status     TaskStatus `json:"status"`
}
type TaskSpec struct {
	WorkloadID   string            `json:"workload_id"`
	Image        string            `json:"image"`
	Command      []string          `json:"command,omitempty"`
	Resources    Resources         `json:"resources"`
	NodeSelector map[string]string `json:"node_selector,omitempty"`
	HostPorts    []uint16          `json:"host_ports,omitempty"`
}
type TaskStatus struct {
	Phase                TaskPhase `json:"phase"`
	AssignmentGeneration int64     `json:"assignment_generation,omitempty"`
	NodeID               string    `json:"node_id,omitempty"`
}

type Assignment struct {
	APIVersion string    `json:"apiVersion"`
	Metadata   Metadata  `json:"metadata"`
	TaskID     string    `json:"task_id"`
	WorkloadID string    `json:"workload_id"`
	NodeID     string    `json:"node_id"`
	Generation int64     `json:"generation"`
	Resources  Resources `json:"resources"`
	Image      string    `json:"image"`
	Command    []string  `json:"command,omitempty"`
	HostPorts  []uint16  `json:"host_ports,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
