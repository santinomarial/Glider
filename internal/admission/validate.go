// Package admission validates untrusted API resources before persistence.
package admission

import (
	"errors"
	"fmt"
	"github.com/santinomarial/glider/internal/api"
	"regexp"
	"strings"
	"time"
)

var idPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,61}[a-z0-9])?$`)

func metadata(m api.Metadata) error {
	if !idPattern.MatchString(m.ID) {
		return errors.New("metadata.id must be a lowercase DNS label of at most 63 characters")
	}
	if len(m.Name) > 253 {
		return errors.New("metadata.name exceeds 253 characters")
	}
	return nil
}
func labels(values map[string]string) error {
	if len(values) > 64 {
		return errors.New("labels exceed limit of 64")
	}
	for k, v := range values {
		if !idPattern.MatchString(k) || len(v) > 256 {
			return fmt.Errorf("invalid label %q", k)
		}
	}
	return nil
}
func resources(v api.Resources) error {
	if v.CPUMilli < 0 || v.CPUMilli > 1_000_000 {
		return errors.New("cpu_milli must be between 0 and 1000000")
	}
	if v.MemoryBytes < 0 || v.MemoryBytes > 1<<50 {
		return errors.New("memory_bytes must be between 0 and 1 PiB")
	}
	return nil
}
func taskSpec(v api.TaskSpec) error {
	if strings.TrimSpace(v.Image) == "" || len(v.Image) > 2048 {
		return errors.New("image is required and limited to 2048 characters")
	}
	if len(v.Command) > 256 {
		return errors.New("command exceeds 256 arguments")
	}
	total := 0
	for _, arg := range v.Command {
		total += len(arg)
	}
	if total > 128<<10 {
		return errors.New("command exceeds 128 KiB")
	}
	if err := resources(v.Resources); err != nil {
		return err
	}
	if err := labels(v.Labels); err != nil {
		return err
	}
	if err := labels(v.NodeSelector); err != nil {
		return err
	}
	seen := map[uint16]bool{}
	for _, port := range v.HostPorts {
		if port == 0 || seen[port] {
			return errors.New("host ports must be non-zero and unique")
		}
		seen[port] = true
	}
	for _, probe := range []*api.Probe{v.Health.Startup, v.Health.Liveness, v.Health.Readiness} {
		if probe == nil {
			continue
		}
		if probe.Period < 0 || probe.Timeout < 0 || probe.InitialDelay < 0 || probe.Period > 24*time.Hour || probe.Timeout > time.Hour {
			return errors.New("probe durations are out of bounds")
		}
		if probe.FailureThreshold < 0 || probe.FailureThreshold > 1000 || probe.SuccessThreshold < 0 || probe.SuccessThreshold > 1000 {
			return errors.New("probe thresholds are out of bounds")
		}
	}
	return nil
}
func Task(v api.Task) error {
	if err := metadata(v.Metadata); err != nil {
		return err
	}
	return taskSpec(v.Spec)
}
func Workload(v api.Workload) error {
	if err := metadata(v.Metadata); err != nil {
		return err
	}
	if v.Spec.Replicas < 0 || v.Spec.Replicas > 10_000 {
		return errors.New("replicas must be between 0 and 10000")
	}
	if v.Spec.Rollout.MaxSurge < 0 || v.Spec.Rollout.MaxSurge > 10_000 || v.Spec.Rollout.MaxUnavailable < 0 || v.Spec.Rollout.MaxUnavailable > 10_000 {
		return errors.New("rollout budgets are out of bounds")
	}
	if v.Spec.Rollout.ProgressDeadline < 0 || v.Spec.Rollout.ProgressDeadline > 7*24*time.Hour {
		return errors.New("progress deadline is out of bounds")
	}
	return taskSpec(v.Spec.Template)
}
func Node(v api.Node) error {
	if err := metadata(v.Metadata); err != nil {
		return err
	}
	if err := resources(v.Spec.Capacity); err != nil {
		return err
	}
	if err := resources(v.Spec.SystemReserved); err != nil {
		return err
	}
	if !v.Spec.Capacity.Fits(v.Spec.SystemReserved) {
		return errors.New("system_reserved exceeds capacity")
	}
	return labels(v.Spec.Labels)
}
func Service(v api.Service) error {
	if err := metadata(v.Metadata); err != nil {
		return err
	}
	if len(v.Spec.Selector) == 0 {
		return errors.New("service selector cannot be empty")
	}
	if err := labels(v.Spec.Selector); err != nil {
		return err
	}
	if v.Spec.Port == 0 || v.Spec.TargetPort == 0 {
		return errors.New("service ports must be non-zero")
	}
	return nil
}
func Event(v api.Event) error {
	if err := metadata(v.Metadata); err != nil {
		return err
	}
	if len(v.Type) > 32 || len(v.Reason) > 128 || len(v.Message) > 4096 || len(v.ObjectKind) > 64 || len(v.ObjectID) > 253 || len(v.Fields) > 32 {
		return errors.New("event fields exceed admission limits")
	}
	return nil
}
