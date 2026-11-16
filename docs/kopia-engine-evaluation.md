# Kopia engine proof of concept

## Decision

**Decision (2026-11-13): adopt Kopia as the sole filesystem snapshot engine and repository format for the test product.** Keep our Go/React control plane, PostgreSQL business catalog, identity, client inventory, job authorization, audit, and administration UI. The Custom chunk engine is retired from new workflows. Old lab data is disposable and will not be migrated; the test repository can be rebuilt around Kopia.

This remains a lab validation, not a production readiness claim. Kopia cannot read the old custom format, and those test backups are intentionally out of scope. Do not mix the Custom chunk objects with the Kopia repository.

## Target shape

- **Control plane:** our application creates authorized backup and restore jobs and presents client, policy, snapshot, and activity information.
- **Client agent:** a native Linux or Windows service invokes a pinned Kopia client through a small Go adapter. The adapter translates our job model to Kopia operations and records structured results. The initial integration should use the supported CLI with machine-readable output rather than depending on unversioned internal packages.
- **MediaServer:** run Kopia repository server against a new, dedicated repository path or disk. Clients connect to the server; they do not receive filesystem access or backing-store credentials.
- **PostgreSQL:** retain clients, accounts, policy assignments, jobs, status, audit events, and a small snapshot index. Kopia's repository remains the source of truth for its snapshot manifests and file trees.
- **Encryption and storage:** Kopia performs content addressing, deduplication, compression, and encryption as part of its repository pipeline. Encryption occurs before repository storage; the MediaServer stores Kopia repository objects and does not run our current per-chunk encryption API for Kopia data.

Keep Kopia repository credentials and encryption recovery material separate from ordinary job data. Losing the repository password or recovery material can make snapshots unrecoverable. Back up and test the recovery procedure before adding non-disposable data.

## Isolation boundary

Start with one repository for this trusted lab. Before any managed-service or multi-company deployment, create a separate repository and key per customer/security domain unless isolation tests and an explicit threat review justify another design. Kopia's repository-server documentation states that ACLs protect snapshot and policy manifests, but content objects are not subject to the same per-file ACL check when a user knows an object ID. Do not treat a shared repository as a proven tenant boundary.

## Lab proof-of-concept gates

1. Pin a Kopia release and record its version, binary checksum, configuration, and license notices. Do not float to `latest`.
2. Create the lab repository on a dedicated MediaServer path or disk.
3. Enroll the Ubuntu client through the control plane and run a first snapshot through the Kopia repository server over the private lab network.
4. Repeat an unchanged snapshot and inspect Kopia's repository statistics to confirm that it uploads little or no duplicate file content. Modify a small region in a large test file and measure the incremental transfer.
5. Restore one selected file and one nested directory to a fresh destination. Compare bytes and check the metadata Kopia claims to preserve. Test original-path overwrite behavior only with disposable files.
6. Interrupt a snapshot, restart the client and repository server, then run Kopia's repository/snapshot verification and restore the test data.
7. Exercise a retention policy and repository maintenance on disposable snapshots. Confirm that every retained point still restores before and after maintenance.
8. Test repository credentials and snapshot visibility with two lab identities. Document the content-ID/ACL limitation and retain separate repositories as the default boundary.
9. Run the same client workflow on Windows before claiming Windows support. Verify VSS behavior separately; do not assume file snapshot support supplies application consistency.

Validation gate: repeat the applicable checks, byte-verify restores, and prove credential recovery before protecting anything other than disposable test data.

## Lab results (2026-11-11)

- Installed and pinned for this lab at Kopia `0.23.1` on the MediaServer and PrimaryServer.
- Created the filesystem repository at `/srv/kopia-lab/repository`; provider validation passed.
- Started the Kopia repository server on the MediaServer host-only address `192.168.56.107:51515` with TLS certificate pinning and a dedicated repository user. The systemd service uses the `kopia` account and its saved repository credential; do not put repository passwords in chat or logs.
- Set a per-source `zstd` compression policy for `/tmp/kopia-poc`. The first 8.1 MB snapshot uploaded 8.1 MB; an unchanged second snapshot uploaded 0 B. A file restore passed byte comparison. Repository content statistics reported 16.2 MB of zstd content packed into about 1.9 KB for the highly repetitive test data; on-disk repository usage was 256 KB including repository metadata.
- Created a separate 64 MiB random file. Its initial snapshot uploaded 67.1 MB. After overwriting 4 KiB in the middle, the next snapshot uploaded 5.7 MB, demonstrating reuse of unchanged content chunks. Restored the changed snapshot and passed byte comparison; `snapshot verify --verify-files-percent=100` completed successfully for its 67.1 MB file. After restarting the Kopia repository service, both snapshots remained listed and full-file verification passed again.

