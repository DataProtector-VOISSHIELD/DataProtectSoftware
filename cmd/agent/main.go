package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const agentVersion = "0.7.0"
type agentState struct {
	ServerURL string `json:"server_url"`
	ClientID  string `json:"client_id"`
	APIToken  string `json:"api_token"`
}

type enrollResponse struct {
	ClientID string `json:"client_id"`
	APIToken string `json:"api_token"`
}

type agentJob struct {
	JobID          string         `json:"job_id"`
	Kind           string         `json:"kind"`
	Engine         string         `json:"engine"`
	Source         string         `json:"source_path"`
	BackupID       string         `json:"backup_id"`
	Target         string         `json:"target_path"`
	TargetMode     string         `json:"target_mode"`
	AllowOverwrite bool           `json:"allow_overwrite"`
	PolicyID       string         `json:"policy_id,omitempty"`
	PolicyVersion  int            `json:"policy_version,omitempty"`
	KeepLast       int            `json:"keep_last,omitempty"`
	KeepDaily      int            `json:"keep_daily,omitempty"`
	KeepWeekly     int            `json:"keep_weekly,omitempty"`
	KeepMonthly    int            `json:"keep_monthly,omitempty"`
	KeepYearly     int            `json:"keep_yearly,omitempty"`
	Excludes       []string       `json:"exclude_patterns,omitempty"`
}

type jobResult struct {
	Message string `json:"message"`
	Logs string `json:"logs,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	RootObjectID string `json:"root_object_id,omitempty"`
	ActiveSnapshotIDs []string `json:"active_snapshot_ids,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "enroll":
		err = enroll(os.Args[2:])
	case "heartbeat":
		err = heartbeat(os.Args[2:])
	case "run":
		err = runAgent(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup-agent:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  backup-agent enroll --server URL --token-file FILE [--name HOSTNAME] [--state-file FILE]")
	fmt.Fprintln(os.Stderr, "  backup-agent heartbeat [--state-file FILE]")
	fmt.Fprintln(os.Stderr, "  backup-agent run [--state-file FILE] [--poll-interval 5s]")
}

func runAgent(args []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	stateFile := flags.String("state-file", defaultStateFile(), "path to the client credential file")
	pollInterval := flags.Duration("poll-interval", 5*time.Second, "interval for checking queued jobs")
	if err := flags.Parse(args); err != nil { return err }
	if *pollInterval < time.Second || *pollInterval > time.Minute { return errors.New("--poll-interval must be between 1s and 1m") }
	state, err := loadState(*stateFile)
	if err != nil { return err }
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("backup agent service started for client %s", state.ClientID)
	lastHeartbeat := time.Time{}
	for {
		if time.Since(lastHeartbeat) >= 30*time.Second {
			if err := sendHeartbeat(state); err != nil { log.Printf("heartbeat failed: %v", err) } else { lastHeartbeat = time.Now() }
		}
		job, found, err := claimAgentJob(state)
		if err != nil { log.Printf("job poll failed: %v", err) } else if found {
			log.Printf("running %s job %s", job.Kind, job.JobID)
			result, jobErr := executeAgentJob(ctx, job, state, *stateFile)
			status := "succeeded"
			if result.Message == "" { result.Message = "completed" }
			if result.Logs == "" { result.Logs = result.Message }
			if jobErr != nil {
				status, result.Message = "failed", jobErr.Error()
				if result.Logs == "" || result.Logs == "completed" { result.Logs = jobErr.Error() } else { result.Logs += "\nERROR: " + jobErr.Error() }
				log.Printf("job %s failed: %v", job.JobID, jobErr)
			}
			if len(result.Logs) > 64*1024 { result.Logs = result.Logs[:64*1024] + "\n[agent log truncated]" }
			if err := reportJobResult(state, job.JobID, status, result); err != nil { log.Printf("could not report job %s result: %v", job.JobID, err) }
			continue
		}
		select { case <-ctx.Done(): log.Printf("backup agent service stopping"); return nil; case <-time.After(*pollInterval): }
	}
}

func sendHeartbeat(state agentState) error {
	request, err := http.NewRequest(http.MethodPost, state.ServerURL+"/api/v1/agents/heartbeat", bytes.NewReader([]byte("{}")))
	if err != nil { return err }
	request.Header.Set("Authorization", "Bearer "+state.APIToken)
	request.Header.Set("X-Agent-Version", agentVersion)
	response, err := (&http.Client{Timeout: 15*time.Second}).Do(request)
	if err != nil { return err }
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK { body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024)); return fmt.Errorf("heartbeat rejected (%s): %s", response.Status, strings.TrimSpace(string(body))) }
	return nil
}

