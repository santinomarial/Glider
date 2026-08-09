# Incident response

1. Acknowledge the page, record UTC start time and release commit, and assign an
   incident commander. Preserve correlated JSON logs and audit events.
2. Check etcd quorum before restarting anything. Never restart every etcd or
   control-plane member together.
3. For node uncertainty, cordon first. Allow lease self-fencing and generation
   reassignment to complete before restoring traffic or removing the node.
4. For suspected credential exposure, deny the principal, rotate its leaf
   certificate, and rotate affected secrets. Rotate the cluster encryption key
   only through the documented migration procedure.
5. For data loss, stop mutations, select a verified encrypted off-host recovery
   point, and follow the restore runbook in an isolated environment before
   promoting it.
6. Validate API, scheduling, service traffic, alerts, and backups before ending
   mitigation. Record recovery times, customer impact, and every command used.
7. Publish a blameless review with root cause, detection gap, corrective owner,
   and deadline. Attach the release evidence bundle and retained artifacts.
