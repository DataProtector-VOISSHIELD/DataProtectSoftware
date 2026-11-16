# Build and test roadmap

This roadmap starts with a small Ubuntu 24.04 VirtualBox lab and expands only after each stage has a working demonstration. A stage is complete when its acceptance checks pass and its setup/recovery steps are documented.

## Lab layout

- **PrimaryServer VM:** Ubuntu 24.04; Docker Compose runs the browser/control service and PostgreSQL catalog.
- **MediaServer VM:** Ubuntu 24.04; runs the media/data service and writes repository data to a dedicated virtual disk.
- **Linux client VM:** Ubuntu 24.04; runs the backup agent as a native system service and holds disposable test data.
- **Recovery target VM:** created when needed; starts empty and is used to prove full-machine recovery.
- Use a private/host-only VirtualBox network for the lab. Keep test data disposable and take VM snapshots before destructive recovery exercises.
- Keep source code and deployment documentation in this repository. Deploy a tagged build to the Ubuntu server and client VMs so the tested version is identifiable.

## Stage 0 — Freeze the first milestone

Agree and record the initial workload, protected paths, retention, expected restore time, and acceptable data loss. Decide whether the first Ubuntu recovery method is file-level reconstruction or block-level imaging; do not promise general BMR until the chosen method boots on a clean target VM.

**Acceptance checks:** the initial OS/version, backup scope, repository location/capacity, and recovery objective are written down; the recovery method has a small proof-of-concept plan.

## Stage 1 — Make the lab repeatable

Create the Ubuntu server and client VMs, configure network access, assign persistent storage for the catalog and repository, and document how to rebuild the lab. Select the implementation language and wire format before starting the service code.

**Acceptance checks:** a rebuilt server can start the services from documented steps; PostgreSQL data and repository payloads live in persistent storage; a client can reach the server over the lab network.

## Stage 2 — Server foundation and local browser login

Build the control-plane API and browser console, local administrator login, session handling, initial roles, audit events, and PostgreSQL schema/migrations. Add a documented admin bootstrap and recovery procedure. Keep identity-provider integration behind an interface for later LDAP/SSO.

**Acceptance checks:** an operator can sign in, create/disable a local account, see audit events, and restart the Compose stack without losing catalog data; unauthenticated users cannot use protected management operations.

## Stage 3 — Services inventory, enrollment, and job lifecycle

Build the portal's **Services** area with Physical servers, Virtual servers, and Other workloads categories. For the first demo, register Linux physical/guest servers by hostname/VM name and/or IP. Issue a short-lived, single-use enrollment token; the agent exchanges it with the control plane for a scoped client credential. Add inventory, heartbeat, disable/revoke, and audit actions. Build a durable job monitor with per-attempt status, sanitized logs, and retry history. Establish versioned APIs and manifests. Hypervisor APIs remain future integrations; a VM guest is initially treated as its registered operating-system client.

**Acceptance checks:** a client can be registered by hostname and/or IP, enroll through one-time token exchange, appear in the correct Services category, and be revoked; job state and logs survive server restarts; an invalid credential is rejected; a failed backup can be retried without erasing its original attempt.

## Stage 4 — Kopia-only filesystem engine

Use Kopia as the sole filesystem engine for the test product. Test data is disposable; do not spend effort preserving or migrating old custom-engine backup points. Pin a Kopia release and use the Kopia repository server on the MediaServer VM. The control plane owns enrollment, job authorization, status, audit, and PostgreSQL business metadata. Kopia owns filesystem snapshots, repository manifests, chunking, compression, deduplication, encryption, retention/expiration primitives, verification, and file restore.

**Acceptance checks:** repeat an unchanged snapshot and measure dedup savings; modify part of a large file and measure incremental transfer; restore selected files and compare bytes and supported metadata; interrupt and restart the client/repository; run repository verification; test repository credential recovery. Review the Kopia repository-server ACL/content limitation and use isolated repositories for separate trust domains.

