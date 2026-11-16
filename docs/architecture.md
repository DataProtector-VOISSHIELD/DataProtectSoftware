# VOIS DataShield architecture

## Decision

Kopia is the only filesystem snapshot engine and repository format in this disposable test build. Do not maintain a parallel chunk/manifest engine. Old prototype data is intentionally not migrated. PostgreSQL is the product catalog, not the source of file content or tree metadata.

## Components

### PrimaryServer

- Go control plane and React/Vite/TypeScript console, deployed with PostgreSQL using Docker Compose.
- Owns local login, agent enrollment, client inventory, policy assignment/versioning, job authorization and lifecycle, retry, audit, and snapshot references.
- Stores Kopia snapshot IDs, source/client/policy/job associations, timestamps, and expiry state in PostgreSQL.
- Does not proxy backup data.

### Linux client agent

- Native system service polls the control plane, reports heartbeats, executes authorized jobs, and posts status/log summaries.
- Invokes the installed Kopia CLI for snapshot, restore, policy retention, and expiration.
- The Kopia repository connection and credential are configured for the same OS account running the agent (root in the current lab).
- Requires a new agent build/install when the source changes.

### MediaServer

- Runs Kopia's repository server and persists its encrypted repository on a dedicated path/disk.
- Kopia handles content chunking, encryption, configured compression, deduplication, snapshots, and repository maintenance.
- Media endpoint reachability in the console currently means TCP connectivity only; repository authentication/integrity need Kopia checks.
- Use a distinct repository and recovery key per security domain until tenant isolation is threat-reviewed. Kopia repository-server ACLs do not provide a proven shared-content tenant boundary.

## Current job flow

1. Administrator enrolls a Linux client and creates a Kopia policy or one-time job.
2. Control plane persists a queued job with client and policy association.
3. Client agent claims it, invokes Kopia using that account's repository connection, and captures output. The client sends encrypted repository traffic directly to the Kopia endpoint on MediaServer; the PrimaryServer is not in the backup-data path.
4. Agent reports outcome, snapshot IDs and bounded logs. Control plane updates the job and PostgreSQL snapshot index.
5. Operator inspects job details or queues a full-snapshot restore to a new folder on a registered Linux client.

Kopia owns the repository's physical indexes, content objects, and snapshot trees on MediaServer. PostgreSQL on PrimaryServer is the product catalog: it records client/policy/job state and references to snapshots that agents report. It is not a copy of Kopia's repository indexes and is not the source of truth for file trees. Retention counts are represented in the policy and passed to Kopia. Expiration can make snapshots unavailable; repository maintenance separately reclaims unreferenced storage. Retention preview and snapshot holds are future safeguards.

Every client must have a Kopia repository-server connection configured under the same OS identity as the backup-agent service. In the current lab that identity is root. The Kopia account identity is unique per client (`username@hostname`); credentials must be provisioned out of band and must never be sent in job payloads. A missing connection fails on the client before a snapshot can be created; it does not cause the PrimaryServer to relay data.

## Fresh lab topology

- PrimaryServer: Ubuntu 24.04, Compose control plane and PostgreSQL.
- MediaServer: Ubuntu 24.04, Kopia repository server and persistent repository storage.
- Client: Ubuntu 24.04 VM, native agent and Kopia CLI.
- Host-only VirtualBox networking for service traffic; NAT can be kept for package downloads.

For a clean test reset, discard the lab PostgreSQL volume and initialize a new Kopia repository path. No old custom data migration exists. Keep the source tree and sensitive `.env`/repository recovery material separate.

## Product boundaries and future work

Kopia supplies filesystem snapshot and repository operations. DataShield supplies the enterprise management experience: local users first, later LDAP/AD and SSO; services inventory; client-to-policy assignment; storage targets; job scheduling/monitoring/logs/retry; catalog; alerts; and audit.

Current scope is Linux file-level backup and full-snapshot redirected restore. Selective path browsing, original-path restore, Windows service/VSS, BMR, database/application-aware integrations, cloud destinations, replication, and VMware/Hyper-V/OpenStack APIs are not complete. Treat each as a separate validated extension. A VM guest is backed up as an enrolled OS client until a hypervisor provider integration is built.

Before enterprise or managed-service use, add production TLS, roles, secret/key recovery workflows, independent catalog backup/restore, health and capacity monitoring, retention preview/holds, upgrade compatibility, tenant isolation review, load testing, and documented disaster recovery.
