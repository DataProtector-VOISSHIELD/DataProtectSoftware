package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

const sessionDuration = 8 * time.Hour

var errInvalidCredentials = errors.New("invalid username or password")

type Authenticator interface {
	Authenticate(ctx context.Context, username, password string) (string, error)
}

type LocalAuthenticator struct {
	db *sql.DB
}

func (a LocalAuthenticator) Authenticate(ctx context.Context, username, password string) (string, error) {
	var storedHash []byte
	var role string
	err := a.db.QueryRowContext(ctx,
		`SELECT password_hash, role FROM users WHERE username = $1`, username,
	).Scan(&storedHash, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errInvalidCredentials
	}
	if err != nil {
		return "", err
	}
	if bcrypt.CompareHashAndPassword(storedHash, []byte(password)) != nil {
		return "", errInvalidCredentials
	}
	return role, nil
}

type server struct {
	db           *sql.DB
	auth         Authenticator
	secureCookie bool
	mediaURL     string
}

type dashboardClient struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OS       string `json:"os"`
	Version  string `json:"version"`
	LastSeen string `json:"last_seen"`
}

type dashboardJob struct {
	ID        string `json:"id"`
	ClientID  string `json:"client_id"`
	Client    string `json:"client"`
	Kind      string `json:"kind"`
	Engine    string `json:"engine"`
	Source    string `json:"source_path"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Message   string `json:"message"`
}

type jobDetails struct {
	ID string `json:"id"`
	ClientID string `json:"client_id"`
	Client string `json:"client"`
	Kind string `json:"kind"`
	Engine string `json:"engine"`
	Source string `json:"source_path"`
	Status string `json:"status"`
	Message string `json:"message"`
	Logs string `json:"logs"`
	PolicyID string `json:"policy_id,omitempty"`
	PolicyVersion int `json:"policy_version,omitempty"`
	CreatedAt string `json:"created_at"`
	StartedAt string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type dashboardPolicy struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Engine string `json:"engine"`
	Source string `json:"source_path"`
	Excludes []string `json:"exclude_patterns"`
	ClientIDs []string `json:"client_ids"`
	ClientNames []string `json:"client_names"`
	Assignments []policyClientAssignment `json:"assignments"`
	Destination string `json:"destination"`
	Version int `json:"version"`
	KeepLast int `json:"keep_last"`
	KeepDaily int `json:"keep_daily"`
	KeepWeekly int `json:"keep_weekly"`
	KeepMonthly int `json:"keep_monthly"`
	KeepYearly int `json:"keep_yearly"`
	ScheduleIntervalMinutes int `json:"schedule_interval_minutes"`
	RunCount int64 `json:"run_count"`
	LastRunAt string `json:"last_run_at,omitempty"`
	NextRunAt string `json:"next_run_at,omitempty"`
}

type policyClientAssignment struct {
	ClientID string `json:"client_id"`
	ClientName string `json:"client_name"`
	Source string `json:"source_path"`
	Excludes []string `json:"exclude_patterns"`
}

type dashboardSnapshot struct {
	ID string `json:"id"`
	ClientID string `json:"client_id"`
	Client string `json:"client"`
	PolicyID string `json:"policy_id,omitempty"`
	PolicyName string `json:"policy_name,omitempty"`
	PolicyVersion int `json:"policy_version,omitempty"`
	JobID string `json:"job_id"`
	Source string `json:"source_path"`
	RootObjectID string `json:"root_object_id"`
	CreatedAt string `json:"created_at"`
	Expired bool `json:"expired"`
}

type dashboardData struct {
	Username string            `json:"username"`
	CSRF     string            `json:"csrf_token"`
	Clients  []dashboardClient `json:"clients"`
	Jobs     []dashboardJob    `json:"jobs"`
	Policies []dashboardPolicy `json:"policies"`
	Snapshots []dashboardSnapshot `json:"snapshots"`
	MediaURL string            `json:"media_url"`
}

//go:embed all:ui/dist
var embeddedUI embed.FS

type enrollRequest struct {
	EnrollmentToken string `json:"enrollment_token"`
	Name            string `json:"name"`
	OS              string `json:"os"`
	AgentVersion    string `json:"agent_version"`
}

type enrollResponse struct {
	ClientID string `json:"client_id"`
	APIToken string `json:"api_token"`
}

type agentJob struct {
	JobID          string         `json:"job_id"`
	Kind           string         `json:"kind"`
	Engine         string         `json:"engine"`
	Source         string         `json:"source_path,omitempty"`
	BackupID       string         `json:"backup_id,omitempty"`
	Target         string         `json:"target_path,omitempty"`
	TargetMode     string         `json:"target_mode,omitempty"`
	PolicyID       string         `json:"policy_id,omitempty"`
	PolicyVersion  int            `json:"policy_version,omitempty"`
	KeepLast       int            `json:"keep_last,omitempty"`
	KeepDaily      int            `json:"keep_daily,omitempty"`
	KeepWeekly     int            `json:"keep_weekly,omitempty"`
	KeepMonthly    int            `json:"keep_monthly,omitempty"`
	KeepYearly     int            `json:"keep_yearly,omitempty"`
	Excludes       []string       `json:"exclude_patterns,omitempty"`
}

type finishJobRequest struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Logs string `json:"logs"`
	SnapshotID string `json:"snapshot_id"`
	RootObjectID string `json:"root_object_id"`
	ActiveSnapshotIDs []string `json:"active_snapshot_ids"`
}

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open catalog: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := waitForDatabase(ctx, db); err != nil {
		log.Fatalf("connect to catalog: %v", err)
	}
	if err := migrate(ctx, db); err != nil {
		log.Fatalf("initialize catalog: %v", err)
	}
	if err := bootstrapAdmin(ctx, db); err != nil {
		log.Fatalf("bootstrap administrator: %v", err)
	}

	app := &server{
		db:           db,
		auth:         LocalAuthenticator{db: db},
		secureCookie: strings.EqualFold(os.Getenv("COOKIE_SECURE"), "true"),
		mediaURL:     strings.TrimRight(envOr("KOPIA_REPOSITORY_URL", "https://192.168.56.107:51515"), "/"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", app.health)
	mux.HandleFunc("GET /api/v1/auth/csrf", app.authCSRF)
	mux.HandleFunc("POST /api/v1/auth/login", app.apiLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", app.apiLogout)
	mux.HandleFunc("GET /api/v1/admin/dashboard", app.dashboardAPI)
	
	mux.HandleFunc("GET /api/v1/admin/jobs/{jobID}", app.adminJobDetails)
	mux.HandleFunc("GET /api/v1/admin/media/health", app.adminMediaHealth)
	mux.HandleFunc("POST /api/v1/admin/enrollment-tokens", app.createEnrollmentTokenAPI)
	mux.HandleFunc("POST /api/v1/admin/jobs", app.createJob)
	mux.HandleFunc("POST /api/v1/admin/policies", app.createPolicy)
	mux.HandleFunc("PUT /api/v1/admin/policies/{policyID}", app.updatePolicy)
	mux.HandleFunc("POST /api/v1/admin/policies/{policyID}/run", app.runPolicy)
	mux.HandleFunc("POST /api/v1/admin/policies/{policyID}/expire", app.expirePolicy)
	mux.HandleFunc("POST /api/v1/agents/enroll", app.enrollAgent)
	mux.HandleFunc("POST /api/v1/agents/validate", app.validateAgent)
	mux.HandleFunc("POST /api/v1/agents/heartbeat", app.agentHeartbeat)
	mux.HandleFunc("POST /api/v1/agents/jobs/claim", app.claimJob)
	mux.HandleFunc("POST /api/v1/agents/jobs/{jobID}/finish", app.finishJob)
	mux.Handle("GET /", app.uiHandler())

	go app.runPolicyScheduler()
	addr := envOr("LISTEN_ADDR", ":8080")
	log.Printf("control plane listening on %s", addr)
	if err := http.ListenAndServe(addr, securityHeaders(mux)); err != nil {
		log.Fatalf("http server: %v", err)
	}
}

func waitForDatabase(ctx context.Context, db *sql.DB) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			username TEXT PRIMARY KEY,
			password_hash BYTEA NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash BYTEA PRIMARY KEY,
			username TEXT NOT NULL REFERENCES users(username) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at)`,
		`CREATE TABLE IF NOT EXISTS enrollment_tokens (
			token_hash BYTEA PRIMARY KEY,
			issued_by TEXT NOT NULL REFERENCES users(username),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			expires_at TIMESTAMPTZ NOT NULL,
			used_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS clients (
			client_id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			os_name TEXT NOT NULL,
			agent_version TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_seen_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS agent_jobs (
			job_id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK (kind IN ('backup', 'restore', 'retention')),
			engine TEXT NOT NULL DEFAULT 'kopia' CHECK (engine = 'kopia'),
		source_path TEXT NOT NULL DEFAULT '',
			backup_id TEXT NOT NULL DEFAULT '',
			target_path TEXT NOT NULL DEFAULT '',
			target_mode TEXT NOT NULL DEFAULT 'new_folder',
			allow_overwrite BOOLEAN NOT NULL DEFAULT false,
			keep_last INTEGER,
			keep_daily INTEGER,
			keep_weekly INTEGER,
			keep_monthly INTEGER,
			keep_yearly INTEGER,
			status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
			message TEXT NOT NULL DEFAULT '',
			log_text TEXT NOT NULL DEFAULT '',
			created_by TEXT NOT NULL REFERENCES users(username),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			started_at TIMESTAMPTZ,
			finished_at TIMESTAMPTZ
		)`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS media_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_jobs ALTER COLUMN media_url SET DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS agent_jobs_client_created_idx ON agent_jobs(client_id, created_at DESC)`,
		`ALTER TABLE agent_jobs DROP CONSTRAINT IF EXISTS agent_jobs_kind_check`,
		`ALTER TABLE agent_jobs ADD CONSTRAINT agent_jobs_kind_check CHECK (kind IN ('backup', 'restore', 'retention'))`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS target_mode TEXT NOT NULL DEFAULT 'new_folder'`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS engine TEXT NOT NULL DEFAULT 'kopia'`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS allow_overwrite BOOLEAN NOT NULL DEFAULT false`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS log_text TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS started_at TIMESTAMPTZ`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS exclude_patterns JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`CREATE TABLE IF NOT EXISTS client_credentials (
			credential_hash BYTEA PRIMARY KEY,
			client_id TEXT NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			revoked_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS backup_policies (
			policy_id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			engine TEXT NOT NULL CHECK (engine = 'kopia'),
			source_path TEXT NOT NULL,
			version INTEGER NOT NULL DEFAULT 1,
			destination TEXT NOT NULL,
			keep_last INTEGER NOT NULL DEFAULT 10,
			keep_daily INTEGER NOT NULL DEFAULT 7,
			keep_weekly INTEGER NOT NULL DEFAULT 4,
			keep_monthly INTEGER NOT NULL DEFAULT 12,
			keep_yearly INTEGER NOT NULL DEFAULT 3,
			schedule_interval_minutes INTEGER NOT NULL DEFAULT 0,
			next_run_at TIMESTAMPTZ,
			created_by TEXT NOT NULL REFERENCES users(username),
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS policy_clients (
			policy_id TEXT NOT NULL REFERENCES backup_policies(policy_id) ON DELETE CASCADE,
			client_id TEXT NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
			PRIMARY KEY (policy_id, client_id)
		)`,
		`ALTER TABLE policy_clients ADD COLUMN IF NOT EXISTS source_path TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE policy_clients ADD COLUMN IF NOT EXISTS exclude_patterns JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS keep_last INTEGER NOT NULL DEFAULT 10`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS keep_daily INTEGER NOT NULL DEFAULT 7`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS keep_weekly INTEGER NOT NULL DEFAULT 4`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS keep_monthly INTEGER NOT NULL DEFAULT 12`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS keep_yearly INTEGER NOT NULL DEFAULT 3`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS schedule_interval_minutes INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS next_run_at TIMESTAMPTZ`,
		`ALTER TABLE backup_policies ADD COLUMN IF NOT EXISTS exclude_patterns JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`UPDATE policy_clients pc SET source_path=p.source_path, exclude_patterns=p.exclude_patterns FROM backup_policies p WHERE pc.policy_id=p.policy_id AND pc.source_path='' AND p.source_path<>''`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS policy_id TEXT REFERENCES backup_policies(policy_id) ON DELETE SET NULL`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS policy_version INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS keep_last INTEGER`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS keep_daily INTEGER`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS keep_weekly INTEGER`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS keep_monthly INTEGER`,
		`ALTER TABLE agent_jobs ADD COLUMN IF NOT EXISTS keep_yearly INTEGER`,
		`CREATE TABLE IF NOT EXISTS kopia_snapshots (
			snapshot_id TEXT PRIMARY KEY,
			root_object_id TEXT NOT NULL,
			client_id TEXT NOT NULL REFERENCES clients(client_id) ON DELETE CASCADE,
			policy_id TEXT REFERENCES backup_policies(policy_id) ON DELETE SET NULL,
			policy_version INTEGER NOT NULL DEFAULT 0,
			job_id TEXT NOT NULL UNIQUE REFERENCES agent_jobs(job_id) ON DELETE CASCADE,
			source_path TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			expired_at TIMESTAMPTZ
		)`,
		`ALTER TABLE kopia_snapshots ADD COLUMN IF NOT EXISTS expired_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			event_id BIGSERIAL PRIMARY KEY,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			target TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func bootstrapAdmin(ctx context.Context, db *sql.DB) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}

	username := strings.TrimSpace(envOr("BOOTSTRAP_ADMIN_USERNAME", "admin"))
	password := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")
	if username == "" || password == "" {
		return errors.New("no local users exist; set BOOTSTRAP_ADMIN_USERNAME and BOOTSTRAP_ADMIN_PASSWORD for first startup")
	}
	if len(password) > 72 {
		return errors.New("BOOTSTRAP_ADMIN_PASSWORD must be 72 bytes or fewer")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ($1, $2, 'admin')`,
		username, passwordHash,
	); err != nil {
		return err
	}
	log.Printf("created initial local administrator %q; remove the bootstrap password from the deployment environment", username)
	return nil
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *server) uiHandler() http.Handler {
	ui, err := fs.Sub(embeddedUI, "ui/dist")
	if err != nil { panic(err) }
	files := http.FileServer(http.FS(ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if _, err := fs.Stat(ui, clean); err != nil { r.URL.Path = "/" }
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func (s *server) authCSRF(w http.ResponseWriter, r *http.Request) {
	token, err := randomToken()
	if err != nil { http.Error(w, "could not start sign in", http.StatusInternalServerError); return }
	http.SetCookie(w, &http.Cookie{Name: "backup_login_csrf", Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: 300})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"csrf_token": token})
}

func (s *server) apiLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var credentials struct { Username string `json:"username"`; Password string `json:"password"` }
	decoder := json.NewDecoder(r.Body); decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil { http.Error(w, "invalid sign-in request", http.StatusBadRequest); return }
	cookie, err := r.Cookie("backup_login_csrf")
	if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.Header.Get("X-CSRF-Token"))) != 1 { http.Error(w, "sign-in form expired; reload and try again", http.StatusBadRequest); return }
	username := strings.TrimSpace(credentials.Username)
	_, err = s.auth.Authenticate(r.Context(), username, credentials.Password)
	if errors.Is(err, errInvalidCredentials) { http.Error(w, "invalid username or password", http.StatusUnauthorized); return }
	if err != nil { http.Error(w, "authentication service unavailable", http.StatusInternalServerError); return }
	token, err := randomToken()
	if err != nil { http.Error(w, "could not create session", http.StatusInternalServerError); return }
	tokenHash := sha256.Sum256([]byte(token))
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO sessions (token_hash, username, expires_at) VALUES ($1, $2, $3)`, tokenHash[:], username, time.Now().Add(sessionDuration)); err != nil { http.Error(w, "could not create session", http.StatusInternalServerError); return }
	http.SetCookie(w, &http.Cookie{Name: "backup_session", Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, Expires: time.Now().Add(sessionDuration), MaxAge: int(sessionDuration.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: "backup_login_csrf", Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	csrf, err := s.setCSRF(w)
	if err != nil { http.Error(w, "could not create security token", http.StatusInternalServerError); return }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"username": username, "csrf_token": csrf})
}

func (s *server) apiLogout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	if !s.validCSRF(r) { http.Error(w, "request expired; reload and try again", http.StatusBadRequest); return }
	cookie, _ := r.Cookie("backup_session")
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	_, _ = s.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, tokenHash[:])
	for _, name := range []string{"backup_session", "backup_csrf", "backup_login_csrf"} { http.SetCookie(w, &http.Cookie{Name:name, Value:"", Path:"/", HttpOnly:true, Secure:s.secureCookie, SameSite:http.SameSiteStrictMode, MaxAge:-1}) }
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) dashboardAPI(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r)
	if !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	clients, err := s.listClients(r.Context()); if err != nil { http.Error(w, "could not load clients", http.StatusInternalServerError); return }
	jobs, err := s.listDashboardJobs(r.Context()); if err != nil { http.Error(w, "could not load backup jobs", http.StatusInternalServerError); return }
	policies, err := s.listDashboardPolicies(r.Context()); if err != nil { http.Error(w, "could not load backup policies", http.StatusInternalServerError); return }
	snapshots, err := s.listDashboardSnapshots(r.Context()); if err != nil { http.Error(w, "could not load Kopia snapshots", http.StatusInternalServerError); return }
	csrf, err := s.setCSRF(w); if err != nil { http.Error(w, "could not create security token", http.StatusInternalServerError); return }
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(dashboardData{Username:username, CSRF:csrf, Clients:clients, Jobs:jobs, Policies:policies, Snapshots:snapshots, MediaURL:s.mediaURL})
}

func (s *server) adminMediaHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	parsed, parseErr := url.ParseRequestURI(s.mediaURL)
	status := "unreachable"
	if parseErr == nil && parsed.Host != "" {
		connection, dialErr := net.DialTimeout("tcp", parsed.Host, 2*time.Second)
		if dialErr == nil { status = "reachable"; _ = connection.Close() }
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": s.mediaURL, "status": status, "checked_at": time.Now().Format(time.RFC3339)})
}

func (s *server) createEnrollmentTokenAPI(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r)
	if !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	if !s.validCSRF(r) { http.Error(w, "request expired; reload and try again", http.StatusBadRequest); return }
	token, err := randomToken()
	if err != nil { http.Error(w, "could not create enrollment token", http.StatusInternalServerError); return }
	hash := sha256.Sum256([]byte(token))
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO enrollment_tokens (token_hash, issued_by, expires_at) VALUES ($1,$2,now()+interval '15 minutes')`, hash[:], username); err != nil { http.Error(w, "could not save enrollment token", http.StatusInternalServerError); return }
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO audit_events (actor, action, target) VALUES ($1,'enrollment_token_created','agent')`, username)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token, "expires_in": "15 minutes"})
}

func (s *server) listClients(ctx context.Context) ([]dashboardClient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT client_id, name, os_name, agent_version, last_seen_at
		FROM clients ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	clients := make([]dashboardClient, 0)
	for rows.Next() {
		var client dashboardClient
		var lastSeen sql.NullTime
		if err := rows.Scan(&client.ID, &client.Name, &client.OS, &client.Version, &lastSeen); err != nil {
			return nil, err
		}
		client.LastSeen = "Never"
		if lastSeen.Valid {
			client.LastSeen = lastSeen.Time.Format(time.RFC3339)
		}
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

func (s *server) listDashboardPolicies(ctx context.Context) ([]dashboardPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.policy_id,p.name,p.engine,p.source_path,p.exclude_patterns,p.destination,p.version,p.keep_last,p.keep_daily,p.keep_weekly,p.keep_monthly,p.keep_yearly,p.schedule_interval_minutes,(SELECT COUNT(*) FROM agent_jobs j WHERE j.policy_id=p.policy_id AND j.kind='backup' AND j.engine='kopia'),(SELECT MAX(j.created_at) FROM agent_jobs j WHERE j.policy_id=p.policy_id AND j.kind='backup' AND j.engine='kopia'),p.next_run_at,COALESCE(array_agg(pc.client_id ORDER BY c.name) FILTER (WHERE pc.client_id IS NOT NULL),ARRAY[]::text[]),COALESCE(array_agg(c.name ORDER BY c.name) FILTER (WHERE pc.client_id IS NOT NULL),ARRAY[]::text[]),COALESCE(jsonb_agg(jsonb_build_object('client_id',pc.client_id,'client_name',c.name,'source_path',pc.source_path,'exclude_patterns',pc.exclude_patterns) ORDER BY c.name) FILTER (WHERE pc.client_id IS NOT NULL),'[]'::jsonb) FROM backup_policies p LEFT JOIN policy_clients pc USING(policy_id) LEFT JOIN clients c USING(client_id) WHERE p.engine='kopia' GROUP BY p.policy_id ORDER BY p.name`)
	if err != nil { return nil, err }
	defer rows.Close()
	items := make([]dashboardPolicy, 0)
	for rows.Next() {
		var item dashboardPolicy
		var lastRun, nextRun sql.NullTime
		var excludes, assignments []byte
		if err := rows.Scan(&item.ID,&item.Name,&item.Engine,&item.Source,&excludes,&item.Destination,&item.Version,&item.KeepLast,&item.KeepDaily,&item.KeepWeekly,&item.KeepMonthly,&item.KeepYearly,&item.ScheduleIntervalMinutes,&item.RunCount,&lastRun,&nextRun,&item.ClientIDs,&item.ClientNames,&assignments); err != nil { return nil, err }
		if err := json.Unmarshal(excludes, &item.Excludes); err != nil { return nil, err }
		if err := json.Unmarshal(assignments, &item.Assignments); err != nil { return nil, err }
		if lastRun.Valid { item.LastRunAt = lastRun.Time.Local().Format(time.RFC3339) }
		if nextRun.Valid { item.NextRunAt = nextRun.Time.Local().Format(time.RFC3339) }
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *server) listDashboardSnapshots(ctx context.Context) ([]dashboardSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT k.snapshot_id,k.client_id,c.name,COALESCE(k.policy_id,''),COALESCE(p.name,''),k.policy_version,k.job_id,k.source_path,k.root_object_id,k.created_at,(k.expired_at IS NOT NULL) FROM kopia_snapshots k JOIN clients c USING(client_id) LEFT JOIN backup_policies p USING(policy_id) ORDER BY k.created_at DESC LIMIT 500`)
	if err != nil { return nil, err }
	defer rows.Close()
	items := make([]dashboardSnapshot,0)
	for rows.Next() { var item dashboardSnapshot; var created time.Time; if err := rows.Scan(&item.ID,&item.ClientID,&item.Client,&item.PolicyID,&item.PolicyName,&item.PolicyVersion,&item.JobID,&item.Source,&item.RootObjectID,&created,&item.Expired); err != nil { return nil,err }; item.CreatedAt=created.Local().Format(time.RFC3339); items=append(items,item) }
	return items, rows.Err()
}

func validateExcludePatterns(patterns []string) ([]string, error) {
	if len(patterns) > 100 { return nil, errors.New("no more than 100 exclusion patterns are allowed") }
	clean := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
		if pattern == "" { continue }
		if len(pattern) > 1024 || strings.ContainsRune(pattern, '\x00') || path.IsAbs(pattern) {
			return nil, errors.New("exclusions must be relative Kopia patterns up to 1,024 characters")
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." { return nil, errors.New("exclusion patterns cannot traverse to a parent directory") }
		}
		clean = append(clean, pattern)
	}
	return clean, nil
}

func (s *server) createPolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r); if !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	if !s.validCSRF(r) { http.Error(w, "request expired; reload and try again", http.StatusBadRequest); return }
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var input struct { Name string `json:"name"`; Engine string `json:"engine"`; Clients []policyClientAssignment `json:"clients"`; KeepLast int `json:"keep_last"`; KeepDaily int `json:"keep_daily"`; KeepWeekly int `json:"keep_weekly"`; KeepMonthly int `json:"keep_monthly"`; KeepYearly int `json:"keep_yearly"`; ScheduleIntervalMinutes int `json:"schedule_interval_minutes"` }
	decoder := json.NewDecoder(r.Body); decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil { http.Error(w, "invalid policy request", http.StatusBadRequest); return }
	input.Name = strings.TrimSpace(input.Name); input.Engine = strings.ToLower(strings.TrimSpace(input.Engine))
	if input.Name == "" || len(input.Name) > 128 || (input.Engine != "" && input.Engine != "kopia") || len(input.Clients) == 0 || len(input.Clients) > 500 { http.Error(w, "Kopia policy requires a name and 1–500 client save-set assignments", http.StatusBadRequest); return }
	input.Engine = "kopia"
	seen := map[string]bool{}
	for index := range input.Clients {
		assignment := &input.Clients[index]
		assignment.ClientID = strings.TrimSpace(assignment.ClientID)
		assignment.Source = strings.TrimSpace(assignment.Source)
		if assignment.ClientID == "" || len(assignment.ClientID) > 128 || seen[assignment.ClientID] { http.Error(w, "each client can have only one save-set assignment in a policy", http.StatusBadRequest); return }
		seen[assignment.ClientID] = true
		if assignment.Source == "" || len(assignment.Source) > 4096 || !path.IsAbs(assignment.Source) { http.Error(w, "each client assignment needs an absolute Linux save-set path", http.StatusBadRequest); return }
		validated, err := validateExcludePatterns(assignment.Excludes); if err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }; assignment.Excludes = validated
	}
	if input.KeepLast < 0 || input.KeepDaily < 0 || input.KeepWeekly < 0 || input.KeepMonthly < 0 || input.KeepYearly < 0 || input.KeepLast+input.KeepDaily+input.KeepWeekly+input.KeepMonthly+input.KeepYearly > 10000 { http.Error(w, "retention counts must be non-negative and total no more than 10,000", http.StatusBadRequest); return }
	if input.KeepLast+input.KeepDaily+input.KeepWeekly+input.KeepMonthly+input.KeepYearly == 0 { http.Error(w, "at least one retention count must be greater than zero", http.StatusBadRequest); return }
	if input.ScheduleIntervalMinutes != 0 && (input.ScheduleIntervalMinutes < 5 || input.ScheduleIntervalMinutes > 525600) { http.Error(w, "schedule interval must be manual or between 5 minutes and 365 days", http.StatusBadRequest); return }
	id, err := randomToken(); if err != nil { http.Error(w, "could not create policy", http.StatusInternalServerError); return }
	tx, err := s.db.BeginTx(r.Context(), nil); if err != nil { http.Error(w, "catalog unavailable", http.StatusServiceUnavailable); return }; defer tx.Rollback()
	emptyExcludes := []byte("[]")
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO backup_policies(policy_id,name,engine,source_path,exclude_patterns,destination,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly,schedule_interval_minutes,next_run_at,created_by) VALUES($1,$2,$3,'',$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $11>0 THEN now()+make_interval(mins=>$11) ELSE NULL END,$12)`, id, input.Name, input.Engine, emptyExcludes,s.mediaURL,input.KeepLast,input.KeepDaily,input.KeepWeekly,input.KeepMonthly,input.KeepYearly,input.ScheduleIntervalMinutes,username); err != nil { http.Error(w, "policy name already exists or catalog unavailable", http.StatusConflict); return }
	for _, assignment := range input.Clients {
		var osName string
		if err := tx.QueryRowContext(r.Context(), `SELECT os_name FROM clients WHERE client_id=$1`, assignment.ClientID).Scan(&osName); err != nil { http.Error(w, "one or more selected clients do not exist", http.StatusBadRequest); return }
		if !strings.EqualFold(osName, "linux") { http.Error(w, "Kopia filesystem policies currently support configured Linux clients only", http.StatusBadRequest); return }
		excludesJSON, _ := json.Marshal(assignment.Excludes)
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO policy_clients(policy_id,client_id,source_path,exclude_patterns) VALUES($1,$2,$3,$4)`, id, assignment.ClientID, assignment.Source, excludesJSON); err != nil { http.Error(w, "could not assign policy save set", http.StatusInternalServerError); return }
	}
	if err := tx.Commit(); err != nil { http.Error(w, "could not save policy", http.StatusInternalServerError); return }
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO audit_events(actor,action,target) VALUES($1,'policy_created',$2)`, username, id)
	w.Header().Set("Content-Type", "application/json"); w.WriteHeader(http.StatusCreated); _ = json.NewEncoder(w).Encode(map[string]string{"policy_id":id,"status":"created"})
}

