// Package api contains Glider's versioned control-plane resource model. These
// types are internal representations; api/proto is the eventual wire contract.
package api

import "time"

const Version = "glider.dev/v1"

type Metadata struct {
	ID                string     `json:"id"`
	Name              string     `json:"name,omitempty"`
	Revision          int64      `json:"revision,omitempty"`
	Generation        int64      `json:"generation,omitempty"`
	DeletionTimestamp *time.Time `json:"deletion_timestamp,omitempty"`
	IdempotencyKey    string     `json:"idempotency_key,omitempty"`
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
	Unschedulable     bool              `json:"unschedulable"`
	Labels            map[string]string `json:"labels,omitempty"`
	Capacity          Resources         `json:"capacity"`
	SystemReserved    Resources         `json:"system_reserved"`
	PodCIDR           string            `json:"pod_cidr,omitempty"`
	TunnelAddress     string            `json:"tunnel_address,omitempty"`
	OperationsAddress string            `json:"operations_address,omitempty"`
}

type RestartPolicy string

const (
	RestartNever     RestartPolicy = "Never"
	RestartOnFailure RestartPolicy = "OnFailure"
	RestartAlways    RestartPolicy = "Always"
)

type ProbeKind string

const (
	ProbeExec ProbeKind = "exec"
	ProbeHTTP ProbeKind = "http"
	ProbeTCP  ProbeKind = "tcp"
)

type Probe struct {
	Kind             ProbeKind     `json:"kind"`
	Command          []string      `json:"command,omitempty"`
	URL              string        `json:"url,omitempty"`
	Address          string        `json:"address,omitempty"`
	InitialDelay     time.Duration `json:"initial_delay,omitempty"`
	Period           time.Duration `json:"period,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
	FailureThreshold int           `json:"failure_threshold,omitempty"`
	SuccessThreshold int           `json:"success_threshold,omitempty"`
}
type HealthSpec struct {
	Startup   *Probe `json:"startup,omitempty"`
	Liveness  *Probe `json:"liveness,omitempty"`
	Readiness *Probe `json:"readiness,omitempty"`
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
	WorkloadID       string            `json:"workload_id"`
	Image            string            `json:"image"`
	Command          []string          `json:"command,omitempty"`
	Resources        Resources         `json:"resources"`
	NodeSelector     map[string]string `json:"node_selector,omitempty"`
	Labels           map[string]string `json:"labels,omitempty"`
	TemplateRevision string            `json:"template_revision,omitempty"`
	HostPorts        []uint16          `json:"host_ports,omitempty"`
	RestartPolicy    RestartPolicy     `json:"restart_policy,omitempty"`
	Health           HealthSpec        `json:"health,omitempty"`
	Secrets          []SecretEnvRef    `json:"secrets,omitempty"`
}
type SecretEnvRef struct {
	SecretID string `json:"secret_id"`
	Key      string `json:"key"`
	Env      string `json:"env"`
}

type Workload struct {
	APIVersion string         `json:"apiVersion"`
	Metadata   Metadata       `json:"metadata"`
	Spec       WorkloadSpec   `json:"spec"`
	Status     WorkloadStatus `json:"status"`
}
type WorkloadSpec struct {
	Replicas int             `json:"replicas"`
	Template TaskSpec        `json:"template"`
	Rollout  RolloutStrategy `json:"rollout"`
}
type RolloutStrategy struct {
	MaxSurge         int           `json:"max_surge"`
	MaxUnavailable   int           `json:"max_unavailable"`
	ProgressDeadline time.Duration `json:"progress_deadline,omitempty"`
}
type WorkloadStatus struct {
	ObservedGeneration int64     `json:"observed_generation"`
	DesiredReplicas    int       `json:"desired_replicas"`
	CurrentReplicas    int       `json:"current_replicas"`
	ReadyReplicas      int       `json:"ready_replicas"`
	UpdatedReplicas    int       `json:"updated_replicas"`
	CurrentRevision    string    `json:"current_revision,omitempty"`
	UpdateRevision     string    `json:"update_revision,omitempty"`
	RolloutStartedAt   time.Time `json:"rollout_started_at,omitempty"`
	LastProgressAt     time.Time `json:"last_progress_at,omitempty"`
	RolloutPhase       string    `json:"rollout_phase,omitempty"`
	RolloutMessage     string    `json:"rollout_message,omitempty"`
}
type TaskStatus struct {
	Phase                TaskPhase `json:"phase"`
	AssignmentGeneration int64     `json:"assignment_generation,omitempty"`
	NodeID               string    `json:"node_id,omitempty"`
	Address              string    `json:"address,omitempty"`
	Ready                bool      `json:"ready"`
	RestartCount         int       `json:"restart_count,omitempty"`
	LastHealthTransition time.Time `json:"last_health_transition,omitempty"`
}

type Service struct {
	APIVersion string        `json:"apiVersion"`
	Metadata   Metadata      `json:"metadata"`
	Spec       ServiceSpec   `json:"spec"`
	Status     ServiceStatus `json:"status"`
}
type ServiceSpec struct {
	Selector   map[string]string `json:"selector"`
	Port       uint16            `json:"port"`
	TargetPort uint16            `json:"target_port"`
}
type ServiceEndpoint struct {
	TaskID     string `json:"task_id"`
	NodeID     string `json:"node_id"`
	Address    string `json:"address"`
	Port       uint16 `json:"port"`
	Generation int64  `json:"generation"`
}
type ServiceStatus struct {
	Endpoints []ServiceEndpoint `json:"endpoints,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}

type Secret struct {
	APIVersion string            `json:"apiVersion"`
	Metadata   Metadata          `json:"metadata"`
	Data       map[string][]byte `json:"data,omitempty"`
}

type Event struct {
	APIVersion string         `json:"apiVersion"`
	Metadata   Metadata       `json:"metadata"`
	Time       time.Time      `json:"time"`
	Type       string         `json:"type"`
	Reason     string         `json:"reason"`
	Message    string         `json:"message,omitempty"`
	ObjectKind string         `json:"object_kind"`
	ObjectID   string         `json:"object_id"`
	NodeID     string         `json:"node_id,omitempty"`
	Fields     map[string]any `json:"fields,omitempty"`
}

type Assignment struct {
	APIVersion    string         `json:"apiVersion"`
	Metadata      Metadata       `json:"metadata"`
	TaskID        string         `json:"task_id"`
	WorkloadID    string         `json:"workload_id"`
	NodeID        string         `json:"node_id"`
	Generation    int64          `json:"generation"`
	Resources     Resources      `json:"resources"`
	Image         string         `json:"image"`
	Command       []string       `json:"command,omitempty"`
	RestartPolicy RestartPolicy  `json:"restart_policy,omitempty"`
	Health        HealthSpec     `json:"health,omitempty"`
	HostPorts     []uint16       `json:"host_ports,omitempty"`
	Secrets       []SecretEnvRef `json:"secrets,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}
