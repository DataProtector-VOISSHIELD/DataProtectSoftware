# Backup Console Product Requirements

## Product goal

An administrator should be able to sign in, protect Linux and Windows systems, define where and how backups run, see what is protected, and browse or restore backed-up data from the browser. Routine operations should not require SSH or commands on the protected client after its agent has been installed and enrolled.

The browser is the control surface. Agents perform local snapshot and filesystem work; media servers move, deduplicate, compress, encrypt, and store backup data; the catalog indexes the inventory and recovery metadata.

## Engine decision (2026-11-13)

Kopia is the sole filesystem backup engine for this test product. All new file backup, restore, policy retention, compression, deduplication, encryption, and repository maintenance workflows must use Kopia. Do not add another filesystem engine or an engine selector in the UI/API. PostgreSQL remains the business catalog and Kopia repository remains authoritative for snapshot content and filesystem metadata. VOIS DataShield continues to own login, clients, enrollment, policy membership/versioning, orchestration, jobs, alerts, audit, and operator UX.

The current Linux lab supports Kopia only. Windows filesystem support will use the same Kopia engine after the native Windows service and VSS/metadata behavior are separately implemented and verified. Kopia does not provide BMR or hypervisor snapshot integrations; those remain separate future capabilities.

## Navigation and workflows

### Console page layout

- Keep the portal organized as separate, task-focused pages so operators do not have to manage clients, policies, destinations, snapshots, restores, and job diagnostics in one long page.
- Provide distinct pages for Services, Policies, Media Targets, Snapshots/Restore Points, Restore, and Activity/Job Monitor. Policy details may link to their associated snapshots and jobs, but each inventory must also have its own searchable page.
- Show a useful empty state on every page, including a clear explanation of what needs to be configured or run next and a direct action to do it. Do not leave a blank panel or an indefinite loading indicator when there are zero records or an API error.
- Provide consistent page-level refresh, filters, loading/error states, confirmation for destructive or security-sensitive actions, and responsive layouts.

### Services and workload registration

- Add a primary **Services** area in the portal. Its landing page groups protected systems by workload type: **Physical servers**, **Virtual servers**, and **Other workloads**.
- Each workload page supports registering a service by hostname/VM name and/or IP address, then shows the registered inventory, OS/platform, agent state, and assigned policy. Virtual platforms (VMware, Hyper-V, OpenStack) are future provider integrations; the first implementation is guest-agent registration for Linux and Windows.
- Provide a secure enrollment-token exchange: an administrator creates a short-lived, single-use token in the portal; the client agent presents it to the control plane; the control plane validates it and returns a scoped client credential. Bind the registered identity to its hostname/platform and show enrollment/heartbeat status. Do not expose repository passwords or destination credentials to the client through the UI.
- Support client lifecycle actions such as view details, assign policy, disable/revoke credentials, and re-enroll. Record these actions in the audit trail.

### Policy and protection workflow

- From **Policies**, an operator creates a policy by defining source paths/exclusions, backup engine/workload options, retention/aging rules, and a target storage destination, then selects one or more registered clients to assign to it.
- The **Protection** client inventory is a compact one-row-per-client table showing client name and platform/agent description, assigned policies, protected save sets/source paths, and online state/last seen. Display policy exclusions alongside the associated save set.
- Linux policies may select `/` as the whole-root save set. For this scope, exclude volatile pseudo-filesystem and temporary paths (`/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/var/tmp`) by default and let operators add Kopia ignore patterns relative to the selected source. The UI and job payload must preserve those exclusions for manual and scheduled runs.
- Kopia is fixed as the filesystem engine in the current product UI and API; do not expose a selectable engine for Linux file backup policies.
- Show policy membership and effective destination on both policy and client pages. Support policy-level destination defaults and client-level overrides, with precedence and override source clearly visible.
- Provide **Run now** for a policy. The control plane queues one authorized backup job per assigned client and associates each run and resulting restore point with the policy ID and exact policy version.
- Before queueing, validate client eligibility, destination health, and required agent/repository configuration. Report actionable errors without exposing secrets.
- Each policy detail page shows its assigned clients, effective destination, retention rule, total run count, last/next run, and all associated restore points/snapshots with timestamp, client, job result, data summary, and retention/expiry state.
- Operators can change retention for an individual restore point (for example, pin/keep indefinitely or set an explicit expiry) without silently changing the policy defaults. Require authorization, show the impact/expiry date, audit the action, and ensure per-snapshot holds override policy aging until released.

