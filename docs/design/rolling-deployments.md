# Rolling deployments (Phase 17)

Each Workload template is hashed into a deterministic revision. Tasks persist
that revision, allowing the controller to reconstruct rollout progress entirely
from etcd after a restart.

The controller enforces `maxSurge` and `maxUnavailable` as hard budgets. It may
create new-revision tasks up to the surge ceiling, but removes an old task only
when the remaining Ready count stays at or above `replicas-maxUnavailable`.
With `maxUnavailable: 0`, this means a replacement must become Ready before any
healthy old replica is removed.

Workload status records the current/update revisions, rollout start and last
progress timestamps, updated/Ready counts, and one of `Progressing`, `Stalled`,
or `Complete`. Crossing `progressDeadline` marks a rollout Stalled; it does not
delete healthy old capacity. Changing the template again creates a new revision
and begins a new durable rollout.

The default strategy permits one surge replica and no unavailable replicas.
Both budgets may not be zero simultaneously; Glider normalizes that unsafe
configuration to one surge replica so the deployment can make progress without
dropping availability.
