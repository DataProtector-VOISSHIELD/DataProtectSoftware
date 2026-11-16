import { useEffect, useMemo, useState } from 'react'
import type { FormEvent } from 'react'

type Client = { id: string; name: string; os: string; version: string; last_seen: string }
type PolicyAssignment = { client_id: string; client_name: string; source_path: string; exclude_patterns: string[] }
type Job = { id: string; client_id: string; client: string; kind: string; engine: string; source_path: string; status: 'queued' | 'running' | 'succeeded' | 'failed'; created_at: string; message: string }
type JobDetails = Job & { logs: string; policy_id?: string; policy_version: number; started_at?: string; finished_at?: string }
type Policy = { id: string; name: string; engine: string; source_path: string; exclude_patterns: string[]; assignments: PolicyAssignment[]; client_ids: string[]; client_names: string[]; destination: string; version: number; keep_last: number; keep_daily: number; keep_weekly: number; keep_monthly: number; keep_yearly: number; schedule_interval_minutes: number; run_count: number; last_run_at?: string; next_run_at?: string }
type Snapshot = { id: string; client_id: string; client: string; policy_id: string; policy_name: string; policy_version: number; job_id: string; source_path: string; root_object_id: string; created_at: string; expired: boolean }
type MediaHealth = { url: string; status: 'reachable' | 'unreachable'; checked_at: string }
type Section = 'dashboard' | 'clients' | 'plans' | 'storage' | 'retention' | 'jobs'
type Dashboard = { username: string; csrf_token: string; clients: Client[]; jobs: Job[]; policies: Policy[]; snapshots: Snapshot[]; media_url: string }
type APIError = Error & { status?: number }

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', ...init })
  if (!response.ok) {
    const message = (await response.text()).trim()
    const error = new Error(message || `Request failed (${response.status})`) as APIError
    error.status = response.status
    throw error
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

function scheduleLabel(minutes: number): string {
  if (minutes === 0) return 'Manual only'
  if (minutes % 1440 === 0) return `Every ${minutes / 1440} day${minutes === 1440 ? '' : 's'}`
  if (minutes % 60 === 0) return `Every ${minutes / 60} hour${minutes === 60 ? '' : 's'}`
  return `Every ${minutes} minutes`
}

function App() {
  const [data, setData] = useState<Dashboard | null>(null)
  const [loginCSRF, setLoginCSRF] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [token, setToken] = useState('')
  const [section, setSection] = useState<Section>('dashboard')
  const [kopiaRestoreID, setKopiaRestoreID] = useState('')
  const [kopiaRestoreClient, setKopiaRestoreClient] = useState('')
  const [kopiaRestoreTarget, setKopiaRestoreTarget] = useState('')
  const [mediaHealth, setMediaHealth] = useState<MediaHealth | null>(null)
  const [mediaChecking, setMediaChecking] = useState(false)
  const [selectedJobID, setSelectedJobID] = useState('')
  const [jobDetails, setJobDetails] = useState<JobDetails | null>(null)
  const [jobDetailsLoading, setJobDetailsLoading] = useState(false)
  const [jobDetailsError, setJobDetailsError] = useState('')
  const [editingPolicy, setEditingPolicy] = useState<Policy | null>(null)

  const refresh = async () => {
    setError('')
    try { setData(await request<Dashboard>('/api/v1/admin/dashboard', { cache: 'no-store' })) }
    catch (reason) {
      const apiError = reason as APIError
      if (apiError.status === 401) {
        setData(null)
        try { setLoginCSRF((await request<{ csrf_token: string }>('/api/v1/auth/csrf')).csrf_token) }
        catch (csrfError) { setError((csrfError as Error).message) }
      } else setError(apiError.message)
    } finally { setLoading(false) }
  }

  useEffect(() => { void refresh() }, [])

  const stats = useMemo(() => ({
    clients: data?.clients.length ?? 0,
    backups: data?.snapshots.filter((snapshot) => !snapshot.expired).length ?? 0,
    active: data?.jobs.filter((job) => job.status === 'running' || job.status === 'queued').length ?? 0,
  }), [data])

  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setError('')
    const form = new FormData(event.currentTarget)
    try {
      await request('/api/v1/auth/login', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': loginCSRF },
        body: JSON.stringify({ username: form.get('username'), password: form.get('password') }),
      })
      await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function createToken() {
    if (!data) return
    setBusy(true); setError(''); setToken('')
    try {
      const result = await request<{ token: string }>('/api/v1/admin/enrollment-tokens', {
        method: 'POST', headers: { 'X-CSRF-Token': data.csrf_token },
      })
      setToken(result.token); setNotice('Enrollment token created. It expires in 15 minutes and can be used once.')
      await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function retryBackup(job: Job) {
    if (!data || job.kind !== 'backup' || job.status !== 'failed') return
    setBusy(true); setError(''); setNotice('')
    try {
      await request('/api/v1/admin/jobs', {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': data.csrf_token },
        body: JSON.stringify({ client_id: job.client_id, kind: 'backup', engine: job.engine, source_path: job.source_path }),
      })
      setNotice(`Backup retry queued for ${job.client}. The failed attempt remains in activity history.`)
      await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function openJobDetails(job: Job) {
    setSelectedJobID(job.id); setJobDetails(null); setJobDetailsError(''); setJobDetailsLoading(true)
    try { setJobDetails(await request<JobDetails>(`/api/v1/admin/jobs/${encodeURIComponent(job.id)}`, { cache: 'no-store' })) }
    catch (reason) { setJobDetailsError((reason as Error).message) }
    finally { setJobDetailsLoading(false) }
  }

  async function createPolicy(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!data) return
    const form = new FormData(event.currentTarget)
    const clientIds = form.getAll('assigned_client_ids').map(String)
    const assignments = clientIds.map((clientId) => ({ client_id: clientId, source_path: String(form.get(`source_path_${clientId}`) ?? '').trim(), exclude_patterns: String(form.get(`exclude_patterns_${clientId}`) ?? '').split(/\r?\n/).map((pattern) => pattern.trim()).filter(Boolean) }))
    setBusy(true); setError(''); setNotice('')
    try {
      const payload = JSON.stringify({ name: form.get('name'), engine: 'kopia', clients: assignments, keep_last: Number(form.get('keep_last')), keep_daily: Number(form.get('keep_daily')), keep_weekly: Number(form.get('keep_weekly')), keep_monthly: Number(form.get('keep_monthly')), keep_yearly: Number(form.get('keep_yearly')), schedule_interval_minutes: Number(form.get('schedule_interval_minutes')) })
      if (editingPolicy) {
        await request(`/api/v1/admin/policies/${encodeURIComponent(editingPolicy.id)}`, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': data.csrf_token }, body: payload })
        setNotice('Policy updated as a new version. Existing backup history keeps its original policy version.')
        setEditingPolicy(null)
      } else {
        await request('/api/v1/admin/policies', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': data.csrf_token }, body: payload })
        setNotice('Policy created and assigned to the selected clients.')
        event.currentTarget.reset()
      }
      await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function agePolicy(policy: Policy) {
    if (!data || policy.engine !== 'kopia') return
    const keep = `latest ${policy.keep_last}, daily ${policy.keep_daily}, weekly ${policy.keep_weekly}, monthly ${policy.keep_monthly}, yearly ${policy.keep_yearly}`
    if (!window.confirm(`Run Kopia expiration for policy “${policy.name}” now? This may remove snapshots that are outside the configured retention rule (${keep}).`)) return
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await request<{ clients: number }>(`/api/v1/admin/policies/${encodeURIComponent(policy.id)}/expire`, { method: 'POST', headers: { 'X-CSRF-Token': data.csrf_token } })
      setNotice(`Retention aging queued for ${result.clients} client${result.clients === 1 ? '' : 's'}. Check Activity for each result.`)
      await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function runPolicy(policy: Policy) {
    if (!data) return
    setBusy(true); setError(''); setNotice('')
    try {
      const result = await request<{ clients: number }>(`/api/v1/admin/policies/${encodeURIComponent(policy.id)}/run`, { method: 'POST', headers: { 'X-CSRF-Token': data.csrf_token } })
      setNotice(`Policy queued for ${result.clients} client${result.clients === 1 ? '' : 's'}.`); await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function logout() {
    if (!data) return
    setBusy(true)
    try { await request('/api/v1/auth/logout', { method: 'POST', headers: { 'X-CSRF-Token': data.csrf_token } }); setData(null); await refresh() }
    catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function copyEnrollmentToken() {
    try { await navigator.clipboard.writeText(token); setNotice('Enrollment token copied.') }
    catch { setNotice('Clipboard access is unavailable here. Select and copy the token manually.') }
  }

  async function submitKopiaRestore() {
    if (!data || !kopiaRestoreID || !kopiaRestoreClient || !kopiaRestoreTarget.trim()) return
    setBusy(true); setError(''); setNotice('')
    try {
      await request('/api/v1/admin/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': data.csrf_token }, body: JSON.stringify({ client_id: kopiaRestoreClient, kind: 'restore', engine: 'kopia', backup_id: kopiaRestoreID, target_mode: 'new_folder', target_path: kopiaRestoreTarget.trim() }) })
      setNotice('Kopia restore queued. The backed-up top-level folder will be restored under the selected parent folder on the chosen Linux client.')
      await refresh()
    } catch (reason) { setError((reason as Error).message) }
    finally { setBusy(false) }
  }

  async function checkMedia() {
    setMediaChecking(true); setError('')
    try { setMediaHealth(await request<MediaHealth>('/api/v1/admin/media/health', { cache: 'no-store' })) }
    catch (reason) { setError((reason as Error).message) }
    finally { setMediaChecking(false) }
  }

  if (loading) return <div className="loading-screen"><img className="loading-logo" src="/vois-datashield-logo.png" alt="VOIS DataShield" /><span>Loading backup console…</span></div>
  if (!data) return <Login onSubmit={login} csrf={loginCSRF} busy={busy} error={error} />

  const online = (lastSeen: string) => lastSeen !== 'Never' && (Date.now() - new Date(lastSeen).getTime()) < 90_000
  const sectionTitles: Record<Section, string> = { dashboard: 'Dashboard', clients: 'Clients', plans: 'Plans & Policies', storage: 'Storage & Copies', retention: 'Retention & Aging', jobs: 'Jobs & Restores' }
  return <div className="app-shell">
    <aside className="sidebar">
      <a className="brand" href="#overview" aria-label="VOIS DataShield home"><img className="brand-logo" src="/vois-datashield-logo.png" alt="VOIS DataShield — VOIS Data Protection Platform" /></a>
      <div className="nav-caption">WORKSPACE</div>
      <nav className="side-nav" aria-label="Main navigation">
        <button className={section === 'dashboard' ? 'active' : ''} onClick={() => setSection('dashboard')}><i>▦</i> Dashboard</button>
        <button className={section === 'clients' ? 'active' : ''} onClick={() => setSection('clients')}><i>⌘</i> Clients</button>
        <button className={section === 'plans' ? 'active' : ''} onClick={() => setSection('plans')}><i>⚙</i> Plans &amp; Policies</button>
        <button className={section === 'storage' ? 'active' : ''} onClick={() => setSection('storage')}><i>◈</i> Storage &amp; Copies</button>
        <button className={section === 'retention' ? 'active' : ''} onClick={() => setSection('retention')}><i>◷</i> Retention &amp; Aging</button>
        <button className={section === 'jobs' ? 'active' : ''} onClick={() => setSection('jobs')}><i>↻</i> Jobs &amp; Restores <b>{stats.active || ''}</b></button>
      </nav>
      <div className="sidebar-bottom"><div className="secure-dot" /> Lab control plane <span>Private network</span></div>
    </aside>

    <main className="main-area">
      <header className="topbar"><div className="crumb">Workspace <span>/</span> {sectionTitles[section]}</div><div className="top-actions"><button className="icon-button" onClick={() => void refresh()} aria-label="Refresh">↻</button><div className="user-chip"><span className="avatar">{data.username.slice(0, 1).toUpperCase()}</span><span>{data.username}<small>Administrator</small></span></div><button className="text-button" onClick={() => void logout()} disabled={busy}>Sign out</button></div></header>

      <div className="content" id="overview">
        <section className="welcome-row"><div><div className="eyebrow">BACKUP OPERATIONS</div><h1>{section === 'dashboard' ? `Good day, ${data.username}` : sectionTitles[section]}</h1><p>{section === 'dashboard' ? 'Review SLA health, recent Linux jobs, and repository capacity.' : section === 'clients' ? 'Manage registered physical Linux servers and their policy save sets.' : section === 'jobs' ? 'Review backup activity, inspect logs, and restore files or snapshots.' : `Manage ${sectionTitles[section].toLowerCase()} for your backup environment.`}</p></div>{section === 'clients' && <button className="button secondary" onClick={() => void createToken()} disabled={busy}>＋ Register client</button>}</section>
        {error && <div className="alert error-alert" role="alert">{error}</div>}
        {notice && <div className="alert success-alert" role="status">{notice}</div>}
        {token && <div className="token-panel"><div><strong>One-time enrollment token</strong><p>Copy it now. It expires in 15 minutes and can only be used once.</p></div><code>{token}</code><button className="button secondary" onClick={() => void copyEnrollmentToken()}>Copy token</button><button className="plain-close" aria-label="Dismiss token" onClick={() => setToken('')}>×</button></div>}

        {(section === 'dashboard' || section === 'clients') && <>
          {section === 'dashboard' && (data.clients.some((client) => !online(client.last_seen)) || data.jobs.some((job) => job.status === 'failed')) && <section className="panel"><div className="section-heading"><div><div className="eyebrow">ATTENTION REQUIRED</div><h2>Alerts</h2></div><span className="count-label">{data.clients.filter((client) => !online(client.last_seen)).length + data.jobs.filter((job) => job.status === 'failed').length} open</span></div>{data.clients.filter((client) => !online(client.last_seen)).length > 0 && <p className="form-hint">{data.clients.filter((client) => !online(client.last_seen)).length} client(s) offline. Queued jobs will wait until the agent reconnects.</p>}{data.jobs.filter((job) => job.status === 'failed').length > 0 && <p className="form-hint">{data.jobs.filter((job) => job.status === 'failed').length} recent job(s) failed. <button className="text-button" onClick={() => setSection('jobs')}>Open Jobs &amp; Restores</button> to inspect logs or retry backups.</p>}</section>}
          {section === 'dashboard' && <section className="stat-grid" aria-label="Backup summary">
            <Stat label="Protected clients" value={stats.clients} note="Registered endpoints" icon="▣" tone="blue" />
            <Stat label="Restore points" value={stats.backups} note="Available in catalog" icon="◷" tone="violet" />
            <Stat label="Jobs in progress" value={stats.active} note="Queued or running" icon="↻" tone="amber" />
            <Stat label="Kopia repository" value={mediaHealth?.status === 'reachable' ? 'Online' : 'Check'} note="Endpoint connectivity" icon="◈" tone="green" />
          </section>}
          {section === 'dashboard' && <section className="panel"><div className="section-heading"><div><div className="eyebrow">OPERATIONS OVERVIEW</div><h2>Recent Linux jobs</h2></div><button className="text-button" onClick={() => setSection('jobs')}>View all jobs</button></div>{data.jobs.length === 0 ? <Empty title="No recent jobs" body="Backup, restore, and aging activity will appear here." /> : <div className="table-wrap"><table><thead><tr><th>Operation</th><th>Client</th><th>Source / target</th><th>Submitted</th><th>Status</th></tr></thead><tbody>{data.jobs.slice(0, 5).map((job) => <tr key={job.id}><td>{job.kind === 'backup' ? 'Backup' : job.kind === 'restore' ? 'Restore' : 'Retention aging'}</td><td>{job.client}</td><td className="file-path">{job.source_path || '—'}</td><td>{new Date(job.created_at).toLocaleString()}</td><td><Status status={job.status} /></td></tr>)}</tbody></table></div>}<div className="notice-card"><strong>Repository capacity</strong><p>Capacity and deduplication telemetry is not yet exposed by the media endpoint. Use Storage &amp; Copies to check connectivity; capacity reporting is a planned integration.</p><button className="button secondary" onClick={() => { setSection('storage'); void checkMedia() }} disabled={mediaChecking}>{mediaChecking ? 'Checking…' : 'Check repository'}</button></div></section>}
          {section === 'clients' && <section className="panel"><div className="section-heading"><div><div className="eyebrow">SOURCE INVENTORY</div><h2>Registered clients</h2></div><span className="count-label">{data.clients.length} clients</span></div><p className="muted">Linux physical servers use the Kopia agent. Windows, VMware, Hyper-V, and OpenStack are planned source types.</p>{data.clients.length === 0 ? <Empty title="No registered clients" body="Create a one-time enrollment token to register your first Linux client." /> : <div className="table-wrap"><table className="protected-clients-table"><thead><tr><th>Client / description</th><th>Policies</th><th>Save sets / protected paths</th><th>Status · last seen</th></tr></thead><tbody>{data.clients.map((client) => { const assignments = data.policies.flatMap((policy) => policy.assignments.filter((assignment) => assignment.client_id === client.id).map((assignment) => ({ policy, assignment }))); return <tr key={client.id}><td><strong>{client.name}</strong><span className="id-text">Physical Linux source · Agent {client.version}</span></td><td>{assignments.length ? assignments.map(({ policy }) => <span className="client-policy-chip" key={policy.id}>{policy.name}</span>) : <span className="muted">No policies assigned</span>}</td><td>{assignments.length ? assignments.map(({ policy, assignment }) => <span className="client-save-set" key={`${policy.id}-${assignment.client_id}`}><strong>{policy.name}:</strong> {assignment.source_path === '/' ? 'All accessible paths (/)' : assignment.source_path}{assignment.exclude_patterns.length > 0 && <small>Excludes: {assignment.exclude_patterns.join(', ')}</small>}</span>) : <span className="muted">Assign this client to a plan and define its save set</span>}</td><td><span className={`status ${online(client.last_seen) ? 'online' : 'offline'}`}><i />{online(client.last_seen) ? 'Online' : 'Offline'}</span><span className="id-text">{client.last_seen === 'Never' ? 'Never seen' : new Date(client.last_seen).toLocaleString()}</span></td></tr> })}</tbody></table></div>}</section>}
        </>}

        {section === 'plans' && <>
          <section className="panel"><div className="section-heading"><div><div className="eyebrow">BACKUP CONFIGURATION</div><h2>{editingPolicy ? `Edit ${editingPolicy.name}` : 'Create protection policy'}</h2></div><span className="count-label">{editingPolicy ? `New version · v${editingPolicy.version + 1}` : 'Versioned · v1'}</span></div>
            <form key={editingPolicy?.id ?? 'new-policy'} className="policy-form" onSubmit={createPolicy}><input type="hidden" name="engine" value="kopia" /><label>Policy name<input name="name" maxLength={128} defaultValue={editingPolicy?.name ?? ''} placeholder="Linux server daily" required /></label><div className="assignment-editor"><strong>Assign clients and define each client’s save set</strong><p className="muted">A client may belong to multiple policies. Its source paths and exclusions are configured separately for each policy assignment.</p>{data.clients.filter((client) => client.os.toLowerCase() === 'linux').length === 0 ? <Empty title="No Linux sources registered" body="Register a Linux source before assigning clients to a policy." /> : <div className="table-wrap"><table className="assignment-table"><thead><tr><th>Include</th><th>Registered source</th><th>Save set / source path</th><th>Exclusions (one per line)</th></tr></thead><tbody>{data.clients.filter((client) => client.os.toLowerCase() === 'linux').map((client) => { const existing = editingPolicy?.assignments.find((assignment) => assignment.client_id === client.id); return <tr key={client.id}><td><input type="checkbox" name="assigned_client_ids" value={client.id} defaultChecked={Boolean(existing)} aria-label={`Assign ${client.name}`} /></td><td><strong>{client.name}</strong><span className="id-text">{client.os} · Agent {client.version}</span></td><td><input name={`source_path_${client.id}`} list="linux-source-options" defaultValue={existing?.source_path ?? ''} placeholder="/home/test or / for all" /><datalist id="linux-source-options"><option value="/">All accessible paths</option><option value="/home">Home directories</option><option value="/etc">System configuration</option><option value="/var/www">Web content</option></datalist><small><code>/</code> includes the root filesystem; pseudo and temporary paths are skipped by default.</small></td><td><textarea name={`exclude_patterns_${client.id}`} rows={3} defaultValue={existing?.exclude_patterns.join('\n') ?? ''} placeholder={'*.tmp\nnode_modules/'} /><small>Relative Kopia patterns for this client only.</small></td></tr> })}</tbody></table></div>}</div><label>Schedule<select name="schedule_interval_minutes" defaultValue={String(editingPolicy?.schedule_interval_minutes ?? 0)}><option value="0">Manual only</option><option value="60">Every hour</option><option value="360">Every 6 hours</option><option value="720">Every 12 hours</option><option value="1440">Daily</option></select><small>Scheduled jobs use the save set configured for each assigned client.</small></label><div className="policy-retention"><strong>Keep restore points</strong><label>Latest<input name="keep_last" type="number" min="0" defaultValue={editingPolicy?.keep_last ?? 10} /></label><label>Daily<input name="keep_daily" type="number" min="0" defaultValue={editingPolicy?.keep_daily ?? 7} /></label><label>Weekly<input name="keep_weekly" type="number" min="0" defaultValue={editingPolicy?.keep_weekly ?? 4} /></label><label>Monthly<input name="keep_monthly" type="number" min="0" defaultValue={editingPolicy?.keep_monthly ?? 12} /></label><label>Yearly<input name="keep_yearly" type="number" min="0" defaultValue={editingPolicy?.keep_yearly ?? 3} /></label><small>Retention counts are shared by all clients assigned to this policy.</small></div><div className="policy-destination"><strong>Backup engine: Kopia</strong><span>{data.media_url}</span><small>Filesystem policies always use Kopia.</small></div><button className="button primary" disabled={busy || data.clients.filter((client) => client.os.toLowerCase() === 'linux').length === 0}>{editingPolicy ? 'Save policy version' : 'Create policy'}</button>{editingPolicy && <button type="button" className="button secondary" onClick={() => setEditingPolicy(null)}>Cancel edit</button>}</form>
          </section>
          <section className="panel"><div className="section-heading"><div><div className="eyebrow">PROTECTION GROUPS</div><h2>Policies and assigned clients</h2></div><span className="count-label">{data.policies.length} policies</span></div>
            {data.policies.length === 0 ? <Empty title="No Kopia policies yet" body="Create a policy and assign one or more clients, each with its own save set." /> : <div className="table-wrap"><table><thead><tr><th>Policy</th><th>Assigned clients and save sets</th><th>Schedule</th><th>Runs</th><th>Last / next run</th><th>Active snapshots</th><th>Retention</th><th>Repository</th><th>Actions</th></tr></thead><tbody>{data.policies.map((policy) => <tr key={policy.id}><td><strong>{policy.name}</strong><span className="id-text">Kopia · Version {policy.version}</span></td><td>{policy.assignments.length ? policy.assignments.map((assignment) => <span className="client-save-set" key={assignment.client_id}><strong>{assignment.client_name}:</strong> {assignment.source_path}{assignment.exclude_patterns.length > 0 && <small>Excludes: {assignment.exclude_patterns.join(', ')}</small>}</span>) : <span>No clients assigned</span>}</td><td>{scheduleLabel(policy.schedule_interval_minutes)}</td><td>{policy.run_count}</td><td><span>Last: {policy.last_run_at ? new Date(policy.last_run_at).toLocaleString() : 'Never'}</span><span className="id-text">Next: {policy.next_run_at ? new Date(policy.next_run_at).toLocaleString() : 'Manual'}</span></td><td>{data.snapshots.filter((snapshot) => snapshot.policy_id === policy.id && !snapshot.expired).length}</td><td>Latest {policy.keep_last} · Daily {policy.keep_daily} · Weekly {policy.keep_weekly} · Monthly {policy.keep_monthly} · Yearly {policy.keep_yearly}</td><td>{policy.destination}</td><td><button className="button secondary" disabled={busy} onClick={() => setEditingPolicy(policy)}>Edit</button> <button className="button secondary" disabled={busy || policy.assignments.length === 0} onClick={() => void runPolicy(policy)}>Run now</button></td></tr>)}</tbody></table></div>}
            <div className="section-heading snapshot-heading"><div><div className="eyebrow">KOPIA RESTORE POINTS</div><h2>Snapshots created by policies</h2></div><span className="count-label">{data.snapshots.length} snapshots</span></div>
            {data.snapshots.length === 0 ? <Empty title="No Kopia snapshots indexed" body="Snapshots will appear here after an agent completes a Kopia policy job." /> : <div className="table-wrap"><table><thead><tr><th>Created</th><th>Client</th><th>Policy</th><th>Source</th><th>Snapshot ID</th><th>State</th><th>Job</th></tr></thead><tbody>{data.snapshots.map((snapshot) => <tr key={snapshot.id}><td>{new Date(snapshot.created_at).toLocaleString()}</td><td>{snapshot.client}</td><td>{snapshot.policy_name || 'Ad hoc'}{snapshot.policy_version ? ` · v${snapshot.policy_version}` : ''}</td><td className="file-path">{snapshot.source_path}</td><td><code className="hash-text">{snapshot.id}</code></td><td><span className={`job-status ${snapshot.expired ? 'failed' : 'succeeded'}`}><i />{snapshot.expired ? 'expired' : 'active'}</span></td><td><code className="hash-text">{snapshot.job_id}</code></td></tr>)}</tbody></table></div>}
          </section>
        </>}

        {section === 'retention' && <section className="panel"><div className="section-heading"><div><div className="eyebrow">LIFECYCLE</div><h2>Kopia policy retention</h2></div><span className="count-label">{data.policies.length} policies</span></div>{data.policies.length === 0 ? <Empty title="No retention policies" body="Create a Kopia protection policy to configure snapshot retention." /> : <div className="table-wrap"><table><thead><tr><th>Policy</th><th>Latest</th><th>Daily</th><th>Weekly</th><th>Monthly</th><th>Yearly</th><th>Action</th></tr></thead><tbody>{data.policies.map((policy) => <tr key={policy.id}><td><strong>{policy.name}</strong><span className="id-text">v{policy.version} · {policy.client_names.join(', ')}</span></td><td>{policy.keep_last}</td><td>{policy.keep_daily}</td><td>{policy.keep_weekly}</td><td>{policy.keep_monthly}</td><td>{policy.keep_yearly}</td><td><button className="button secondary" onClick={() => { setEditingPolicy(policy); setSection('plans') }}>Edit rule</button> <button className="button primary" disabled={busy || policy.client_ids.length === 0} onClick={() => void agePolicy(policy)}>Run aging now</button></td></tr>)}</tbody></table></div>}<div className="notice-card"><strong>Kopia expiration</strong><p>Aging applies the selected policy’s Kopia retention counts and reconciles the catalog after the repository reports its active snapshots. Expiration rules do not immediately reclaim deduplicated storage; Kopia maintenance handles repository cleanup separately.</p></div></section>}

        {section === 'jobs' && <>
          <section className="panel"><div className="section-heading"><div><div className="eyebrow">KOPIA RESTORE</div><h2>Restore a Kopia snapshot</h2></div><span className="count-label">{data.snapshots.length} available</span></div>
            {data.snapshots.filter((snapshot) => !snapshot.expired).length === 0 ? <Empty title="No active Kopia snapshots available" body="Complete a Kopia backup job to populate the snapshot catalog." /> : <div className="restore-picker-grid"><label>Snapshot<select value={kopiaRestoreID} onChange={(event) => { const id = event.target.value; setKopiaRestoreID(id); const item = data.snapshots.find((snapshot) => snapshot.id === id); setKopiaRestoreClient(item?.client_id ?? '') }}><option value="">Choose a snapshot…</option>{data.snapshots.filter((snapshot) => !snapshot.expired).map((snapshot) => <option key={snapshot.id} value={snapshot.id}>{snapshot.client} · {new Date(snapshot.created_at).toLocaleString()} · {snapshot.policy_name || 'Ad hoc'}</option>)}</select></label><label>Destination Linux client<select value={kopiaRestoreClient} onChange={(event) => setKopiaRestoreClient(event.target.value)}><option value="">Choose a client…</option>{data.clients.filter((client) => client.os.toLowerCase() === 'linux').map((client) => <option key={client.id} value={client.id}>{client.name}</option>)}</select></label><label>Existing parent folder<input value={kopiaRestoreTarget} onChange={(event) => setKopiaRestoreTarget(event.target.value)} placeholder="/home/test/restores" /></label><button className="button primary" disabled={busy || !kopiaRestoreID || !kopiaRestoreClient || !kopiaRestoreTarget.trim()} onClick={() => void submitKopiaRestore()}>Restore snapshot</button></div>}
            <p className="muted">Kopia is the only filesystem restore engine. Select the snapshot, destination client, and parent folder. The agent creates the restore destination and reports the outcome in Activity.</p>
          </section>
        </>}

        {section === 'storage' && <section className="panel"><div className="section-heading"><div><div className="eyebrow">KOPIA STORAGE</div><h2>Repository endpoint</h2></div><button className="button secondary" onClick={() => void checkMedia()} disabled={mediaChecking}>{mediaChecking ? 'Checking…' : 'Check endpoint'}</button></div><article className="destination-card"><div className="destination-icon">◈</div><div className="destination-main"><h3>Kopia repository server</h3><p>{data.media_url}</p><div className="destination-facts"><span>Encryption <strong>AES-256-GCM</strong></span><span>Compression <strong>Policy controlled</strong></span><span>Deduplication <strong>Incremental/content based</strong></span></div></div><span className={`job-status ${mediaHealth?.status === 'reachable' ? 'succeeded' : mediaHealth?.status === 'unreachable' ? 'failed' : 'queued'}`}><i />{mediaHealth?.status ?? 'not checked'}</span></article><div className="notice-card"><strong>Data and catalog flow</strong><p>Each client agent connects to this Kopia endpoint and sends encrypted backup data directly to the MediaServer. The PrimaryServer does not proxy backup data. Kopia maintains its repository indexes and snapshot trees with the repository on MediaServer; the PrimaryServer PostgreSQL catalog tracks client, policy, job, and snapshot-reference metadata for the console.</p></div><p className="muted media-footnote">Reachable means the endpoint accepts TCP connections; repository authentication and data integrity need Kopia-specific checks. Each agent must have its own Kopia server identity and a connection configured for the same OS account that runs the agent.</p>{mediaHealth && <p className="form-hint">Last endpoint check: {new Date(mediaHealth.checked_at).toLocaleString()}</p>}</section>}

        {section === 'jobs' && <section className="panel"><div className="section-heading"><div><div className="eyebrow">TASK QUEUE</div><h2>Recent activity</h2></div><button className="text-button" onClick={() => void refresh()}>Refresh</button></div>
          {data.jobs.length === 0 ? <Empty title="No Kopia jobs yet" body="Kopia backup and restore operations will appear here." /> : <div className="table-wrap"><table><thead><tr><th>Activity ID</th><th>Operation</th><th>Engine</th><th>Client</th><th>Source / target</th><th>Submitted</th><th>Status</th><th>Summary</th><th>Action</th></tr></thead><tbody>{data.jobs.map((job) => <tr key={job.id}><td><button className="job-id-button" title={`Open details for ${job.id}`} onClick={() => void openJobDetails(job)} onDoubleClick={() => void openJobDetails(job)}>{job.id.slice(0, 12)}…</button></td><td><span className="kind-icon">{job.kind === 'backup' ? '↑' : job.kind === 'restore' ? '↓' : '◷'}</span>{job.kind === 'backup' ? 'Backup' : job.kind === 'restore' ? 'Restore' : 'Retention aging'}</td><td>Kopia</td><td>{job.client}</td><td className="file-path">{job.source_path || '—'}</td><td>{new Date(job.created_at).toLocaleString()}</td><td><Status status={job.status} /></td><td className="detail-cell">{job.message || '—'}</td><td className="activity-actions"><button className="button secondary" onClick={() => void openJobDetails(job)}>Details</button>{job.kind === 'backup' && job.status === 'failed' && <button className="button secondary" disabled={busy} onClick={() => void retryBackup(job)}>Retry</button>}</td></tr>)}</tbody></table></div>}
        </section>}
        {selectedJobID && <div className="dialog-backdrop" onClick={() => setSelectedJobID('')}><section className="job-detail-dialog" role="dialog" aria-modal="true" aria-labelledby="job-detail-title" onClick={(event) => event.stopPropagation()}><div className="section-heading"><div><div className="eyebrow">ACTIVITY RECORD</div><h2 id="job-detail-title">Job details</h2></div><button className="icon-button" aria-label="Close job details" onClick={() => setSelectedJobID('')}>×</button></div>{jobDetailsLoading ? <div className="empty-state">Loading job details…</div> : jobDetailsError ? <div className="alert error-alert">{jobDetailsError}</div> : jobDetails && <><div className="job-detail-grid"><div><span>Activity ID</span><code>{jobDetails.id}</code></div><div><span>Client</span><strong>{jobDetails.client}</strong></div><div><span>Operation</span><strong>{jobDetails.kind} · {jobDetails.engine}</strong></div><div><span>Status</span><Status status={jobDetails.status} /></div><div><span>Source / target</span><code>{jobDetails.source_path || '—'}</code></div><div><span>Policy</span><strong>{jobDetails.policy_id ? `${jobDetails.policy_id} · v${jobDetails.policy_version}` : '—'}</strong></div><div><span>Submitted</span><strong>{new Date(jobDetails.created_at).toLocaleString()}</strong></div><div><span>Started</span><strong>{jobDetails.started_at ? new Date(jobDetails.started_at).toLocaleString() : '—'}</strong></div><div><span>Finished</span><strong>{jobDetails.finished_at ? new Date(jobDetails.finished_at).toLocaleString() : '—'}</strong></div></div><div className="job-detail-summary"><strong>Summary</strong><p>{jobDetails.message || 'No result summary reported.'}</p></div><div className="job-log-heading"><strong>Agent-reported log</strong><span>Captured when the job finishes</span></div><pre className="job-log">{jobDetails.logs || 'No agent log output was captured for this job.'}</pre></>}</section></div>}
        <footer className="page-footer">Backup Console <span>·</span> Lab build</footer>
      </div>
    </main>
  </div>
}

function Login({ onSubmit, csrf, busy, error }: { onSubmit: (event: FormEvent<HTMLFormElement>) => void; csrf: string; busy: boolean; error: string }) {
  return <main className="login-screen"><div className="login-art"><div className="art-orbit orbit-one" /><div className="art-orbit orbit-two" /><div className="art-lock">◈</div><div className="art-caption">CONFIDENTIALITY · INTEGRITY · RECOVERY</div></div><section className="login-box"><a className="brand login-brand" href="/" aria-label="VOIS DataShield home"><img className="brand-logo" src="/vois-datashield-logo.png" alt="VOIS DataShield — VOIS Data Protection Platform" /></a><div className="eyebrow">ADMINISTRATOR ACCESS</div><h1>Sign in to your console</h1><p className="login-subtitle">Manage protected systems and recovery operations.</p>{error && <div className="alert error-alert">{error}</div>}<form onSubmit={onSubmit} className="login-form"><input type="hidden" name="csrf" value={csrf} /><label htmlFor="username">Username</label><input id="username" name="username" autoComplete="username" required autoFocus /><label htmlFor="password">Password</label><input id="password" name="password" type="password" autoComplete="current-password" required /><button className="button primary login-submit" disabled={busy || !csrf}>{busy ? 'Signing in…' : 'Sign in'}</button></form><div className="login-foot">Local administrator · Private lab access</div></section></main>
}

function Stat({ label, value, note, icon, tone }: { label: string; value: string | number; note: string; icon: string; tone: string }) {
  return <article className="stat-card"><span className={`stat-icon ${tone}`}>{icon}</span><div className="stat-label">{label}</div><div className="stat-value">{value}</div><div className="stat-note">{note}</div></article>
}

function Status({ status }: { status: Job['status'] }) {
  return <span className={`job-status ${status}`}><i />{status}</span>
}

function Empty({ title, body }: { title: string; body: string }) {
  return <div className="empty-state"><span>◌</span><strong>{title}</strong><p>{body}</p></div>
}

export default App