### Job monitoring and logs

- Provide a **Job Monitor** view for backup and restore jobs with client/workload, policy/version, source, destination, submission/start/end times, progress where available, status, and result summary.
- Selecting or double-clicking a job opens a detail view with its timestamped logs/events, skipped-file warnings, retry history, and sanitized failure details. Preserve each attempt as a separate immutable history entry.
- A failed backup exposes **Retry** in the same monitor. Retry creates a new job using the same client, policy/version, source, engine, and effective destination; it must not reset or rewrite the failed attempt. Require the client and destination to be available before dispatch, and show the new attempt linked to the original.
- Distinguish backup execution success from post-backup retention/maintenance warnings. Show warnings as warnings; do not conceal failed backup data transfers behind a successful status.

### Restore browsing and recovery

- Restore starts with a registered client, then lists that client's available restore points newest-first. Each entry shows backup timestamp, associated policy/version, source, job status, engine, destination/repository, and retention/hold/expiry state.
- Selecting a restore point opens a navigable hierarchy of folders and files. Where application-aware backup providers exist, show supported applications/components as a separate browseable restore view; do not label arbitrary filesystem folders as applications.
- Allow selection of files/folders (and supported application components) for restore to the original path or a redirected path on the source or another compatible registered client. Show the resolved destination before queueing.
- Preserve existing destination parents, recreate missing directories, and protect existing files unless overwrite is explicitly approved. Restore supported ACLs/permissions and metadata, and report unsupported or skipped metadata.
- Track restore jobs in the same Job Monitor, with detailed logs, destination, progress, and retry/resume behavior appropriate to the operation. Do not retry a restore blindly if it could overwrite data; revalidate the target and require renewed confirmation where needed.

### Login

- Local username/password login for the lab.
- Expiring sessions, CSRF protection, sign-out, and clear expired-session handling.
- Later: LDAP/Active Directory and SSO, user lifecycle, roles, and MFA.

### Protection

- Inventory of clients, OS/agent version, online status, last heartbeat, and assigned policy.
- Enroll, disable, rename, update, and inspect clients.
- Show recent backup runs, progress, result, duration, bytes read/transferred, and error details.
- Queue an ad hoc backup or run a policy on demand.
- Provide agent health and required agent update information.

### Policies

- Define included paths, exclusions, schedule, backup type, destination, consistency requirements, and encryption/compression settings.
- Assign policies to one or more clients and show effective configuration.
- Version policy changes so old restore points retain their original context.
- Validate source paths against the selected OS and show the agent's access limitations.
- Treat a policy as a reusable protection group. A client can be assigned to a policy, and the console shows both membership and the effective settings.
- Allow a storage destination on the policy as the default and a client-level destination override. Precedence is client override → policy destination → system default; show which setting supplied the effective destination.
- Capture the policy version and resolved destination on each job and restore point so edits do not rewrite history.
- Retention is policy-scoped and expressed as keep counts for latest, daily, weekly, monthly, and yearly restore points. Policy create/edit/detail pages must let an authorized operator view and change these values, show the effective rule and policy version, and explain when a change takes effect. A Kopia policy run applies the configured counts; the UI must not imply that saving a policy alone immediately expires repository data.
- Each Kopia snapshot reference records its client, source path, job, and policy version, while the repository remains authoritative for snapshot content. The console catalogs active/expired state from the agent's complete `snapshot list --show-identical` response. Report expiry separately from physical storage reclamation, which Kopia maintenance performs.

### Retention

