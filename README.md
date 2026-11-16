# VOIS DataShield (Kopia-only lab)

VOIS DataShield is an enterprise backup product prototype. The filesystem backup engine is **Kopia**: agents create encrypted snapshots in a Kopia repository; Kopia supplies chunking, incremental transfer, deduplication, compression, repository maintenance, and restore. The Go control plane and React/Vite/TypeScript console own local login, client enrollment, policy groups, scheduling, job orchestration, the PostgreSQL business catalog, and operator workflows.

The product is being rebuilt around Kopia as its only filesystem backup engine. The old custom format and its test data are disposable and are not migrated. The lab currently targets Linux clients; Windows, BMR, and hypervisor integrations are later stages. See [product requirements](docs/product-requirements.md), [roadmap](docs/roadmap.md), and [Kopia integration notes](docs/kopia-engine-evaluation.md).

## Lab components

- **PrimaryServer:** Docker Compose control plane and PostgreSQL catalog.
- **MediaServer:** Kopia repository server with TLS, backed by a persistent filesystem repository.
- **Linux client:** native `backup-agent` service plus a pinned Kopia CLI, configured to connect to the repository server.

Kopia credentials must be configured in the same OS account that runs the agent service. Keep repository passwords and certificate private keys out of the UI, source control, job logs, and chat. Store and test recovery of the repository password separately; losing it can make snapshots unrecoverable.

## Deploy the control plane

1. Copy the repository to PrimaryServer (for example, `~/backup-platform`).
2. Copy `deploy/lab/.env.example` to `deploy/lab/.env`, set unique long random PostgreSQL and bootstrap-admin passwords, and set `KOPIA_REPOSITORY_URL` to the MediaServer Kopia endpoint, such as `https://192.168.56.107:51515`.
3. Keep the environment file private, then run:

   ```bash
   chmod 600 deploy/lab/.env
   sudo docker compose --env-file deploy/lab/.env -f deploy/lab/compose.yaml up --build -d
   sudo docker compose --env-file deploy/lab/.env -f deploy/lab/compose.yaml ps
   curl -fsS http://127.0.0.1:8080/healthz
   ```

4. Open `http://<PrimaryServer-host-only-IP>:8080` and sign in using the bootstrap values.

This remains an HTTP-only private lab deployment; do not expose it to the internet or protect real data with it.

## Configure the Kopia repository server and agent

Use the Kopia repository server deployment documented in [the Kopia integration notes](docs/kopia-engine-evaluation.md). The server should listen on its private lab interface with TLS enabled and use persistent repository storage. The control-plane Media page's endpoint check reports TCP reachability only; verify repository health/authentication using Kopia commands.

On each Linux agent, install the pinned Kopia CLI and connect it to the server using certificate fingerprint verification and the identity assigned to that client. Configure the connection as the account that runs `backup-agent` (the lab service runs as root). Then install the agent binary built into the control-plane image:

```bash
sudo docker cp enterprise-backup-lab-controlplane-1:/app/backup-agent /tmp/backup-agent
sudo install -o root -g root -m 0755 /tmp/backup-agent /usr/local/bin/backup-agent
sudo systemctl restart backup-agent.service
sudo systemctl status backup-agent.service --no-pager
```

Use the console to enroll clients, create Kopia policies, run backups, inspect Activity, and restore snapshots. Validate with disposable data first. Retention expiry may make snapshots unavailable; physical space reclamation is handled separately by Kopia repository maintenance.

## Current gaps

The lab does not yet provide production-grade TLS for the web console, Windows agent/VSS support, a retention dry-run/expiry preview, per-file Kopia browsing and restore, a full repository health/capacity dashboard, BMR, or hypervisor APIs. These are tracked in the product requirements and roadmap. A clean rebuild starts with a fresh PostgreSQL catalog and a new Kopia repository; do not attempt to use the old custom backup data.
