//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/santinomarial/glider/internal/api"
	imagemanager "github.com/santinomarial/glider/internal/image/manager"
	containernetwork "github.com/santinomarial/glider/internal/network"
	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"github.com/santinomarial/glider/internal/runtime/process"
	processstate "github.com/santinomarial/glider/internal/runtime/process/state"
)

// RuntimeDriver is gliderd's production bridge to Glider's native image,
// runtime, cgroup, and networking packages. Launches continue in a goroutine;
// durable runtime state, rather than that goroutine, is the source of truth.
type RuntimeDriver struct {
	dataRoot     string
	stateRoot    string
	images       *imagemanager.Manager
	network      *containernetwork.Manager
	startTimeout time.Duration
}

func (d *RuntimeDriver) EnsureOverlay(localTunnel string, peers []api.Node, mtu int) error {
	local, err := netip.ParseAddr(localTunnel)
	if err != nil {
		return err
	}
	desired := make([]containernetwork.Peer, 0, len(peers))
	for _, node := range peers {
		if node.Status.Phase != api.NodeReady || node.Spec.PodCIDR == "" || node.Spec.TunnelAddress == "" {
			continue
		}
		cidr, err := netip.ParsePrefix(node.Spec.PodCIDR)
		if err != nil {
			return err
		}
		tunnel, err := netip.ParseAddr(node.Spec.TunnelAddress)
		if err != nil {
			return err
		}
		desired = append(desired, containernetwork.Peer{NodeID: node.Metadata.ID, PodCIDR: cidr, TunnelAddress: tunnel})
	}
	return d.network.EnsureOverlay(local, desired, mtu)
}

func NewRuntimeDriver(dataRoot, networkCIDR string, insecureRegistry bool) (*RuntimeDriver, error) {
	if dataRoot == "" || !filepath.IsAbs(dataRoot) {
		return nil, errors.New("data root must be absolute")
	}
	images, err := imagemanager.New(dataRoot, http.DefaultClient, nil, insecureRegistry)
	if err != nil {
		return nil, err
	}
	network, err := containernetwork.NewManager(filepath.Join(dataRoot, "network"), networkCIDR, containernetwork.DefaultBridge)
	if err != nil {
		return nil, err
	}
	return &RuntimeDriver{dataRoot: dataRoot, stateRoot: filepath.Join(dataRoot, "containers"), images: images, network: network, startTimeout: 30 * time.Second}, nil
}

func containerID(a api.Assignment) string {
	sum := sha256.Sum256([]byte(a.TaskID))
	return fmt.Sprintf("%x-g%d", sum[:10], a.Generation)
}

func (d *RuntimeDriver) Ensure(ctx context.Context, a api.Assignment) (Observed, error) {
	id := containerID(a)
	if observed, err := d.observeID(id); err != nil || observed.Phase == ObservedRunning {
		return observed, err
	} else if observed.Phase != ObservedAbsent {
		if err := d.cleanup(ctx, id); err != nil {
			return Observed{}, err
		}
	} else if _, err := os.Stat(processstate.Dir(d.stateRoot, id)); err == nil {
		if err := d.cleanup(ctx, id); err != nil {
			return Observed{}, err
		}
	}
	if a.Image == "" {
		return Observed{}, errors.New("assignment image is required")
	}
	prepared, err := d.images.Prepare(ctx, a.Image, id)
	if err != nil {
		return Observed{}, fmt.Errorf("prepare image: %w", err)
	}
	argv := append([]string(nil), a.Command...)
	if len(argv) == 0 {
		argv = append(append([]string(nil), prepared.Image.Config.Entrypoint...), prepared.Image.Config.Cmd...)
	}
	if len(argv) == 0 {
		return Observed{}, errors.New("image and assignment provide no command")
	}
	ports := make([]containernetwork.PortMapping, 0, len(a.HostPorts))
	for _, p := range a.HostPorts {
		ports = append(ports, containernetwork.PortMapping{Protocol: "tcp", HostPort: p, ContainerPort: p})
	}
	if err := containernetwork.ConfigureDNS(prepared.RootFS, containernetwork.HostNameservers()); err != nil {
		return Observed{}, err
	}
	cfg := process.Config{RootFS: prepared.RootFS, Argv: argv, Hostname: a.TaskID, StateDir: d.stateRoot, ContainerID: id, Env: append([]string(nil), prepared.Image.Config.Env...), WorkingDir: prepared.Image.Config.WorkingDir, Resources: cgroup.Resources{CPUCores: float64(a.Resources.CPUMilli) / 1000, MemoryBytes: a.Resources.MemoryBytes}}
	cfg.ConfigureNetwork = func(pid int) error {
		_, err := d.network.EnsureWithPorts(context.Background(), id, pid, ports)
		return err
	}
	stop := make(chan os.Signal, 1)
	go func() { _, _ = process.Run(stop, cfg) }()
	deadline := time.NewTimer(d.startTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		observed, loadErr := d.observeID(id)
		if loadErr == nil && observed.Phase == ObservedRunning {
			return observed, nil
		}
		select {
		case <-ctx.Done():
			return Observed{}, ctx.Err()
		case <-deadline.C:
			return Observed{}, errors.New("container start timed out")
		case <-ticker.C:
		}
	}
}

func (d *RuntimeDriver) Observe(_ context.Context, a api.Assignment, _ Observed) (Observed, error) {
	return d.observeID(containerID(a))
}

func (d *RuntimeDriver) observeID(id string) (Observed, error) {
	rec, err := processstate.Load(processstate.Dir(d.stateRoot, id))
	if os.IsNotExist(err) {
		return Observed{Phase: ObservedAbsent, ContainerID: id}, nil
	}
	if err != nil {
		return Observed{}, err
	}
	if rec.Phase == processstate.Running || rec.Phase == processstate.Created {
		alive, err := process.ValidateProcessIdentity(process.ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime})
		if err != nil {
			return Observed{}, err
		}
		if alive {
			return Observed{Phase: ObservedRunning, ContainerID: id}, nil
		}
	}
	if rec.Phase == processstate.Failed {
		return Observed{Phase: ObservedFailed, ContainerID: id, Error: rec.Error}, nil
	}
	return Observed{Phase: ObservedAbsent, ContainerID: id}, nil
}

func (d *RuntimeDriver) Remove(ctx context.Context, a api.Assignment, _ Observed) error {
	id := containerID(a)
	rec, err := processstate.Load(processstate.Dir(d.stateRoot, id))
	if err == nil && rec.InitPID > 0 {
		alive, verifyErr := process.ValidateProcessIdentity(process.ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime})
		if verifyErr != nil {
			return verifyErr
		}
		if alive {
			if signalErr := syscall.Kill(rec.InitPID, syscall.SIGTERM); signalErr != nil {
				return signalErr
			}
		}
		for alive {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
			alive, err = process.ValidateProcessIdentity(process.ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime})
			if err != nil {
				return err
			}
		}
	}
	return d.cleanup(ctx, id)
}

func (d *RuntimeDriver) cleanup(ctx context.Context, id string) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := process.Recover(d.stateRoot, id)
		if errors.Is(err, process.ErrContainerNotFound) {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := d.network.Remove(id); err != nil {
		return err
	}
	return d.images.Remove(id)
}