- Provide a dedicated **Retention** page and policy-level retention editor. For each policy show the configured latest, hourly, daily, weekly, monthly, and yearly keep counts supported by the selected engine, last aging run, next scheduled aging run, and any warnings. Make unsupported settings unavailable with an explanation instead of silently ignoring them.
- Support changing a policy rule with a preview of the affected restore points: which remain, which become eligible for expiration, and the expected effective date. Require an explicit confirmation before applying the change; record the previous/new rule, operator, timestamp, and affected policy version in the audit log.
- Make aging/expiration a visible, persistent operation independent of taking a new backup. Provide **Run aging now**, scheduled aging, job status, progress/result logs, retry for safe failed aging operations, and the last successful execution time. A restart must not lose a queued or running aging operation.
- Provide snapshot-level actions on the Snapshots page: set an expiry date, clear an override to return to policy retention, and place/remove an authorized retention hold (where supported). Show policy-derived expiry and any override/hold separately. A hold prevents expiration until released.
- Before an operator applies an immediate expiry or policy change, preview the exact snapshots affected and request confirmation. Prevent expiry of the only recovery point for a protected client unless the operator explicitly acknowledges the risk.
- Apply expiration through the selected repository engine's supported lifecycle API. Catalog status must not be changed to expired/deleted until the repository confirms the operation; reconcile repository and catalog after interrupted operations.
- Separate snapshot expiration from physical chunk reclamation. Reclaim deduplicated chunks only through safe repository maintenance after proving they are no longer referenced by any retained snapshot. Show maintenance status and reclaimed-space results separately from aging.
- Keep audit history and restore-point metadata after expiry according to the configured catalog/audit retention. Clearly distinguish active, expiration-pending, expired, held, and repository-maintenance states.
- Legal hold/immutability and compliance lock remain later enterprise capabilities; the initial retention hold is an application-level protection and must not be presented as immutable storage.

### Restore

- Select a source client, then choose among its restore points by creation time.
- Browse the saved directory tree and select individual files for recovery.
- Select any registered, compatible destination client; restore selected files to their recorded original paths or into a new folder.
- Require explicit confirmation when restoring to original paths may overwrite existing files, and report progress/results in Activity.
- Keep full-machine/BMR recovery as a separate workflow with bootable recovery media and system/volume metadata.

### Media

- Provide a dedicated **Media Targets** page, separate from policy and restore-point pages. List each configured target with name, storage type, media server/repository, health, capacity, encryption/compression/deduplication status, and last successful read/write.
- Provide a GUI workflow to add, edit, test, enable, and disable a target. The initial target type is **filesystem** on a media server. The form collects a display name, media server, filesystem/repository path, and encryption configuration; validate path reachability, permissions, available space, and repository compatibility before allowing policies to use it.
- Encryption must be enabled by default for a new target. The GUI may accept an encryption password/key during secure repository setup, but must never return it to the browser after submission, place it in ordinary catalog rows, logs, job details, policy JSON, or agent-visible configuration. Store/retrieve secrets through a protected secret mechanism, with a documented backup and recovery procedure. Display only encryption mode and a non-secret key/credential reference.
- For an already initialized repository, require the operator to provide or select its credential securely and validate it before marking the target usable. Never silently create a second repository over an existing path or overwrite repository contents.
- Support policy-level default destination and client-level override using the same configured target records. Show the resolved target and the setting that supplied it on policy, client, job, and restore-point details.
- Show target test results and actionable errors without exposing secrets. Do not mark a target healthy based only on a running process; perform a non-destructive read/write validation where supported.
- Keep filesystem targets as the first implementation. Model target providers so NFS/SMB and Kopia-supported cloud destinations can be added later. Replication is a separate configured and monitored workflow, not an implicit second write destination for each backup.
- Do not expose raw encryption keys in the browser or store backup file payloads in the catalog.

### Snapshot inventory and restore-point discovery

- Provide a dedicated **Snapshots / Restore Points** page showing restore points indexed from every successful policy or ad hoc Kopia backup, including client, policy (or ad hoc label), source, timestamp, snapshot ID, engine, destination, size/file summary, job status, and retention/hold/expiry state where known.
- A successful Kopia job must not appear as successful in Activity while its snapshot is absent from the catalog. If catalog indexing fails after the repository snapshot succeeds, report a catalog/indexing warning or failure clearly and provide a safe refresh/reconciliation action.
- Empty states must distinguish “no snapshots have been created yet” from “snapshot index is unavailable or has not refreshed.” Include the latest sync/index time and an action to refresh/reconcile from configured repositories.
- Support filters for client, policy, destination, time range, and active/expired state. Selecting a restore point opens the corresponding guided Restore workflow.