func claimAgentJob(state agentState) (agentJob, bool, error) {
	var job agentJob
	request, err := http.NewRequest(http.MethodPost, state.ServerURL+"/api/v1/agents/jobs/claim", bytes.NewReader([]byte("{}")))
	if err != nil { return job, false, err }
	request.Header.Set("Authorization", "Bearer "+state.APIToken)
	request.Header.Set("X-Agent-Version", agentVersion)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15*time.Second}).Do(request)
	if err != nil { return job, false, err }
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent { return job, false, nil }
	if response.StatusCode != http.StatusOK { body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024)); return job, false, fmt.Errorf("job claim rejected (%s): %s", response.Status, strings.TrimSpace(string(body))) }
	if err := json.NewDecoder(io.LimitReader(response.Body, 18*1024*1024)).Decode(&job); err != nil { return job, false, err }
	return job, true, nil
}

func executeAgentJob(ctx context.Context, job agentJob, state agentState, stateFile string) (jobResult, error) {
	switch job.Kind {
	case "backup":
		return createKopiaSnapshot(ctx, job)
	case "restore":
		return restoreKopiaSnapshot(ctx,job)
	case "retention":
		return expireKopiaSnapshots(ctx, job)
	default:
		return jobResult{}, fmt.Errorf("unsupported job type %q", job.Kind)
	}
}

func expireKopiaSnapshots(ctx context.Context, job agentJob) (jobResult, error) {
	if strings.TrimSpace(job.Source) == "" || !filepath.IsAbs(job.Source) { return jobResult{}, errors.New("Kopia aging requires an absolute policy source path") }
	policy := exec.CommandContext(ctx,"kopia","policy","set","--keep-latest",strconv.Itoa(job.KeepLast),"--keep-daily",strconv.Itoa(job.KeepDaily),"--keep-weekly",strconv.Itoa(job.KeepWeekly),"--keep-monthly",strconv.Itoa(job.KeepMonthly),"--keep-annual",strconv.Itoa(job.KeepYearly),job.Source)
	var policyErr bytes.Buffer; policy.Stderr=&policyErr
	if err := policy.Run(); err != nil { detail:=strings.TrimSpace(policyErr.String()); if detail=="" { detail=err.Error() }; return jobResult{Logs:detail},fmt.Errorf("could not apply current Kopia retention rule: %s",detail) }
	expire := exec.CommandContext(ctx,"kopia","snapshot","expire","--delete",job.Source)
	var expireOut, expireErr bytes.Buffer; expire.Stdout=&expireOut; expire.Stderr=&expireErr
	if err := expire.Run(); err != nil { detail:=strings.TrimSpace(expireErr.String()); if detail=="" { detail=err.Error() }; if len(detail)>1500 { detail=detail[:1500] }; return jobResult{Logs:detail},fmt.Errorf("Kopia snapshot expiration failed: %s",detail) }
	list := exec.CommandContext(ctx,"kopia","snapshot","list","--json","--show-identical",job.Source)
	var listErr bytes.Buffer; list.Stderr=&listErr
	output, err := list.Output(); if err != nil { detail:=strings.TrimSpace(listErr.String()); if detail=="" { detail=err.Error() }; return jobResult{Logs:strings.TrimSpace(expireOut.String())+"\n"+detail},fmt.Errorf("expiration completed, but repository snapshot refresh failed: %s",detail) }
	var snapshots []struct { ID string `json:"id"` }
	if err := json.Unmarshal(output,&snapshots); err != nil { return jobResult{Logs:strings.TrimSpace(expireOut.String())},fmt.Errorf("expiration completed, but Kopia returned an unreadable snapshot list: %w",err) }
	active:=make([]string,0,len(snapshots)); for _, snapshot := range snapshots { if snapshot.ID!="" { active=append(active,snapshot.ID) } }
	message:=fmt.Sprintf("Kopia aging completed for %s; %d active snapshot(s) remain",job.Source,len(active))
	logs:=strings.TrimSpace(expireOut.String())+"\n"+message
	return jobResult{Message:message,Logs:logs,ActiveSnapshotIDs:active},nil
}