These are small lab measurements, not throughput or capacity benchmarks. Compression results depend heavily on the source data. The lab still needs credential recovery documentation/testing, interruption/restart testing, retention testing, Windows/VSS testing, and deployment validation of the rebuilt control-plane job/catalog/UI.

## First control-plane adapter increment

The workspace agent invokes Kopia for all service backup, restore, and retention jobs. The console no longer offers an engine choice; policy and ad hoc filesystem jobs are Kopia-only. The custom manifest/chunk API and agent implementation have been removed from source. The latest changes have not yet been rebuilt and deployed to the lab. Start the demo from a fresh catalog and a new Kopia repository; old test data is not migrated.

The current GUI has policy assignment, scheduling, snapshot references, full-snapshot redirected restore, and retention controls. Selective Kopia file browsing, retention dry-run/preview, safe expiry safeguards, and a repository health/usage dashboard remain incomplete. Do not treat the lab workflow as production-ready until those safeguards and verification are in place.

## Scope that remains ours

Kopia is a filesystem backup engine, not our enterprise control plane. We still own the local login and future LDAP/SSO integration, client enrollment, roles, protection-plan assignments, orchestration, fleet views, alerts, audit, catalog backup, and operations. Kopia does not provide bare-metal recovery; BMR remains a separate bootable recovery project. VMware, Hyper-V, and OpenStack are later provider integrations.

## Job, schedule, and monitoring ownership

Use this mapping during the proof of concept and adapter design. Do not report a control-plane job as successful until the Kopia operation has completed and its result has been recorded.

| Capability | Kopia responsibility | Our control-plane responsibility |
| --- | --- | --- |
| Snapshot execution | Read source paths, snapshot, incrementals, repository writes | Authorize and dispatch a client job; associate it with client, protection plan, and requestor |
| Scheduling | Can schedule local snapshot policies for a Kopia installation | Centrally assign schedules across enrolled clients, apply maintenance windows, prevent conflicting jobs, and show next/last run |
| Job state | Operation output and snapshot result | Durable job lifecycle (`queued`, `running`, `succeeded`, `failed`, `cancelled`), timestamps, progress where available, retry policy, and sanitized error details |
| Retention | Snapshot policy and expiration/prune mechanics | Define/assign retention in the UI, preview expiry, authorize cleanup, record audit trail, and ensure cleanup does not conflict with active jobs |
| Repository health | Repository status, maintenance, verification, and storage statistics exposed by supported Kopia commands | Poll/collect health and capacity, alert on failure or low space, and display media-server reachability and last verification time |
| Logs and audit | Engine diagnostics | Correlate engine output to our job ID; retain actionable sanitized logs; audit who requested backup, restore, policy changes, and expiry |
| Restore | Snapshot listing, file browsing and restore execution | Select client and restore point; show path hierarchy; authorize selected files and destination; track and report restore job |
| Recovery | Repository password/key material required to read repository | Document and test protected credential recovery; maintain catalog/repository consistency and independent catalog recovery |

For the first adapter, prefer Kopia's supported CLI with version-pinned, machine-readable output. Persist the Kopia snapshot identifier and summary alongside our job record. Capture stdout/stderr with size limits and secret redaction; never put repository passwords in job arguments, UI errors, or logs. The scheduler proof should initially use our control plane as the sole scheduler so we do not run duplicate jobs from both Kopia and our application.

## References

- [Kopia features](https://kopia.io/docs/features/)
- [Kopia architecture](https://kopia.io/docs/advanced/architecture/)
- [Kopia repository server and ACL behavior](https://kopia.io/docs/repository-server/)
- [Kopia compression](https://kopia.io/docs/advanced/compression/)
- [Kopia installation and platform support](https://kopia.io/docs/installation/)
- [Kopia project license](https://github.com/kopia/kopia)