### Activity and alerts

- Fleet-wide job history, filterable by client, policy, state, and time.
- Retry/cancel where safe, with audit history for administrator actions.
- Alert on failed jobs, offline agents, low repository space, and catalog/media health problems.

## Catalog data model

The catalog should store metadata and references, not backup file payloads. At minimum, normalize these entities as the system grows:

- users, roles, sessions, and audit events;
- clients, agent credentials, agent capabilities, and heartbeat state;
- versioned policies and client/policy assignments;
- media servers and storage destinations;
- policy-to-client memberships, client destination overrides, and replication relationships/status;
- backup jobs/runs and their states/log summaries;
- restore points/snapshots and the policy/configuration used, with per-snapshot run association/counting, retention override, hold, and expiry state;
- indexed file entries (path, type, size, timestamps, permissions/ACL metadata, checksum, and restore-point membership);
- chunk references and repository object identifiers;
- retention rules, expiry/hold state, and chunk garbage-collection state.

Kopia is authoritative for each snapshot's directory tree, file metadata, chunk references, and object lifecycle. PostgreSQL should index only the product's required snapshot references and operational metadata; do not duplicate Kopia's file manifest or track chunk ownership in custom tables. If a searchable file catalog is added later, derive it from Kopia's supported APIs and preserve Kopia as the restore source of truth.

### Storage destinations and replication

- Model destinations independently from media-server addresses, with provider type, repository/security domain, endpoint reference, health, and capacity. Keep credentials in protected configuration or a secret store, not policy JSON or job output.
- Start with the lab filesystem/media repository. Add cloud providers later through Kopia-supported storage adapters without changing client/policy relationships.
- Replication is a separate scheduled and monitored operation, with its own status, lag, last success, credentials, and recovery verification. Kopia supports incremental repository `sync-to` for filesystem and cloud targets, but its documentation says one snapshot is not written directly to multiple repositories in a single snapshot operation. Do not represent this as synchronous multi-target backup.
- Default replication to non-destructive behavior; deleting from the source must not silently delete the replica. Preserve repository encryption boundaries and test restores from the replica.

## OS and recovery requirements

### Linux filesystem backup

- Path filtering, permissions/ownership, timestamps, symlinks policy, sparse files, hard links, ACLs, and extended attributes need explicit product behavior. Restore into an existing destination parent must create missing directories and restore selected files without replacing existing files unless the operator explicitly authorizes overwrite. ACLs and supported metadata must be captured at backup time and applied on restore; unsupported metadata must be reported clearly rather than silently dropped.
- Non-fatal skips (for example, sockets/devices, excluded paths, configured link skips, or files that cannot be read because they are open/locked or permission-denied) should produce a successful-with-warnings job when at least one eligible file is protected. Record a bounded, actionable warning summary and skipped paths in job details; continue processing other files. Fail the job for repository, authentication, integrity, or catalog commit errors, and fail rather than report success if no eligible files were protected.
- Failed backup jobs must offer a retry action from Activity. A retry creates a new job using the same client, engine, and source path; it does not erase or mutate the failed attempt. The agent must retry transient chunk/network failures with bounded backoff before marking the job failed.
- Applications that change during a backup need a snapshot/consistency strategy or a clear warning about crash-consistent results.

### Windows filesystem backup

- A Windows agent/service, NTFS metadata handling, ACLs, reparse points, and VSS snapshots are required for reliable live-system file backup.
- Apply the same warning-versus-failure contract on Windows: report and continue for configured skips or files unavailable to the selected capture mode; use VSS where required for open-file consistency, and clearly identify when a file was skipped or captured without application consistency.
- VSS writer integration is needed for application-aware snapshots such as SQL Server; generic file copying does not provide database consistency.
- Windows BMR additionally needs volume/partition/boot configuration and tested recovery media. It is not achieved by restoring files alone.