func restoreKopiaSnapshot(ctx context.Context, job agentJob) (jobResult,error) {
	if strings.TrimSpace(job.BackupID)=="" || strings.TrimSpace(job.Target)=="" || !filepath.IsAbs(job.Target) { return jobResult{},errors.New("Kopia restore requires a snapshot ID and absolute destination path") }
	parentInfo,err:=os.Stat(filepath.Dir(job.Target))
	if err!=nil { return jobResult{},fmt.Errorf("inspect restore parent folder: %w",err) }
	if !parentInfo.IsDir() { return jobResult{},errors.New("restore parent path is not a directory") }
	if _,err:=os.Lstat(job.Target); err==nil { return jobResult{},errors.New("restore destination already exists; choose a new folder") } else if !os.IsNotExist(err) { return jobResult{},fmt.Errorf("inspect restore destination: %w",err) }
	command:=exec.CommandContext(ctx,"kopia","snapshot","restore",job.BackupID,job.Target)
	var stderr,stdout bytes.Buffer; command.Stderr=&stderr; command.Stdout=&stdout
	err=command.Run()
	if err!=nil { detail:=strings.TrimSpace(stderr.String()); if len(detail)>1500 { detail=detail[:1500] }; if detail=="" { detail=err.Error() }; return jobResult{},fmt.Errorf("Kopia restore failed: %s",detail) }
	message:=strings.TrimSpace(stdout.String()); if len(message)>1500 { message=message[:1500] }; if message=="" { message=fmt.Sprintf("Kopia snapshot %s restored to %s",job.BackupID,job.Target) }
	return jobResult{Message:message,Logs:message},nil
}


