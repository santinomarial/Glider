// Package scheduler implements Glider's Phase 12 filter/score placement loop.
// Persistence and CAS binding are separate so the algorithm is deterministic.
package scheduler

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/santinomarial/glider/internal/api"
)

var ErrUnschedulable = errors.New("task is unschedulable")

type Rejection struct{ NodeID, Reason string }
type Decision struct {
	Node     api.Node
	Score    int64
	Rejected []Rejection
}

func Schedule(task api.Task, nodes []api.Node, assigned []api.Assignment) (Decision, error) {
	usedPorts := map[string]map[uint16]bool{}
	replicas := map[string]int{}
	for _, a := range assigned {
		if a.WorkloadID == task.Spec.WorkloadID {
			replicas[a.NodeID]++
		}
		if usedPorts[a.NodeID] == nil {
			usedPorts[a.NodeID] = map[uint16]bool{}
		}
		for _, p := range a.HostPorts {
			usedPorts[a.NodeID][p] = true
		}
	}
	candidates := make([]Decision, 0, len(nodes))
	rejected := make([]Rejection, 0)
	for _, n := range nodes {
		reason := filter(task, n, usedPorts[n.Metadata.ID])
		if reason != "" {
			rejected = append(rejected, Rejection{n.Metadata.ID, reason})
			continue
		}
		candidates = append(candidates, Decision{Node: n, Score: score(task, n, replicas[n.Metadata.ID])})
	}
	if len(candidates) == 0 {
		return Decision{Rejected: rejected}, fmt.Errorf("%w: %d nodes rejected", ErrUnschedulable, len(rejected))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Node.Metadata.ID < candidates[j].Node.Metadata.ID
		}
		return candidates[i].Score > candidates[j].Score
	})
	candidates[0].Rejected = rejected
	return candidates[0], nil
}

func filter(t api.Task, n api.Node, ports map[uint16]bool) string {
	if n.Status.Phase != api.NodeReady {
		return "node not READY"
	}
	if n.Spec.Unschedulable {
		return "node unschedulable"
	}
	for k, v := range t.Spec.NodeSelector {
		if n.Spec.Labels[k] != v {
			return "node selector mismatch"
		}
	}
	if !n.Available().Fits(t.Spec.Resources) {
		return "insufficient requested resources"
	}
	seen := map[uint16]bool{}
	for _, p := range t.Spec.HostPorts {
		if p == 0 || seen[p] || ports[p] {
			return "required host port unavailable"
		}
		seen[p] = true
	}
	return ""
}
func score(t api.Task, n api.Node, replicas int) int64 {
	alloc := n.Allocatable()
	after := n.Available().Sub(t.Spec.Resources)
	cpu, mem := float64(1), float64(1)
	if alloc.CPUMilli > 0 {
		cpu = float64(after.CPUMilli) / float64(alloc.CPUMilli)
	}
	if alloc.MemoryBytes > 0 {
		mem = float64(after.MemoryBytes) / float64(alloc.MemoryBytes)
	}
	balanced := int64(math.Round((cpu + mem - math.Abs(cpu-mem)) * 500))
	return balanced - int64(replicas*100)
}
