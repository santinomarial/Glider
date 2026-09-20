# Local control-plane demo

Run a small scheduling and rollout scenario without provisioning a Linux
cluster. You will see real controllers use embedded etcd to create tasks,
assign them, publish service discovery state, and begin a rolling update.
The demo supplies simulated node health and endpoints; it does not run images.

## Requirements

- Git, Bash, and Make.
- Go 1.26.6 or newer on macOS or Linux, or Docker if Go is not installed.
- Network access for the first dependency or container-image download.
- Permission to bind ephemeral ports on loopback and create temporary files.

No administrator privileges or external etcd service are required for the
native Go path. Docker provides an alternative build environment, not Glider's
container runtime.

## Run the scenario

1. Clone the repository and enter it:

   ```bash
   git clone https://github.com/santinomarial/Glider.git
   cd Glider
   ```

2. Run the demo:

   ```bash
   make demo
   ```

3. Look for the success markers:

   ```text
   READY workload=api replicas=2 service=api.glider endpoints=[10.96.20.195]
   ROLLOUT image=demo/api:v2 readiness-gated=true healthy-v1-preserved-until-ready=true
   DEMO GREEN
   ```

The scenario has a 20-second execution deadline after Go finishes compiling.
Dependency downloads and compilation can take longer on the first run.

## Interpret the result

| Stage | Real behavior | Fixture input |
|---|---|---|
| Desired state | etcd stores a two-replica workload and matching service | One Ready node with declared capacity |
| Placement | The workload controller creates tasks; the scheduler binds them | Image name `demo/api:v1` |
| Readiness | The service controller consumes task endpoint and health state | The demo reports addresses and healthy status directly |
| Discovery | The discovery package looks up `api.glider` in stored service state | No operating-system DNS server or traffic probe |
| Rollout | The workload template changes to `demo/api:v2`; reconciliation creates replacement work | The demo marks replacement tasks healthy |

The `endpoints` field prints the service VIP returned by the discovery lookup.
It is not an HTTP endpoint available from your laptop. The rollout line is a
scenario summary, not a measurement of live application availability. The
demo does not prove multi-host failover, kernel isolation, or API authentication.

Read the implementation in [cmd/glider-demo](../../cmd/glider-demo/main.go).
The [runtime flows](../architecture/runtime-flows.md) explain how these
controllers fit into a running cluster.

## Cleanup and troubleshooting

The normal exit path closes embedded etcd and removes its temporary state. The
demo creates no persistent Glider cluster or workload containers. A forced
process kill can leave a `glider-demo-*` temporary directory; remove only the
specific directory from that interrupted run after confirming it is unused.

- If dependencies cannot download, check network access and retry.
- If loopback binding fails, check local endpoint protection or sandbox rules.
- If Go is installed but too old, update it to the version in `go.mod`. The
  Docker fallback runs only when the `go` command is absent.
- Embedded etcd may emit server-closed errors while shutting down after
  `DEMO GREEN`. Check the exit status as well as the markers; startup errors
  or a nonzero exit status need investigation.

## Continue learning

Use [the architecture overview](../architecture/overview.md) to follow the
desired-state lifecycle. For real container execution, use the privileged
Linux [runtime tests](../../scripts/test-linux-runtime.sh) in a disposable
environment. Cluster setup starts with [installation](../operations/install.md)
and [PKI](../operations/pki.md).