func (s *server) runPolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r); if !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	if !s.validCSRF(r) { http.Error(w, "request expired; reload and try again", http.StatusBadRequest); return }
	var policy dashboardPolicy
	if err := s.db.QueryRowContext(r.Context(), `SELECT policy_id,name,engine,destination,version,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly FROM backup_policies WHERE policy_id=$1`, r.PathValue("policyID")).Scan(&policy.ID,&policy.Name,&policy.Engine,&policy.Destination,&policy.Version,&policy.KeepLast,&policy.KeepDaily,&policy.KeepWeekly,&policy.KeepMonthly,&policy.KeepYearly); err != nil { http.Error(w, "policy not found", http.StatusNotFound); return }
	if policy.Engine != "kopia" { http.Error(w, "this legacy policy must be edited and saved as Kopia before it can run", http.StatusConflict); return }
	rows, err := s.db.QueryContext(r.Context(), `SELECT client_id,source_path,exclude_patterns FROM policy_clients WHERE policy_id=$1 ORDER BY client_id`, policy.ID); if err != nil { http.Error(w, "catalog unavailable", http.StatusServiceUnavailable); return }
	assignments := []policyClientAssignment{}
	for rows.Next() { var assignment policyClientAssignment; var excludes []byte; if err := rows.Scan(&assignment.ClientID,&assignment.Source,&excludes); err != nil { rows.Close(); http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; if err := json.Unmarshal(excludes,&assignment.Excludes); err != nil { rows.Close(); http.Error(w,"client save-set exclusions are invalid",http.StatusInternalServerError); return }; assignments=append(assignments,assignment) }; if err := rows.Err(); err != nil { rows.Close(); http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; rows.Close()
	if len(assignments)==0 { http.Error(w,"assign at least one client before running this policy",http.StatusBadRequest); return }
	tx, err := s.db.BeginTx(r.Context(),nil); if err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; defer tx.Rollback()
	for _, assignment := range assignments { id, err := randomToken(); if err != nil { http.Error(w,"could not queue policy",http.StatusInternalServerError); return }; excludesJSON, _ := json.Marshal(assignment.Excludes); if _, err := tx.ExecContext(r.Context(), `INSERT INTO agent_jobs(job_id,client_id,kind,engine,source_path,exclude_patterns,created_by,policy_id,policy_version,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly) VALUES($1,$2,'backup',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,id,assignment.ClientID,policy.Engine,assignment.Source,excludesJSON,username,policy.ID,policy.Version,policy.KeepLast,policy.KeepDaily,policy.KeepWeekly,policy.KeepMonthly,policy.KeepYearly); err != nil { http.Error(w,"could not queue policy jobs",http.StatusInternalServerError); return } }
	if err := tx.Commit(); err != nil { http.Error(w,"could not queue policy jobs",http.StatusInternalServerError); return }
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO audit_events(actor,action,target) VALUES($1,'policy_run_queued',$2)`,username,policy.ID)
	w.Header().Set("Content-Type","application/json"); w.WriteHeader(http.StatusCreated); _ = json.NewEncoder(w).Encode(map[string]any{"status":"queued","clients":len(assignments)})
}

func (s *server) updatePolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r); if !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	if !s.validCSRF(r) { http.Error(w, "request expired; reload and try again", http.StatusBadRequest); return }
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var input struct { Name string `json:"name"`; Engine string `json:"engine"`; Clients []policyClientAssignment `json:"clients"`; KeepLast int `json:"keep_last"`; KeepDaily int `json:"keep_daily"`; KeepWeekly int `json:"keep_weekly"`; KeepMonthly int `json:"keep_monthly"`; KeepYearly int `json:"keep_yearly"`; ScheduleIntervalMinutes int `json:"schedule_interval_minutes"` }
	decoder := json.NewDecoder(r.Body); decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil { http.Error(w, "invalid policy request", http.StatusBadRequest); return }
	input.Name = strings.TrimSpace(input.Name); input.Engine = strings.ToLower(strings.TrimSpace(input.Engine))
	total := input.KeepLast+input.KeepDaily+input.KeepWeekly+input.KeepMonthly+input.KeepYearly
	if input.Name == "" || len(input.Name) > 128 || (input.Engine != "" && input.Engine != "kopia") || len(input.Clients) == 0 || len(input.Clients) > 500 || total < 1 || total > 10000 { http.Error(w, "Kopia policy fields or retention counts are invalid", http.StatusBadRequest); return }
	input.Engine = "kopia"
	if input.KeepLast < 0 || input.KeepDaily < 0 || input.KeepWeekly < 0 || input.KeepMonthly < 0 || input.KeepYearly < 0 { http.Error(w,"retention counts cannot be negative",http.StatusBadRequest); return }
	if input.ScheduleIntervalMinutes != 0 && (input.ScheduleIntervalMinutes < 5 || input.ScheduleIntervalMinutes > 525600) { http.Error(w, "schedule interval must be manual or between 5 minutes and 365 days", http.StatusBadRequest); return }
	seen := map[string]bool{}
	for index := range input.Clients {
		assignment := &input.Clients[index]
		assignment.ClientID = strings.TrimSpace(assignment.ClientID)
		assignment.Source = strings.TrimSpace(assignment.Source)
		if assignment.ClientID == "" || len(assignment.ClientID) > 128 || seen[assignment.ClientID] { http.Error(w, "each client can have only one save-set assignment in a policy", http.StatusBadRequest); return }
		seen[assignment.ClientID] = true
		if assignment.Source == "" || len(assignment.Source) > 4096 || !path.IsAbs(assignment.Source) { http.Error(w, "each client assignment needs an absolute Linux save-set path", http.StatusBadRequest); return }
		validated, err := validateExcludePatterns(assignment.Excludes); if err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }; assignment.Excludes = validated
	}
	tx, err := s.db.BeginTx(r.Context(), nil); if err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; defer tx.Rollback()
	var policyExists bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM backup_policies WHERE policy_id=$1)`,r.PathValue("policyID")).Scan(&policyExists); err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }
	if !policyExists { http.NotFound(w,r); return }
	if _, err := tx.ExecContext(r.Context(), `UPDATE backup_policies SET name=$1,engine=$2,source_path='',exclude_patterns='[]'::jsonb,keep_last=$3,keep_daily=$4,keep_weekly=$5,keep_monthly=$6,keep_yearly=$7,schedule_interval_minutes=$8,next_run_at=CASE WHEN $8>0 THEN now()+make_interval(mins=>$8) ELSE NULL END,version=version+1 WHERE policy_id=$9`, input.Name,input.Engine,input.KeepLast,input.KeepDaily,input.KeepWeekly,input.KeepMonthly,input.KeepYearly,input.ScheduleIntervalMinutes,r.PathValue("policyID")); err != nil { http.Error(w,"policy name already exists or catalog unavailable",http.StatusConflict); return }
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM policy_clients WHERE policy_id=$1`,r.PathValue("policyID")); err != nil { http.Error(w,"could not update policy clients",http.StatusInternalServerError); return }
	for _, assignment := range input.Clients {
		var osName string
		if err := tx.QueryRowContext(r.Context(), `SELECT os_name FROM clients WHERE client_id=$1`,assignment.ClientID).Scan(&osName); err != nil { http.Error(w,"one or more selected clients do not exist",http.StatusBadRequest); return }
		if !strings.EqualFold(osName,"linux") { http.Error(w,"Kopia filesystem policies currently support configured Linux clients only",http.StatusBadRequest); return }
		excludesJSON, _ := json.Marshal(assignment.Excludes)
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO policy_clients(policy_id,client_id,source_path,exclude_patterns) VALUES($1,$2,$3,$4)`,r.PathValue("policyID"),assignment.ClientID,assignment.Source,excludesJSON); err != nil { http.Error(w,"could not update policy clients",http.StatusInternalServerError); return }
	}
	if err := tx.Commit(); err != nil { http.Error(w,"could not update policy",http.StatusServiceUnavailable); return }
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO audit_events(actor,action,target) VALUES($1,'policy_updated',$2)`,username,r.PathValue("policyID"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) expirePolicy(w http.ResponseWriter, r *http.Request) {
	username, ok := s.currentUser(r); if !ok { http.Error(w,"sign-in required",http.StatusUnauthorized); return }
	if !s.validCSRF(r) { http.Error(w,"request expired; reload and try again",http.StatusBadRequest); return }
	var policyID, engine string; var version,keepLast,keepDaily,keepWeekly,keepMonthly,keepYearly int
	if err := s.db.QueryRowContext(r.Context(), `SELECT policy_id,engine,version,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly FROM backup_policies WHERE policy_id=$1`,r.PathValue("policyID")).Scan(&policyID,&engine,&version,&keepLast,&keepDaily,&keepWeekly,&keepMonthly,&keepYearly); err != nil { http.NotFound(w,r); return }
	if engine != "kopia" { http.Error(w,"retention aging is currently supported only for Kopia policies",http.StatusBadRequest); return }
	rows, err := s.db.QueryContext(r.Context(), `SELECT client_id,source_path FROM policy_clients WHERE policy_id=$1 ORDER BY client_id`,policyID); if err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }
	type agingAssignment struct { ClientID, Source string }; clients := []agingAssignment{}
	for rows.Next() { var assignment agingAssignment; if err := rows.Scan(&assignment.ClientID,&assignment.Source); err != nil { rows.Close(); http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; clients=append(clients,assignment) }; if err := rows.Err(); err != nil { rows.Close(); http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; rows.Close()
	if len(clients)==0 { http.Error(w,"assign at least one client before running aging",http.StatusBadRequest); return }
	tx, err := s.db.BeginTx(r.Context(),nil); if err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; defer tx.Rollback()
	queued:=0
	for _, assignment := range clients {
		var pending bool
		if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_jobs WHERE policy_id=$1 AND client_id=$2 AND kind IN ('backup','retention') AND status IN ('queued','running'))`,policyID,assignment.ClientID).Scan(&pending); err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }
		if pending { http.Error(w,"cannot run aging while a backup or another aging job is queued or running for this policy client",http.StatusConflict); return }
		jobID, err := randomToken(); if err != nil { http.Error(w,"could not queue aging",http.StatusInternalServerError); return }
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO agent_jobs(job_id,client_id,kind,engine,source_path,created_by,policy_id,policy_version,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly) VALUES($1,$2,'retention','kopia',$3,$4,$5,$6,$7,$8,$9,$10,$11)`,jobID,assignment.ClientID,assignment.Source,username,policyID,version,keepLast,keepDaily,keepWeekly,keepMonthly,keepYearly); err != nil { http.Error(w,"could not queue aging",http.StatusInternalServerError); return }
		queued++
	}
	if err := tx.Commit(); err != nil { http.Error(w,"could not queue aging",http.StatusServiceUnavailable); return }
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO audit_events(actor,action,target) VALUES($1,'policy_retention_queued',$2)`,username,policyID)
	w.Header().Set("Content-Type","application/json"); w.WriteHeader(http.StatusAccepted); _ = json.NewEncoder(w).Encode(map[string]int{"clients":queued})
}

func (s *server) runPolicyScheduler() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		if err := s.queueScheduledPolicies(ctx); err != nil {
			log.Printf("policy scheduler: %v", err)
		}
		cancel()
	}
}

func (s *server) queueScheduledPolicies(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return err }
	defer tx.Rollback()

	type scheduledPolicy struct {
		ID, Engine, Destination, CreatedBy string
		Version, IntervalMinutes int
	}
	rows, err := tx.QueryContext(ctx, `SELECT policy_id,engine,destination,created_by,version,schedule_interval_minutes FROM backup_policies WHERE engine='kopia' AND schedule_interval_minutes>0 AND next_run_at<=now() ORDER BY next_run_at LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if err != nil { return err }
	policies := make([]scheduledPolicy, 0)
	for rows.Next() {
		var item scheduledPolicy
		if err := rows.Scan(&item.ID,&item.Engine,&item.Destination,&item.CreatedBy,&item.Version,&item.IntervalMinutes); err != nil { rows.Close(); return err }
		policies = append(policies,item)
	}
	if err := rows.Err(); err != nil { rows.Close(); return err }
	rows.Close()

	queuedJobs := 0
	for _, policy := range policies {
		clientRows, err := tx.QueryContext(ctx, `SELECT pc.client_id,pc.source_path,pc.exclude_patterns FROM policy_clients pc JOIN clients c USING(client_id) WHERE pc.policy_id=$1 ORDER BY c.name`, policy.ID)
		if err != nil { return err }
		type scheduledAssignment struct { ClientID, Source string; Excludes []byte }; assignments := make([]scheduledAssignment,0)
		for clientRows.Next() { var assignment scheduledAssignment; if err := clientRows.Scan(&assignment.ClientID,&assignment.Source,&assignment.Excludes); err != nil { clientRows.Close(); return err }; assignments=append(assignments,assignment) }
		if err := clientRows.Err(); err != nil { clientRows.Close(); return err }
		clientRows.Close()

		for _, assignment := range assignments {
			var hasPending bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_jobs WHERE policy_id=$1 AND client_id=$2 AND kind='backup' AND status IN ('queued','running'))`,policy.ID,assignment.ClientID).Scan(&hasPending); err != nil { return err }
			if hasPending { continue }
			jobID, err := randomToken(); if err != nil { return err }
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_jobs(job_id,client_id,kind,engine,source_path,exclude_patterns,created_by,policy_id,policy_version,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly) SELECT $1,$2,'backup',$3,$4,$5,$6,$7,version,keep_last,keep_daily,keep_weekly,keep_monthly,keep_yearly FROM backup_policies WHERE policy_id=$7`,jobID,assignment.ClientID,policy.Engine,assignment.Source,assignment.Excludes,policy.CreatedBy,policy.ID); err != nil { return err }
			queuedJobs++
		}
		if _, err := tx.ExecContext(ctx, `UPDATE backup_policies SET next_run_at=now()+make_interval(mins=>schedule_interval_minutes) WHERE policy_id=$1`,policy.ID); err != nil { return err }
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(actor,action,target) VALUES('scheduler','policy_schedule_queued',$1)`,policy.ID); err != nil { return err }
	}
	if err := tx.Commit(); err != nil { return err }
	if queuedJobs > 0 { log.Printf("policy scheduler queued %d backup job(s)",queuedJobs) }
	return nil
}

func (s *server) listDashboardJobs(ctx context.Context) ([]dashboardJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT j.job_id, j.client_id, c.name, j.kind, j.engine, CASE WHEN j.kind IN ('backup','retention') THEN j.source_path ELSE j.target_path END, j.status, j.created_at, j.message
		FROM agent_jobs j JOIN clients c USING (client_id)
		WHERE j.engine='kopia'
		ORDER BY j.created_at DESC LIMIT 100
	`)
	if err != nil { return nil, err }
	defer rows.Close()
	items := make([]dashboardJob, 0)
	for rows.Next() {
		var item dashboardJob
		var created time.Time
		if err := rows.Scan(&item.ID, &item.ClientID, &item.Client, &item.Kind, &item.Engine, &item.Source, &item.Status, &created, &item.Message); err != nil { return nil, err }
		item.CreatedAt = created.Local().Format("2006-01-02 15:04:05")
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *server) adminJobDetails(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentUser(r); !ok { http.Error(w,"sign-in required",http.StatusUnauthorized); return }
	var detail jobDetails
	var created time.Time
	var started, finished sql.NullTime
	err := s.db.QueryRowContext(r.Context(), `SELECT j.job_id,j.client_id,c.name,j.kind,j.engine,CASE WHEN j.kind IN ('backup','retention') THEN j.source_path ELSE j.target_path END,j.status,j.message,COALESCE(NULLIF(j.log_text,''),j.message),COALESCE(j.policy_id,''),j.policy_version,j.created_at,j.started_at,j.finished_at FROM agent_jobs j JOIN clients c USING(client_id) WHERE j.job_id=$1`,r.PathValue("jobID")).Scan(&detail.ID,&detail.ClientID,&detail.Client,&detail.Kind,&detail.Engine,&detail.Source,&detail.Status,&detail.Message,&detail.Logs,&detail.PolicyID,&detail.PolicyVersion,&created,&started,&finished)
	if errors.Is(err,sql.ErrNoRows) { http.Error(w,"job not found",http.StatusNotFound); return }
	if err != nil { http.Error(w,"could not load job details",http.StatusInternalServerError); return }
	detail.CreatedAt=created.Local().Format(time.RFC3339)
	if started.Valid { detail.StartedAt=started.Time.Local().Format(time.RFC3339) }
	if finished.Valid { detail.FinishedAt=finished.Time.Local().Format(time.RFC3339) }
	w.Header().Set("Content-Type","application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

func (s *server) createJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	var input struct {
		ClientID       string   `json:"client_id"`
		Kind           string   `json:"kind"`
		Engine         string   `json:"engine"`
		Source         string   `json:"source_path"`
		BackupID       string   `json:"backup_id"`
		Target         string   `json:"target_path"`
		TargetMode     string   `json:"target_mode"`
		AllowOverwrite bool     `json:"allow_overwrite"`
	}
	apiRequest := strings.Contains(r.Header.Get("Content-Type"), "application/json")
	if apiRequest {
		decoder := json.NewDecoder(r.Body); decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || !s.validCSRF(r) { http.Error(w, "request expired or invalid", http.StatusBadRequest); return }
	} else {
		if err := r.ParseForm(); err != nil || !s.validCSRF(r) { http.Error(w, "form expired or invalid", http.StatusBadRequest); return }
		input.ClientID, input.Kind = r.FormValue("client_id"), r.FormValue("kind")
		input.Engine = r.FormValue("engine")
		input.Source, input.BackupID, input.Target = r.FormValue("source_path"), r.FormValue("backup_id"), r.FormValue("target_path")
	input.TargetMode = r.FormValue("target_mode")
	}
	username, ok := s.currentUser(r)
	if !ok { http.Error(w, "sign-in required", http.StatusUnauthorized); return }
	clientID := strings.TrimSpace(input.ClientID)
	kind := input.Kind
	engine := strings.ToLower(strings.TrimSpace(input.Engine))
	if engine == "" { engine = "kopia" }
	source, backupID, target := strings.TrimSpace(input.Source), strings.TrimSpace(input.BackupID), strings.TrimSpace(input.Target)
	if len(clientID) == 0 || len(clientID) > 128 { http.Error(w, "choose a client", http.StatusBadRequest); return }
	if kind == "backup" && (source == "" || len(source) > 4096) { http.Error(w, "enter a source path on the selected client", http.StatusBadRequest); return }
	if kind == "restore" && (backupID == "" || len(backupID) > 128) { http.Error(w, "choose a Kopia restore point", http.StatusBadRequest); return }
	if kind != "backup" && kind != "restore" { http.Error(w, "unsupported job type", http.StatusBadRequest); return }
	if engine != "kopia" { http.Error(w, "Kopia is the only supported filesystem backup engine", http.StatusBadRequest); return }
	if kind == "restore" {
		if engine == "kopia" { input.TargetMode = "new_folder" }
		if input.TargetMode != "original" && input.TargetMode != "new_folder" { http.Error(w, "choose the original location or a new folder", http.StatusBadRequest); return }
		if input.TargetMode == "new_folder" && (target == "" || len(target) > 4096) { http.Error(w, "enter a new destination folder", http.StatusBadRequest); return }
		if engine == "kopia" && !path.IsAbs(target) { http.Error(w, "Kopia restore parent folder must be an absolute Linux path", http.StatusBadRequest); return }
		if input.TargetMode == "original" && !input.AllowOverwrite { http.Error(w, "confirm that existing files may be overwritten", http.StatusBadRequest); return }
	}
	if kind == "restore" && engine == "kopia" {
		var snapshotClient, snapshotSource string
		if err := s.db.QueryRowContext(r.Context(), `SELECT client_id,source_path FROM kopia_snapshots WHERE snapshot_id=$1 AND expired_at IS NULL`, backupID).Scan(&snapshotClient,&snapshotSource); err != nil { http.Error(w, "active Kopia snapshot was not found", http.StatusBadRequest); return }
		rootName := path.Base(strings.TrimRight(snapshotSource, "/"))
		if rootName == "" || rootName == "." || rootName == "/" { rootName = "restored-snapshot" }
		target = path.Join(target, rootName)
		if len(target) > 4096 { http.Error(w, "resolved restore destination is too long", http.StatusBadRequest); return }
	}
	var clientOS string
	if err := s.db.QueryRowContext(r.Context(), `SELECT os_name FROM clients WHERE client_id=$1`, clientID).Scan(&clientOS); err != nil {
		http.Error(w, "selected client was not found", http.StatusBadRequest); return
	}
	if !strings.EqualFold(clientOS, "linux") { http.Error(w, "Kopia filesystem jobs currently support configured Linux clients only", http.StatusBadRequest); return }
	id, err := randomToken()
	if err != nil { http.Error(w, "could not create job", http.StatusInternalServerError); return }
	_, err = s.db.ExecContext(r.Context(), `INSERT INTO agent_jobs (job_id, client_id, kind, engine, source_path, backup_id, target_path, target_mode, allow_overwrite, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, clientID, kind, engine, source, backupID, target, input.TargetMode, input.AllowOverwrite, username)
	if err != nil { http.Error(w, "could not queue job", http.StatusInternalServerError); return }
	_, _ = s.db.ExecContext(r.Context(), `INSERT INTO audit_events (actor, action, target) VALUES ($1,$2,$3)`, username, kind+"_job_queued", id)
	if apiRequest { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(http.StatusCreated); _ = json.NewEncoder(w).Encode(map[string]string{"job_id":id, "status":"queued"}); return }
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) claimJob(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authenticateAgent(r)
	if !ok { http.Error(w, "agent credential invalid or revoked", http.StatusUnauthorized); return }
	version := strings.TrimSpace(r.Header.Get("X-Agent-Version"))
	if len(version) > 64 { version = version[:64] }
	_, _ = s.db.ExecContext(r.Context(), `UPDATE clients SET last_seen_at=now(), agent_version=CASE WHEN $2='' THEN agent_version ELSE $2 END WHERE client_id=$1`, clientID, version)
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil { http.Error(w, "catalog unavailable", http.StatusServiceUnavailable); return }
	defer tx.Rollback()
	var job agentJob
	var jobExcludes []byte
	err = tx.QueryRowContext(r.Context(), `SELECT j.job_id, j.kind, j.engine, j.source_path, j.backup_id, j.target_path, j.target_mode, COALESCE(j.policy_id,''),j.policy_version,COALESCE(j.keep_last,p.keep_last,0),COALESCE(j.keep_daily,p.keep_daily,0),COALESCE(j.keep_weekly,p.keep_weekly,0),COALESCE(j.keep_monthly,p.keep_monthly,0),COALESCE(j.keep_yearly,p.keep_yearly,0),j.exclude_patterns FROM agent_jobs j LEFT JOIN backup_policies p ON p.policy_id=j.policy_id WHERE j.client_id=$1 AND j.status='queued' ORDER BY j.created_at LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, clientID).Scan(&job.JobID, &job.Kind, &job.Engine, &job.Source, &job.BackupID, &job.Target, &job.TargetMode,&job.PolicyID,&job.PolicyVersion,&job.KeepLast,&job.KeepDaily,&job.KeepWeekly,&job.KeepMonthly,&job.KeepYearly,&jobExcludes)
	if errors.Is(err, sql.ErrNoRows) { w.WriteHeader(http.StatusNoContent); return }
	if err != nil { http.Error(w, "catalog unavailable", http.StatusServiceUnavailable); return }
	if err := json.Unmarshal(jobExcludes, &job.Excludes); err != nil { http.Error(w,"job exclusion data is invalid",http.StatusInternalServerError); return }
	if _, err := tx.ExecContext(r.Context(), `UPDATE agent_jobs SET status='running',started_at=COALESCE(started_at,now()),updated_at=now() WHERE job_id=$1`, job.JobID); err != nil { http.Error(w, "could not claim job", http.StatusServiceUnavailable); return }
	if err := tx.Commit(); err != nil { http.Error(w, "could not claim job", http.StatusServiceUnavailable); return }
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(job)
}

func (s *server) finishJob(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authenticateAgent(r)
	if !ok { http.Error(w, "agent credential invalid or revoked", http.StatusUnauthorized); return }
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
	var result finishJobRequest
	decoder := json.NewDecoder(r.Body); decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || (result.Status != "succeeded" && result.Status != "failed") { http.Error(w, "invalid job result", http.StatusBadRequest); return }
	if len(result.Message) > 2000 { result.Message = result.Message[:2000] }
	if len(result.Logs) > 64*1024 { result.Logs = result.Logs[:64*1024] }
	if len(result.SnapshotID)>128 || len(result.RootObjectID)>256 { http.Error(w,"snapshot reference is invalid",http.StatusBadRequest); return }
	tx, err := s.db.BeginTx(r.Context(),nil); if err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }; defer tx.Rollback()
	var jobKind, engine, source, policyID string; var policyVersion int
	err = tx.QueryRowContext(r.Context(), `UPDATE agent_jobs SET status=$1,message=$2,log_text=$3,finished_at=now(),updated_at=now() WHERE job_id=$4 AND client_id=$5 AND status='running' RETURNING kind,engine,source_path,COALESCE(policy_id,''),policy_version`,result.Status,result.Message,result.Logs,r.PathValue("jobID"),clientID).Scan(&jobKind,&engine,&source,&policyID,&policyVersion)
	if errors.Is(err,sql.ErrNoRows) { http.Error(w,"job not found or no longer running",http.StatusNotFound); return }
	if err != nil { http.Error(w,"catalog unavailable",http.StatusServiceUnavailable); return }
	if result.Status=="succeeded" && engine=="kopia" && jobKind=="backup" && result.SnapshotID!="" && result.RootObjectID!="" {
		if result.ActiveSnapshotIDs != nil {
			if len(result.ActiveSnapshotIDs)>10000 { http.Error(w,"snapshot catalog refresh exceeds limit",http.StatusBadRequest); return }
			containsCurrentSnapshot := false
			for _, snapshotID := range result.ActiveSnapshotIDs { if len(snapshotID) > 128 { http.Error(w,"snapshot catalog contains an invalid ID",http.StatusBadRequest); return }; if snapshotID == result.SnapshotID { containsCurrentSnapshot = true } }
			if containsCurrentSnapshot {
				if _,err:=tx.ExecContext(r.Context(),`UPDATE kopia_snapshots SET expired_at=COALESCE(expired_at,now()) WHERE client_id=$1 AND source_path=$2 AND expired_at IS NULL AND NOT (snapshot_id = ANY($3::text[]))`,clientID,source,result.ActiveSnapshotIDs); err!=nil { http.Error(w,"could not reconcile expired Kopia snapshots",http.StatusServiceUnavailable); return }
			}
		}
		if _,err := tx.ExecContext(r.Context(),`INSERT INTO kopia_snapshots(snapshot_id,root_object_id,client_id,policy_id,policy_version,job_id,source_path,expired_at) VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,NULL) ON CONFLICT(snapshot_id) DO UPDATE SET expired_at=NULL`,result.SnapshotID,result.RootObjectID,clientID,policyID,policyVersion,r.PathValue("jobID"),source); err != nil { http.Error(w,"could not record Kopia snapshot in catalog",http.StatusServiceUnavailable); return }
	}
	if result.Status=="succeeded" && engine=="kopia" && jobKind=="retention" && result.ActiveSnapshotIDs!=nil {
		if len(result.ActiveSnapshotIDs)>10000 { http.Error(w,"snapshot catalog refresh exceeds limit",http.StatusBadRequest); return }
		for _, snapshotID := range result.ActiveSnapshotIDs { if len(snapshotID)>128 { http.Error(w,"snapshot catalog contains an invalid ID",http.StatusBadRequest); return } }
		if _,err:=tx.ExecContext(r.Context(),`UPDATE kopia_snapshots SET expired_at=COALESCE(expired_at,now()) WHERE client_id=$1 AND source_path=$2 AND expired_at IS NULL AND NOT (snapshot_id = ANY($3::text[]))`,clientID,source,result.ActiveSnapshotIDs); err!=nil { http.Error(w,"could not reconcile expired Kopia snapshots",http.StatusServiceUnavailable); return }
	}
	if err:=tx.Commit(); err!=nil { http.Error(w,"could not commit job result",http.StatusServiceUnavailable); return }
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request enrollRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid enrollment request", http.StatusBadRequest)
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.OS = strings.TrimSpace(request.OS)
	request.AgentVersion = strings.TrimSpace(request.AgentVersion)
	if len(request.EnrollmentToken) < 32 || len(request.Name) == 0 || len(request.Name) > 255 || len(request.OS) == 0 || len(request.OS) > 64 || len(request.AgentVersion) == 0 || len(request.AgentVersion) > 64 {
		http.Error(w, "invalid enrollment request", http.StatusBadRequest)
		return
	}

	enrollmentHash := sha256.Sum256([]byte(request.EnrollmentToken))
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	defer tx.Rollback()
	var expiresAt time.Time
	var usedAt sql.NullTime
	err = tx.QueryRowContext(r.Context(), `
		SELECT expires_at, used_at FROM enrollment_tokens
		WHERE token_hash = $1 FOR UPDATE
	`, enrollmentHash[:]).Scan(&expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (usedAt.Valid || !expiresAt.After(time.Now()))) {
		http.Error(w, "enrollment token is invalid, expired, or already used", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	clientID, err := randomToken()
	if err != nil {
		http.Error(w, "could not create client identity", http.StatusInternalServerError)
		return
	}
	apiToken, err := randomToken()
	if err != nil {
		http.Error(w, "could not create client credential", http.StatusInternalServerError)
		return
	}
	apiHash := sha256.Sum256([]byte(apiToken))
	result, err := tx.ExecContext(r.Context(), `
		INSERT INTO clients (client_id, name, os_name, agent_version)
		VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING
	`, clientID, request.Name, request.OS, request.AgentVersion)
	if err != nil {
		http.Error(w, "could not enroll client", http.StatusInternalServerError)
		return
	}
	if count, _ := result.RowsAffected(); count == 0 {
		http.Error(w, "a client with that name is already enrolled", http.StatusConflict)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO client_credentials (credential_hash, client_id) VALUES ($1, $2)
	`, apiHash[:], clientID); err != nil {
		http.Error(w, "could not save client credential", http.StatusInternalServerError)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE enrollment_tokens SET used_at = now() WHERE token_hash = $1`, enrollmentHash[:]); err != nil {
		http.Error(w, "could not complete enrollment", http.StatusInternalServerError)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO audit_events (actor, action, target) VALUES ($1, 'agent_enrolled', $2)
	`, request.Name, clientID); err != nil {
		http.Error(w, "could not record enrollment", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "could not complete enrollment", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(enrollResponse{ClientID: clientID, APIToken: apiToken})
}

func (s *server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authenticateAgent(r)
	if !ok {
		http.Error(w, "agent credential required", http.StatusUnauthorized)
		return
	}
	version := strings.TrimSpace(r.Header.Get("X-Agent-Version"))
	if len(version) > 64 { version = version[:64] }
	if _, err := s.db.ExecContext(r.Context(), `UPDATE clients SET last_seen_at = now(), agent_version = CASE WHEN $2 = '' THEN agent_version ELSE $2 END WHERE client_id = $1`, clientID, version); err != nil {
		http.Error(w, "catalog unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"client_id": clientID, "status": "online"})
}

func (s *server) validateAgent(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authenticateAgent(r)
	if !ok {
		http.Error(w, "agent credential invalid or revoked", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"client_id": clientID})
}

func (s *server) authenticateAgent(r *http.Request) (string, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	tokenHash := sha256.Sum256([]byte(parts[1]))
	var clientID string
	err := s.db.QueryRowContext(r.Context(), `
		SELECT client_id FROM client_credentials
		WHERE credential_hash = $1 AND revoked_at IS NULL
	`, tokenHash[:]).Scan(&clientID)
	return clientID, err == nil
}

func (s *server) setCSRF(w http.ResponseWriter) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: "backup_csrf", Value: token, Path: "/", HttpOnly: true,
		Secure: s.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: 3600,
	})
	return token, nil
}

func (s *server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("backup_csrf")
	if err != nil {
		return false
	}
	provided := r.Header.Get("X-CSRF-Token")
	if provided == "" { provided = r.FormValue("csrf") }
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(provided)) == 1
}

func (s *server) currentUser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie("backup_session")
	if err != nil || cookie.Value == "" {
		return "", false
	}
	tokenHash := sha256.Sum256([]byte(cookie.Value))
	var username string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT username FROM sessions WHERE token_hash = $1 AND expires_at > now()`, tokenHash[:],
	).Scan(&username)
	return username, err == nil
}

func randomToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
