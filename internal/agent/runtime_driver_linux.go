//go:build linux

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/santinomarial/glider/internal/api"
	healthcheck "github.com/santinomarial/glider/internal/health"
	imagemanager "github.com/santinomarial/glider/internal/image/manager"
	"github.com/santinomarial/glider/internal/logfile"
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
	execHelper   string
	gcGrace      time.Duration
	minFreeBytes uint64
	minFreePct   float64
	secrets      SecretResolver
}

const maxLogBytes = 64 << 20

var ErrDiskPressure = errors.New("node disk pressure")

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

func (d *RuntimeDriver) EnsureServices(services []api.Service) error {
	desired := make([]containernetwork.Service, 0, len(services))
	for _, service := range services {
		clusterIP, err := netip.ParseAddr(service.Status.ClusterIP)
		if err != nil {
			continue
		}
		item := containernetwork.Service{ID: service.Metadata.ID, ClusterIP: clusterIP, Port: service.Spec.Port}
		for _, endpoint := range service.Status.Endpoints {
			address, err := netip.ParseAddr(endpoint.Address)
			if err != nil {
				continue
			}
			item.Endpoints = append(item.Endpoints, containernetwork.ServiceEndpoint{Address: address, Port: endpoint.Port})
		}
		desired = append(desired, item)
	}
	return d.network.EnsureServices(desired)
}

func (d *RuntimeDriver) CheckProbe(ctx context.Context, a api.Assignment, probe api.Probe) error {
	endpoint, err := d.network.Endpoint(containerID(a))
	if err != nil {
		return err
	}
	address := endpoint.Address.String()
	switch probe.Kind {
	case api.ProbeTCP:
		_, port, err := net.SplitHostPort(probe.Address)
		if err != nil {
			return err
		}
		probe.Address = net.JoinHostPort(address, port)
	case api.ProbeHTTP:
		parsed, err := url.Parse(probe.URL)
		if err != nil {
			return err
		}
		_, port, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr == nil {
			parsed.Host = net.JoinHostPort(address, port)
		} else {
			parsed.Host = address
		}
		probe.URL = parsed.String()
	}
	return (&healthcheck.Prober{}).Check(ctx, probe)
}

func (d *RuntimeDriver) EndpointAddress(a api.Assignment) (string, error) {
	endpoint, err := d.network.Endpoint(containerID(a))
	if err != nil {
		return "", err
	}
	return endpoint.Address.String(), nil
}

func (d *RuntimeDriver) Logs(a api.Assignment, tailBytes int64) ([]byte, error) {
	if tailBytes <= 0 || tailBytes > 4<<20 {
		return nil, errors.New("tail_bytes must be between 1 and 4194304")
	}
	path := filepath.Join(d.dataRoot, "logs", containerID(a)+".log")
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	if _, err = file.Seek(offset, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, tailBytes))
}
func (d *RuntimeDriver) Stats(a api.Assignment) (cgroup.Stats, error) {
	manager, err := cgroup.NewManager()
	if err != nil {
		return cgroup.Stats{}, err
	}
	return manager.Stats(containerID(a))
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
	return &RuntimeDriver{dataRoot: dataRoot, stateRoot: filepath.Join(dataRoot, "containers"), images: images, network: network, startTimeout: 30 * time.Second, execHelper: "/usr/libexec/glider-exec", gcGrace: 24 * time.Hour, minFreeBytes: 2 << 30, minFreePct: 10}, nil
}

func (d *RuntimeDriver) ConfigureStorage(grace time.Duration, minFreeBytes uint64, minFreePercent float64) error {
	if grace < 0 || minFreePercent < 0 || minFreePercent > 100 {
		return errors.New("invalid storage policy")
	}
	d.gcGrace, d.minFreeBytes, d.minFreePct = grace, minFreeBytes, minFreePercent
	return nil
}

func (d *RuntimeDriver) CollectImages(ctx context.Context) (imagemanager.GCResult, error) {
	return d.images.Collect(ctx, d.gcGrace)
}

