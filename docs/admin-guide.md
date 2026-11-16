# Administrator Guide (Kopia Lab)

This is a disposable Linux test deployment. Kopia is the only filesystem backup engine. PostgreSQL stores product metadata (users, clients, policies, jobs, and snapshot references); the Kopia repository is authoritative for snapshot data and file trees. Existing prototype backup data is not migrated.

## Components

| Component | Responsibility | Lab address |
| --- | --- | --- |
| PrimaryServer | React console, Go control plane, PostgreSQL catalog | `192.168.56.105:8080` |
| MediaServer | Kopia repository server and encrypted repository files | `192.168.56.107:51515` |
| Linux clients | Root or service account runs the agent and Kopia CLI | Host-only network |

The Media page's endpoint check verifies TCP reachability only. Use Kopia's CLI to check repository authentication, maintenance, and integrity.

## Fresh test deployment

Before using this source revision, copy it to PrimaryServer. To discard the old test catalog—including local users, registered clients, policies, jobs, and snapshot references—and initialize the new schema, run on PrimaryServer:

```bash
cd ~/backup-platform
sudo docker compose --env-file deploy/lab/.env -f deploy/lab/compose.yaml down -v
sudo docker compose --env-file deploy/lab/.env -f deploy/lab/compose.yaml up --build -d
sudo docker compose --env-file deploy/lab/.env -f deploy/lab/compose.yaml ps
curl -fsS http://127.0.0.1:8080/healthz
```

`down -v` permanently deletes this Compose project's PostgreSQL volume. The administrator account is created from `BOOTSTRAP_ADMIN_USERNAME` and `BOOTSTRAP_ADMIN_PASSWORD` in the private `.env`; keep that file mode `600`. Do not run this on any environment whose catalog data must be kept.

For a completely empty media repository, initialize a **new empty repository directory** on MediaServer and point its Kopia service at it. Keep the existing repository untouched until you have confirmed the new service can connect and restore a disposable snapshot. Do not put repository passwords in the control-plane database, job arguments, screenshots, or chat.

## Console workflow

Open `http://192.168.56.105:8080` and sign in with the bootstrap administrator credentials.

1. **Protection / Services:** create a short-lived enrollment token and use it to enroll a Linux agent. Tokens are single use; treat them as passwords.
2. **Policies:** define a policy name, absolute Linux source path, assigned clients, schedule interval, and Kopia retention counts. The current build uses one configured Kopia repository endpoint; repository selection and destination overrides are not yet available in the UI.
3. **Run now:** submit a policy job. The online client agent claims it; offline clients keep queued jobs until they reconnect.
4. **Activity:** refresh to see queued/running/succeeded/failed jobs. Open **Details** for captured logs. Failed backup jobs can be retried; retries create a new activity record.
5. **Restore:** select an active Kopia snapshot, a registered Linux destination client, and an existing parent folder. The agent restores the full snapshot into a newly created child directory. The destination itself must not already exist.
6. **Retention:** review configured policy counts and request aging. Kopia expiration can make snapshots unavailable; repository maintenance separately reclaims unreferenced storage.

Backup and restore paths refer to paths on the selected client, not the browser PC. This build does not provide per-file browsing, selective restore, or original-path overwrite restore.

## Install or refresh a Linux agent

The control-plane image includes a Linux agent binary. After rebuilding it on PrimaryServer, install the matching binary on the client and point its Kopia CLI configuration at the Kopia repository server. The Kopia configuration and repository identity must belong to the same OS account that runs the agent service (the current lab service runs as root):

```bash
sudo docker cp enterprise-backup-lab-controlplane-1:/app/backup-agent /tmp/backup-agent
sudo install -o root -g root -m 0755 /tmp/backup-agent /usr/local/bin/backup-agent
sudo systemctl restart backup-agent.service
sudo systemctl status backup-agent.service --no-pager -l
```

Use the console to create an enrollment token. On the client, write it to a root-only file without adding it to shell history, then enroll:

```bash
sudo install -d -o root -g root -m 0700 /etc/backup-agent
sudo sh -c 'umask 077; cat > /etc/backup-agent/enrollment-token'
# Paste the token, press Ctrl+D, then:
sudo /usr/local/bin/backup-agent enroll \
  --server http://192.168.56.105:8080 \
  --token-file /etc/backup-agent/enrollment-token \
  --name CLIENT-NAME \
  --state-file /etc/backup-agent/state.json
sudo rm -f /etc/backup-agent/enrollment-token
sudo systemctl restart backup-agent.service
```

The credential file must stay root-only. Check service logs with `sudo journalctl -u backup-agent.service -f`. Ensure Kopia is installed and the root account's Kopia repository connection is configured before requesting jobs.

## Kopia checks on the client

Run these as the account used by `backup-agent` (root in this lab):

```bash
sudo -H kopia repository status
sudo -H kopia snapshot list --all
sudo -H kopia content stats
```

Use Kopia's documented repository validation, snapshot verification, maintenance, and recovery procedures for the pinned version. Protect the repository password/certificate identity separately and rehearse recovery using disposable data.

## Current scope limits

- Linux filesystem backup only; Windows service/VSS, BMR, and hypervisor APIs are future work.
- Full-snapshot redirected restore only; file browsing, selective restore, and original-path restore are not yet implemented.
- One configured Kopia endpoint; GUI-managed media targets, cloud destinations, and replication are future work.
- Retention preview/holds, repository-health metrics, production TLS, key-management integration, and disaster-recovery automation are not complete.