func createKopiaSnapshot(ctx context.Context, job agentJob) (jobResult, error) {
	source := job.Source
	if strings.TrimSpace(source) == "" || !filepath.IsAbs(source) { return jobResult{}, errors.New("Kopia backup source must be an absolute path") }
	if job.PolicyID != "" {
		policyArgs := []string{"policy", "set", "--clear-ignore", "--keep-latest", strconv.Itoa(job.KeepLast), "--keep-daily", strconv.Itoa(job.KeepDaily), "--keep-weekly", strconv.Itoa(job.KeepWeekly), "--keep-monthly", strconv.Itoa(job.KeepMonthly), "--keep-annual", strconv.Itoa(job.KeepYearly)}
		if filepath.Clean(source) == string(filepath.Separator) {
			for _, pattern := range []string{"proc/", "sys/", "dev/", "run/", "tmp/", "var/tmp/"} { policyArgs = append(policyArgs, "--add-ignore", pattern) }
		}
		for _, pattern := range job.Excludes { policyArgs = append(policyArgs, "--add-ignore", pattern) }
		policyArgs = append(policyArgs, source)
		policyCommand := exec.CommandContext(ctx, "kopia", policyArgs...)
		var policyStderr bytes.Buffer; policyCommand.Stderr = &policyStderr
		if err := policyCommand.Run(); err != nil { return jobResult{}, fmt.Errorf("apply Kopia retention policy: %s", strings.TrimSpace(policyStderr.String())) }
	}
	command := exec.CommandContext(ctx, "kopia", "snapshot", "create", "--json", source)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 1500 { detail = detail[:1500] }
		if detail != "" { return jobResult{}, fmt.Errorf("Kopia snapshot failed: %s", detail) }
		return jobResult{}, fmt.Errorf("Kopia snapshot failed: %w", err)
	}
	var snapshot struct {
		ID               string `json:"id"`
		Incomplete       string `json:"incomplete,omitempty"`
		IncompleteReason string `json:"incompleteReason,omitempty"`
		Stats            struct {
			FileCount          int64 `json:"fileCount"`
			DirCount           int64 `json:"dirCount"`
			TotalSize          int64 `json:"totalSize"`
			ReadErrors         int64 `json:"readErrors"`
			ExcludedFileCount  int64 `json:"excludedFileCount"`
			ExcludedDirCount   int64 `json:"excludedDirCount"`
			ExcludedTotalSize  int64 `json:"excludedTotalSize"`
			SymlinkCount       int64 `json:"symlinkCount"`
		} `json:"stats"`
		RootEntry struct {
			ObjectID string `json:"obj"`
			Summary  struct {
				FailedCount int64 `json:"numFailed"`
				SymlinkCount int64 `json:"symlinks"`
			} `json:"summ"`
		} `json:"rootEntry"`
	}
	if err := json.Unmarshal(output, &snapshot); err != nil { return jobResult{}, fmt.Errorf("parse Kopia snapshot result: %w", err) }
	if snapshot.ID == "" || snapshot.RootEntry.ObjectID == "" { return jobResult{}, errors.New("Kopia returned a snapshot without an ID or root object ID") }
	message := fmt.Sprintf("Kopia snapshot %s created (root %s)", snapshot.ID, snapshot.RootEntry.ObjectID)
	warnings := make([]string, 0, 4)
	if reason := strings.TrimSpace(snapshot.IncompleteReason); reason != "" {
		warnings = append(warnings, "Kopia marked the snapshot incomplete: "+reason)
	} else if reason := strings.TrimSpace(snapshot.Incomplete); reason != "" {
		warnings = append(warnings, "Kopia marked the snapshot incomplete: "+reason)
	}
	if snapshot.Stats.ReadErrors > 0 {
		warnings = append(warnings, fmt.Sprintf("Kopia reported %d file read error(s); review the agent log", snapshot.Stats.ReadErrors))
	} else if snapshot.RootEntry.Summary.FailedCount > 0 {
		warnings = append(warnings, fmt.Sprintf("Kopia reported %d skipped or failed item(s); review the agent log", snapshot.RootEntry.Summary.FailedCount))
	}
	if snapshot.Stats.ExcludedFileCount > 0 || snapshot.Stats.ExcludedDirCount > 0 {
		warnings = append(warnings, fmt.Sprintf("Kopia excluded %d file(s) and %d directory item(s) by policy", snapshot.Stats.ExcludedFileCount, snapshot.Stats.ExcludedDirCount))
	}
	symlinks := snapshot.Stats.SymlinkCount
	if symlinks == 0 {
		symlinks = snapshot.RootEntry.Summary.SymlinkCount
	}
	if symlinks > 0 {
		warnings = append(warnings, fmt.Sprintf("snapshot contains %d symbolic link(s); Kopia stores links according to its configured policy", symlinks))
	}
	if len(warnings) > 0 {
		message += "; WARNING: " + strings.Join(warnings, "; ")
	}
	var activeSnapshotIDs []string
	if job.PolicyID != "" {
		expire := exec.CommandContext(ctx, "kopia", "snapshot", "expire", "--delete", source)
		var expireStderr bytes.Buffer; expire.Stderr = &expireStderr
		if err := expire.Run(); err != nil { detail := strings.TrimSpace(expireStderr.String()); if detail == "" { detail = err.Error() }; message += "; WARNING: backup succeeded but retention expiration failed: " + detail }
		list := exec.CommandContext(ctx,"kopia","snapshot","list","--json","--show-identical",source)
		var listStderr bytes.Buffer; list.Stderr=&listStderr
		output,err:=list.Output()
		if err!=nil { detail:=strings.TrimSpace(listStderr.String()); if detail=="" { detail=err.Error() }; message += "; WARNING: could not refresh snapshot catalog after expiration: "+detail } else {
			var current []struct { ID string `json:"id"` }
			if err:=json.Unmarshal(output,&current); err!=nil {
				message += "; WARNING: Kopia returned an unreadable snapshot list after expiration"
			} else {
				activeSnapshotIDs = make([]string, 0, len(current))
				containsCurrentSnapshot := false
				for _, item := range current {
					if item.ID == "" { continue }
					activeSnapshotIDs = append(activeSnapshotIDs, item.ID)
					if item.ID == snapshot.ID { containsCurrentSnapshot = true }
				}
				if !containsCurrentSnapshot {
					activeSnapshotIDs = nil
					message += "; WARNING: the post-expiration snapshot list did not contain the snapshot just created; catalog aging was skipped"
				}
			}
		}
	}
	logOutput := fmt.Sprintf("%s\nfiles=%d directories=%d total_bytes=%d read_errors=%d excluded_files=%d excluded_directories=%d",message,snapshot.Stats.FileCount,snapshot.Stats.DirCount,snapshot.Stats.TotalSize,snapshot.Stats.ReadErrors,snapshot.Stats.ExcludedFileCount,snapshot.Stats.ExcludedDirCount)
	return jobResult{Message:message,Logs:logOutput,SnapshotID:snapshot.ID,RootObjectID:snapshot.RootEntry.ObjectID,ActiveSnapshotIDs:activeSnapshotIDs},nil
}