## Stage 5 — Integrate the engine with the console

Complete the Kopia-only console integration. Add selective restores through Kopia's supported file browsing/restore operations; mirror snapshot IDs, source, client, policy/version, destination, timestamps, and summary statistics in PostgreSQL without making PostgreSQL the authority for repository contents. Improve policy creation with source/exclusions, storage target, retention, and assigned clients; show associated snapshots, run counts, and last/next run on policy detail. Extend restore as client → timestamp/policy → folder/file → original/redirected destination. Add safe retention preview, job detail/log views, and Kopia repository health/usage.

**Acceptance checks:** create and assign a policy; run it from the GUI; monitor each client job; open detailed sanitized logs; retry a failed attempt; see policy run counts and associated snapshots; pin/override retention on one snapshot and verify it survives policy aging; select a client's restore timestamp and policy, browse files, and restore to original or redirected paths on a compatible client; confirm snapshot-to-policy/version association; a clean server rebuild reconnects to Kopia and restores using documented recovery credentials.

## Stage 6 — Policies, retention, and operations

Build protection policies as reusable client groups with versioned assignments. Policies define selection, schedules, retention, and a default destination; allow a client-level destination override and show the resolved destination. Translate supported filesystem settings to Kopia policy settings. Add safe expiration, repository maintenance, health reporting, alerts/logging, capacity reporting, catalog backup, and server upgrades. Then add repository-level replication via Kopia `repository sync-to` as a separate scheduled/monitored workflow, with lag reporting and restore verification. Deletion/maintenance must follow Kopia repository safety rules and never race active snapshots.

**Acceptance checks:** scheduled jobs run; retained restore points remain restorable after cleanup; catalog recovery works independently of the media repository; an interrupted job and a server restart do not leave the catalog and repository inconsistent.

## Stage 7 — Full-machine recovery to a new VM

Build bootable recovery media, repository discovery/authentication, disk-layout handling, and the selected Ubuntu recovery method. Validate first against the exact supported Ubuntu configuration; then expand the compatibility matrix. Keep the source VM and destination VM separate.

**Acceptance checks:** restore into an empty VirtualBox VM, boot it, verify services/files, and record elapsed recovery time and manual steps. Repeat after a failed/interrupted recovery. This is the gate for claiming BMR support for that tested configuration.

## Stage 8 — Windows support

Build a native Windows agent/service with the pinned Kopia client and validate file/metadata behavior first. Test VSS snapshots and application writers separately; do not assume filesystem snapshots guarantee application consistency. Define and validate Windows full-machine recovery separately.

**Acceptance checks:** Windows file backup/restore preserves the explicitly supported metadata; interrupted jobs recover cleanly; full-machine recovery is claimed only for configurations that boot successfully in a fresh VM.

## Stage 9 — Security and scale hardening

Threat-model the server, agents, repository, keys, and restore workflow. Add external identity (LDAP/AD, then SSO as chosen), stronger role separation, tenant boundaries if offering a service, multiple media servers/repositories, monitoring, upgrade compatibility, and load testing based on real usage.

**Acceptance checks:** access boundaries are verified across users/tenants; key loss and identity-provider outage procedures are rehearsed; capacity and recovery objectives hold under representative load; upgrades preserve old restore points.

## Later integrations

Add VMware, Hyper-V, or OpenStack adapters only after the guest-agent path and shared management contracts are stable. Each adapter gets its own discovery, snapshot/consistency, image backup, and restore acceptance checks. VirtualBox guest backup in the initial lab does not itself require a VirtualBox API integration.

## Working loop for each stage

1. Agree on the stage's scope and acceptance checks.
2. Implement a small vertical slice in the repository and document configuration/API/storage formats.
3. Build and deploy that version to the Ubuntu lab.
4. Run the acceptance checks and preserve logs/results without secrets.
5. Fix issues, repeat the failed checks, and update troubleshooting/recovery notes.
6. Tag the known-good version before moving to the next stage.
