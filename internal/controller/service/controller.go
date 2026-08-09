// Package service maintains discovery endpoints from Ready tasks.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/netip"
	"reflect"
	"sort"
	"time"

	"github.com/santinomarial/glider/internal/api"
	storeapi "github.com/santinomarial/glider/internal/store"
)

type Store interface {
	ListServices(context.Context) ([]api.Service, error)
	PutService(context.Context, api.Service, int64) (api.Service, error)
	ListTasks(context.Context) ([]api.Task, error)
}
type Controller struct {
	store Store
	now   func() time.Time
}

func New(store Store) (*Controller, error) {
	if store == nil {
		return nil, errors.New("service store is required")
	}
	return &Controller{store: store, now: time.Now}, nil
}
func matches(labels, selector map[string]string) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return len(selector) > 0
}
func (c *Controller) Reconcile(ctx context.Context, service api.Service) error {
	if service.Spec.Port == 0 || service.Spec.TargetPort == 0 {
		return errors.New("service ports must be non-zero")
	}
	services, err := c.store.ListServices(ctx)
	if err != nil {
		return err
	}
	clusterIP, err := allocateClusterIP(service, services)
	if err != nil {
		return err
	}
	tasks, err := c.store.ListTasks(ctx)
	if err != nil {
		return err
	}
	endpoints := make([]api.ServiceEndpoint, 0)
	for _, task := range tasks {
		if !task.Status.Ready || !matches(task.Spec.Labels, service.Spec.Selector) {
			continue
		}
		if _, err := netip.ParseAddr(task.Status.Address); err != nil {
			continue
		}
		endpoints = append(endpoints, api.ServiceEndpoint{TaskID: task.Metadata.ID, NodeID: task.Status.NodeID, Address: task.Status.Address, Port: service.Spec.TargetPort, Generation: task.Status.AssignmentGeneration})
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].TaskID < endpoints[j].TaskID })
	if service.Status.ClusterIP == clusterIP && reflect.DeepEqual(service.Status.Endpoints, endpoints) {
		return nil
	}
	service.Status.ClusterIP = clusterIP
	service.Status.Endpoints = endpoints
	service.Status.UpdatedAt = c.now().UTC()
	_, err = c.store.PutService(ctx, service, service.Metadata.Revision)
	if errors.Is(err, storeapi.ErrConflict) {
		return nil
	}
	return err
}

var servicePrefix = netip.MustParsePrefix("10.96.0.0/16")

func allocateClusterIP(service api.Service, services []api.Service) (string, error) {
	used := map[netip.Addr]string{}
	for _, candidate := range services {
		if candidate.Metadata.ID == service.Metadata.ID || candidate.Status.ClusterIP == "" {
			continue
		}
		address, err := netip.ParseAddr(candidate.Status.ClusterIP)
		if err == nil && servicePrefix.Contains(address) {
			used[address] = candidate.Metadata.ID
		}
	}
	if current, err := netip.ParseAddr(service.Status.ClusterIP); err == nil && servicePrefix.Contains(current) {
		if _, collision := used[current]; !collision {
			return current.String(), nil
		}
	}
	sum := sha256.Sum256([]byte(service.Metadata.ID))
	start := int(binary.BigEndian.Uint16(sum[:2])) % 65534
	for offset := range 65534 {
		host := (start+offset)%65534 + 1
		address := netip.AddrFrom4([4]byte{10, 96, byte(host >> 8), byte(host)})
		if _, collision := used[address]; !collision {
			return address.String(), nil
		}
	}
	return "", errors.New("service address pool is exhausted")
}
func (c *Controller) Run(ctx context.Context, period time.Duration) error {
	if period <= 0 {
		period = 2 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		services, err := c.store.ListServices(ctx)
		if err == nil {
			for _, service := range services {
				_ = c.Reconcile(ctx, service)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
