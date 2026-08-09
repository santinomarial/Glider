//go:build linux

package network

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
)

type Service struct {
	ID        string            `json:"id"`
	ClusterIP netip.Addr        `json:"cluster_ip"`
	Port      uint16            `json:"port"`
	Endpoints []ServiceEndpoint `json:"endpoints"`
}

type ServiceEndpoint struct {
	Address netip.Addr `json:"address"`
	Port    uint16     `json:"port"`
}

var clusterServicePrefix = netip.MustParsePrefix("10.96.0.0/16")

// EnsureServices durably publishes the complete service snapshot before
// replacing Glider's nftables rules. Any later endpoint reconciliation rebuilds
// the same service data plane after a crash or manual firewall deletion.
func (m *Manager) EnsureServices(services []Service) error {
	for i := range services {
		service := &services[i]
		if !safeOwner(service.ID) || !service.ClusterIP.Is4() || !clusterServicePrefix.Contains(service.ClusterIP) || service.Port == 0 {
			return errors.New("invalid service data-plane record")
		}
		for _, endpoint := range service.Endpoints {
			if !endpoint.Address.Is4() || endpoint.Port == 0 {
				return errors.New("invalid service endpoint")
			}
		}
		sort.Slice(service.Endpoints, func(a, b int) bool {
			if service.Endpoints[a].Address == service.Endpoints[b].Address {
				return service.Endpoints[a].Port < service.Endpoints[b].Port
			}
			return service.Endpoints[a].Address.Less(service.Endpoints[b].Address)
		})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	data, err := json.MarshalIndent(services, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(m.root, "services.json")
	temporary := final + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temporary, final); err != nil {
		return err
	}
	if directory, openErr := os.Open(m.root); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return m.reconcileNAT()
}

func (m *Manager) loadServices() ([]Service, error) {
	data, err := os.ReadFile(filepath.Join(m.root, "services.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var services []Service
	if err := json.Unmarshal(data, &services); err != nil {
		return nil, err
	}
	return services, nil
}
