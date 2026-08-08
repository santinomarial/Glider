# Authenticated node operations

`gliderd --operations-listen` exposes a mutually authenticated TLS gRPC
endpoint. `glider logs TASK` and `glider stats TASK` first resolve the current
task assignment through the control plane and then present both task ID and
assignment generation to the owning node. The node rejects stale generations
and assignments owned elsewhere.

Workload stdout and stderr are captured together in node-local files with a
64 MiB segment limit, three retained backups, restrictive permissions, and
durable rotation. A log response is bounded to 4 MiB. Stats are read directly
from the assigned container's cgroup v2 files and are never accepted from the
client. The node endpoint permits only `operator` and `admin` certificate
roles.

Nodes advertise `spec.operations_address`. Operators may override discovery
with the CLI's `--node-endpoint` flag for maintenance. Interactive exec and
true follow-mode log streaming remain blocking work; this milestone does not
claim that the complete node-operations production gate is closed.