func reportJobResult(state agentState, jobID, status string, result jobResult) error {
	body, err := json.Marshal(map[string]any{"status": status, "message": result.Message, "logs":result.Logs, "snapshot_id": result.SnapshotID, "root_object_id": result.RootObjectID, "active_snapshot_ids": result.ActiveSnapshotIDs})
	if err != nil { return err }
	request, err := http.NewRequest(http.MethodPost, state.ServerURL+"/api/v1/agents/jobs/"+url.PathEscape(jobID)+"/finish", bytes.NewReader(body))
	if err != nil { return err }
	request.Header.Set("Authorization", "Bearer "+state.APIToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15*time.Second}).Do(request)
	if err != nil { return err }
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent { body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024)); return fmt.Errorf("job result rejected (%s): %s", response.Status, strings.TrimSpace(string(body))) }
	return nil
}

func enroll(args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	serverURL := flags.String("server", "", "control-plane base URL")
	tokenFile := flags.String("token-file", "", "file containing the one-time enrollment token")
	name := flags.String("name", "", "client name; defaults to the machine hostname")
	stateFile := flags.String("state-file", defaultStateFile(), "where to save the client credential")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *serverURL == "" || *tokenFile == "" {
		return errors.New("--server and --token-file are required")
	}
	parsed, err := url.Parse(*serverURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("--server must be an http or https URL without embedded credentials")
	}
	clientName := strings.TrimSpace(*name)
	if clientName == "" {
		clientName, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("get hostname: %w", err)
		}
	}
	enrollmentTokenBytes, err := os.ReadFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read enrollment token file: %w", err)
	}
	enrollmentToken := strings.TrimSpace(string(enrollmentTokenBytes))
	if enrollmentToken == "" {
		return errors.New("enrollment token file is empty")
	}

	body, err := json.Marshal(map[string]string{
		"enrollment_token": enrollmentToken,
		"name":             clientName,
		"os":               runtime.GOOS,
		"agent_version":    agentVersion,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(*serverURL, "/")+"/api/v1/agents/enroll", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("contact control plane: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("read enrollment response: %w", err)
	}
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("enrollment failed (%s): %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	var result enrollResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return fmt.Errorf("decode enrollment response: %w", err)
	}
	if result.ClientID == "" || result.APIToken == "" {
		return errors.New("control plane returned an incomplete client credential")
	}
	if err := saveState(*stateFile, agentState{ServerURL: strings.TrimRight(*serverURL, "/"), ClientID: result.ClientID, APIToken: result.APIToken}); err != nil {
		return err
	}
	_ = os.Remove(*tokenFile)
	fmt.Printf("Enrolled client %s. Credential saved to %s\n", result.ClientID, *stateFile)
	return nil
}

func heartbeat(args []string) error {
	flags := flag.NewFlagSet("heartbeat", flag.ContinueOnError)
	stateFile := flags.String("state-file", defaultStateFile(), "path to the client credential file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	state, err := loadState(*stateFile)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, state.ServerURL+"/api/v1/agents/heartbeat", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+state.APIToken)
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("contact control plane: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("heartbeat failed (%s): %s", response.Status, strings.TrimSpace(string(responseBody)))
	}
	fmt.Printf("Heartbeat accepted for client %s\n", state.ClientID)
	return nil
}

func saveState(path string, state agentState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create credential file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("write credential file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(temporary)
		return fmt.Errorf("sync credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("close credential file: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("activate credential file: %w", err)
	}
	return nil
}

func loadState(path string) (agentState, error) {
	var state agentState
	data, err := os.ReadFile(path)
	if err != nil {
		return state, fmt.Errorf("read credential file: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode credential file: %w", err)
	}
	if state.ServerURL == "" || state.ClientID == "" || state.APIToken == "" {
		return state, errors.New("credential file is incomplete")
	}
	return state, nil
}

func defaultStateFile() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(".", "backup-agent", "state.json")
	}
	return filepath.Join(configDir, "backup-agent", "state.json")
}