func (d *RuntimeDriver) DiskUsage() (total, available uint64, pressured bool, err error) {
	var stat syscall.Statfs_t
	if err = syscall.Statfs(d.dataRoot, &stat); err != nil {
		return 0, 0, false, fmt.Errorf("stat data filesystem: %w", err)
	}
	total = stat.Blocks * uint64(stat.Bsize)
	available = stat.Bavail * uint64(stat.Bsize)
	return total, available, diskPressured(total, available, d.minFreeBytes, d.minFreePct), nil
}

func diskPressured(total, available, minBytes uint64, minPercent float64) bool {
	if available < minBytes {
		return true
	}
	return total > 0 && float64(available)*100/float64(total) < minPercent
}

func (d *RuntimeDriver) ensureDiskCapacity(ctx context.Context) error {
	_, _, pressured, err := d.DiskUsage()
	if err != nil || !pressured {
		return err
	}
	if _, err := d.CollectImages(ctx); err != nil {
		return fmt.Errorf("%w: collection failed: %v", ErrDiskPressure, err)
	}
	_, available, pressured, err := d.DiskUsage()
	if err != nil {
		return err
	}
	if pressured {
		return fmt.Errorf("%w: %d bytes available", ErrDiskPressure, available)
	}
	return nil
}
func (d *RuntimeDriver) SetExecHelper(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("exec helper path must be absolute")
	}
	d.execHelper = path
	return nil
}
func (d *RuntimeDriver) SetSecretResolver(resolver SecretResolver) { d.secrets = resolver }
func (d *RuntimeDriver) Exec(ctx context.Context, a api.Assignment, command []string) ([]byte, int, error) {
	if len(command) == 0 {
		return nil, 0, errors.New("command is required")
	}
	rec, err := processstate.Load(processstate.Dir(d.stateRoot, containerID(a)))
	if err != nil {
		return nil, 0, err
	}
	alive, err := process.ValidateProcessIdentity(process.ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime})
	if err != nil || !alive {
		if err == nil {
			err = errors.New("container init is not alive")
		}
		return nil, 0, err
	}
	args := append([]string{"--pid", fmt.Sprint(rec.InitPID)}, command...)
	cmd := exec.CommandContext(ctx, d.execHelper, args...)
	output := &limitedBuffer{limit: 4 << 20}
	cmd.Stdout = output
	cmd.Stderr = output
	err = cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
		err = nil
	} else if err != nil {
		return output.Bytes(), 0, err
	}
	return output.Bytes(), code, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := w.limit - w.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = w.Buffer.Write(p)
	}
	return original, nil
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
	if err := d.ensureDiskCapacity(ctx); err != nil {
		return Observed{}, err
	}
	var secretEnv []string
	if len(a.Secrets) > 0 {
		if d.secrets == nil {
			return Observed{}, errors.New("assignment references secrets but node secret delivery is not configured")
		}
		resolved, resolveErr := d.secrets.Resolve(ctx, a)
		if resolveErr != nil {
			return Observed{}, fmt.Errorf("resolve assignment secrets: %w", resolveErr)
		}
		secretEnv = resolved
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
	logDir := filepath.Join(d.dataRoot, "logs")
	logFile, err := logfile.New(filepath.Join(logDir, id+".log"), maxLogBytes, 3)
	if err != nil {
		return Observed{}, err
	}
	cfg := process.Config{RootFS: prepared.RootFS, Argv: argv, Hostname: a.TaskID, StateDir: d.stateRoot, ContainerID: id, Env: append([]string(nil), prepared.Image.Config.Env...), SecretEnv: secretEnv, WorkingDir: prepared.Image.Config.WorkingDir, Resources: cgroup.Resources{CPUCores: float64(a.Resources.CPUMilli) / 1000, MemoryBytes: a.Resources.MemoryBytes}, Stdout: logFile, Stderr: logFile}
	cfg.ConfigureNetwork = func(pid int) error {
		_, err := d.network.EnsureWithPorts(context.Background(), id, pid, ports)
		return err
	}
	stop := make(chan os.Signal, 1)
	go func() { defer logFile.Close(); _, _ = process.Run(stop, cfg) }()
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