### Virtual machines and platforms

- Hyper-V, VMware, and OpenStack are separate integrations with their own snapshot/consistency and inventory APIs. Keep a provider interface in the architecture, but finish and validate the filesystem workflow first.

## Current MVP status (2026-11-11)

| Capability | Current state |
| --- | --- |
| React/Vite/TypeScript console with Protection, Policies, Retention, Restore, Media, and Activity navigation | Implemented in workspace; frontend build/typecheck and Ubuntu image deployment still need verification. |
| Local browser login | Implemented. |
| Client enrollment and inventory | Implemented for the current Linux agent. |
| GUI immediate backup/restore queue | Implemented in workspace with an agent polling service; requires deployment and end-to-end verification. |
| Data engine | Kopia is the only filesystem engine in the product path. Its deduplication, compression, encryption, changed-file restore, and verification were manually exercised in the separate lab repository. The Go agent invokes the installed Kopia CLI for Linux backup, restore, and retention; the latest source changes still need build/deployment verification. |
| File restore-point catalog | PostgreSQL stores Kopia snapshot references and business metadata; Kopia remains authoritative for snapshot trees and file content. |
| Restore | Kopia full-snapshot redirected restore is the only implemented restore path in the current source. Per-file browsing, selective restore, and original-path restore remain future work. |
| Live client filesystem picker | Not implemented; the current Protection view requires an operator to type the source path on the client. |
| Directory selection as a single recursive selection | Not implemented; files are selected individually from each folder view. |
| Policy groups | Workspace code creates policies, assigns Linux clients, and queues manual or interval-scheduled policy runs with the policy version and retention settings. The control plane persists next-run times and avoids duplicate queued/running jobs per client and policy. Policy editing/version changes, destination choice/override, cron schedules, and deployment verification remain. |
| Retention and snapshot aging | Workspace code applies latest/daily/weekly/monthly/yearly counts and requests Kopia expiration after a policy backup. It refreshes the catalog using all snapshots, including identical snapshots, and avoids aging catalog entries if that refresh does not include the new point. This is not built/deployed or lab-verified. Expiration preview, **Run aging now**, scheduled aging independent of backup runs, per-snapshot expiry overrides/holds, and safe repository-maintenance controls are not implemented. |
| Kopia snapshot inventory and restore | Workspace code records successful Kopia snapshot IDs/root IDs and client/source/job/policy links, shows active/expired points, and queues full-snapshot redirected restore to a new child folder under an existing parent path on a registered Linux client. Backup warnings summarize incomplete scans, read errors, excluded entries, and symbolic links. It is not built/deployed or lab-verified; Kopia folder/file browsing and selective restore remain incomplete. |
| Multiple media destinations/capacity dashboard | Not implemented; only one URL is configured. |
| GUI-managed filesystem media targets and encryption setup | Not implemented; the current Media page reports one configured service and does not create/manage destinations. |
| Dedicated snapshot inventory page with index health/refresh state | Not implemented as a separate page; snapshot visibility remains limited to current policy/restore views. |
| Windows filesystem agent/VSS | Not implemented. |
| BMR | Not implemented. |
| Production security/operations | TLS, role management, key management integration, resilient job recovery, database backup/restore, and monitoring are still required. |

## Recommended build order

1. Deploy the Kopia-only control-plane and agent to a fresh test catalog and Kopia repository; verify policy backup, restore, and retention operations.
2. Verify policy backup/restore and retention end-to-end on disposable Linux data; add retention dry-run and safe expiry preview before regular use.
3. Complete snapshot file browsing/selective restore, restore metadata/ACL behavior, job diagnostics, and repository health/verification views using Kopia capabilities.
4. Add Windows agent/service support using Kopia and validate VSS, NTFS ACLs, reparse points, and application consistency.
5. Add repository targets, capacity reporting, and monitored Kopia `repository sync-to` replication.
6. Build BMR separately, then add VMware, Hyper-V, and OpenStack integrations as provider-specific projects.
7. Harden TLS, identity/roles, repository isolation, key recovery, database disaster recovery, audit, monitoring, and operations before production or commercial use.

