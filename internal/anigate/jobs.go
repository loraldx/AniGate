package anigate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type JobState string

const (
	JobRunning   JobState = "running"
	JobDone      JobState = "done"
	JobFailed    JobState = "failed"
	JobCancelled JobState = "cancelled"
)

type JobRecord struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind,omitempty"`
	Preset     string         `json:"preset"`
	SessionID  string         `json:"session_id,omitempty"`
	TaskID     string         `json:"task_id,omitempty"`
	State      JobState       `json:"state"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
	Workspace  string         `json:"workspace"`
	Cwd        string         `json:"cwd"`
	Command    []string       `json:"command"`
	Args       map[string]any `json:"args,omitempty"`
	ExitCode   int            `json:"exit_code,omitempty"`
	Error      string         `json:"error,omitempty"`
	LogPath    string         `json:"log_path"`
	Truncated  bool           `json:"truncated,omitempty"`
}

type JobSpec struct {
	Kind       string
	Name       string
	SessionID  string
	TaskID     string
	Workspace  string
	Cwd        string
	Command    []string
	Stdin      string
	Args       map[string]any
	TimeoutSec int
	Env        map[string]string
	EventTool  string
	OnFinish   func(JobRecord)
}

type JobManager struct {
	cfg    Config
	policy pathPolicy
	events *EventLog
	log    *slog.Logger
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewJobManager(cfg Config, policy pathPolicy, events *EventLog, log *slog.Logger) (*JobManager, error) {
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "jobs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "logs"), 0o700); err != nil {
		return nil, err
	}
	m := &JobManager{cfg: cfg, policy: policy, events: events, log: log, active: map[string]context.CancelFunc{}}
	m.reconcileInterruptedJobs()
	return m, nil
}

// reconcileInterruptedJobs finalizes jobs left in "running" by a previous
// process. A freshly constructed manager has an empty active map, so any
// on-disk "running" record is an orphan from a crash/restart and can never be
// finalized or cancelled otherwise. It is marked failed so status, cancel, and
// context-health counters stop reporting a phantom running job.
func (m *JobManager) reconcileInterruptedJobs() {
	entries, err := os.ReadDir(filepath.Join(m.cfg.StateDir, "jobs"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := m.Status(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || rec.State != JobRunning {
			continue
		}
		rec.State = JobFailed
		rec.Error = "interrupted: anigate process restarted"
		rec.ExitCode = -1
		rec.FinishedAt = time.Now().UTC()
		if err := m.writeRecord(rec); err != nil {
			continue
		}
		_ = m.events.Append(Event{
			Kind:      "job_finished",
			JobID:     rec.ID,
			Preset:    rec.Preset,
			Workspace: rec.Workspace,
			OK:        false,
			Message:   rec.Error,
			Fields:    map[string]any{"task_id": rec.TaskID, "reconciled": true},
		})
	}
}

func (m *JobManager) RunPreset(ctx context.Context, name string, args map[string]any, async bool) (JobRecord, string, error) {
	preset, ok := m.findPreset(name)
	if !ok {
		return JobRecord{}, "", fmt.Errorf("unknown preset %q", name)
	}
	ws, err := m.policy.workspace(preset.Workspace)
	if err != nil {
		return JobRecord{}, "", err
	}
	if ws.Profile != "operator" && ws.Profile != "agent" {
		return JobRecord{}, "", fmt.Errorf("workspace %q does not allow preset execution", ws.Name)
	}
	command, normalizedArgs, err := RenderPresetCommand(preset, args)
	if err != nil {
		return JobRecord{}, "", err
	}
	spec := JobSpec{
		Kind:       "preset",
		Name:       preset.Name,
		Workspace:  preset.Workspace,
		Cwd:        preset.Cwd,
		Command:    command,
		Args:       normalizedArgs,
		TimeoutSec: preset.TimeoutSec,
		Env:        preset.Env,
		EventTool:  "app.run_preset",
	}
	return m.RunCommand(ctx, spec, async || preset.Async)
}

func (m *JobManager) RunCommand(ctx context.Context, spec JobSpec, async bool) (JobRecord, string, error) {
	job, err := m.newRecord(spec)
	if err != nil {
		return JobRecord{}, "", err
	}
	if err := m.writeRecord(job); err != nil {
		return JobRecord{}, "", err
	}
	m.events.Append(Event{Kind: "job_started", Tool: spec.EventTool, JobID: job.ID, Preset: spec.Name, Workspace: spec.Workspace, OK: true, Fields: map[string]any{"task_id": spec.TaskID}})

	runCtx, cancel := context.WithCancel(ctx)
	m.registerActive(job.ID, cancel)
	if async {
		go m.run(runCtx, spec, job)
		return job, "", nil
	}
	finished := m.run(runCtx, spec, job)
	tail, _ := m.LogsTail(finished.ID, 8192)
	return finished, tail, nil
}

func (m *JobManager) Status(id string) (JobRecord, error) {
	if !validName(id) {
		return JobRecord{}, errors.New("invalid job id")
	}
	b, err := os.ReadFile(filepath.Join(m.cfg.StateDir, "jobs", id+".json"))
	if err != nil {
		return JobRecord{}, err
	}
	var rec JobRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return JobRecord{}, err
	}
	return rec, nil
}

func (m *JobManager) List(limit int, state JobState) ([]JobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	entries, err := os.ReadDir(filepath.Join(m.cfg.StateDir, "jobs"))
	if errors.Is(err, os.ErrNotExist) {
		return []JobRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []JobRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		rec, err := m.Status(id)
		if err != nil {
			continue
		}
		if state != "" && rec.State != state {
			continue
		}
		jobs = append(jobs, rec)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].StartedAt.After(jobs[j].StartedAt)
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

// Count returns the number of job records (optionally filtered by state)
// without the per-call limit clamp that List applies, so internal aggregators
// (context.health, gate.stats) report accurate totals.
func (m *JobManager) Count(state JobState) (int, error) {
	entries, err := os.ReadDir(filepath.Join(m.cfg.StateDir, "jobs"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if state == "" {
			count++
			continue
		}
		rec, err := m.Status(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || rec.State != state {
			continue
		}
		count++
	}
	return count, nil
}

func (m *JobManager) Cancel(id string) (JobRecord, error) {
	if !validName(id) {
		return JobRecord{}, errors.New("invalid job id")
	}
	m.mu.Lock()
	cancel := m.active[id]
	m.mu.Unlock()
	if cancel == nil {
		rec, err := m.Status(id)
		if err != nil {
			return JobRecord{}, err
		}
		if rec.State == JobRunning {
			return rec, errors.New("job is running but not active in this process")
		}
		return rec, fmt.Errorf("job is already %s", rec.State)
	}
	cancel()
	rec, err := m.Status(id)
	if err != nil {
		return JobRecord{}, err
	}
	return rec, nil
}

func (m *JobManager) LogsTail(id string, maxBytes int64) (string, error) {
	rec, err := m.Status(id)
	if err != nil {
		return "", err
	}
	if maxBytes <= 0 {
		maxBytes = 4096
	}
	if maxBytes > m.cfg.MaxJobLogBytes {
		maxBytes = m.cfg.MaxJobLogBytes
	}
	f, err := os.Open(rec.LogPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	if start > 0 {
		return "[truncated head]\n" + string(b), nil
	}
	return string(b), nil
}

func (m *JobManager) findPreset(name string) (Preset, bool) {
	for _, p := range m.cfg.Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

func (m *JobManager) newRecord(spec JobSpec) (JobRecord, error) {
	id, err := newJobID()
	if err != nil {
		return JobRecord{}, err
	}
	cwd, err := m.policy.resolve(spec.Workspace, spec.Cwd)
	if err != nil {
		return JobRecord{}, err
	}
	kind := spec.Kind
	if kind == "" {
		kind = "command"
	}
	return JobRecord{
		ID:        id,
		Kind:      kind,
		Preset:    spec.Name,
		SessionID: spec.SessionID,
		TaskID:    spec.TaskID,
		State:     JobRunning,
		StartedAt: time.Now().UTC(),
		Workspace: spec.Workspace,
		Cwd:       cwd.Abs,
		Command:   append([]string(nil), spec.Command...),
		Args:      spec.Args,
		LogPath:   filepath.Join(m.cfg.StateDir, "logs", id+".log"),
	}, nil
}

func newJobID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:]), nil
}

func (m *JobManager) run(parent context.Context, spec JobSpec, rec JobRecord) JobRecord {
	defer m.unregisterActive(rec.ID)
	if spec.TimeoutSec <= 0 {
		spec.TimeoutSec = 30
	}
	timeout := time.Duration(spec.TimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	logFile, err := os.OpenFile(rec.LogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		rec.State = JobFailed
		rec.Error = err.Error()
		rec.FinishedAt = time.Now().UTC()
		_ = m.writeRecord(rec)
		return rec
	}
	defer logFile.Close()

	_, _ = fmt.Fprintf(logFile, "job_id=%s kind=%s name=%s cwd=%s started=%s\n", rec.ID, rec.Kind, rec.Preset, rec.Cwd, rec.StartedAt.Format(time.RFC3339))
	cmd := exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)
	cmd.Dir = rec.Cwd
	cmd.Env = m.buildEnv(spec.Env)
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}
	lw := &limitedWriter{w: logFile, max: m.cfg.MaxJobLogBytes}
	cmd.Stdout = lw
	cmd.Stderr = lw

	err = cmd.Run()
	rec.FinishedAt = time.Now().UTC()
	rec.Truncated = lw.truncated
	if ctx.Err() == context.DeadlineExceeded {
		rec.State = JobFailed
		rec.Error = "timeout"
		rec.ExitCode = -1
	} else if ctx.Err() == context.Canceled {
		rec.State = JobCancelled
		rec.Error = "cancelled"
		rec.ExitCode = -1
	} else if err != nil {
		rec.State = JobFailed
		rec.Error = err.Error()
		if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
			rec.ExitCode = exitErr.ExitCode()
		} else {
			rec.ExitCode = -1
		}
	} else {
		rec.State = JobDone
		rec.ExitCode = 0
	}
	if rec.Truncated {
		_, _ = fmt.Fprintln(logFile, "\n[anigate: log truncated]")
	}
	_ = m.writeRecord(rec)
	m.events.Append(Event{
		Kind:      "job_finished",
		Tool:      spec.EventTool,
		JobID:     rec.ID,
		Preset:    spec.Name,
		Workspace: spec.Workspace,
		OK:        rec.State == JobDone,
		Message:   rec.Error,
		Fields:    map[string]any{"task_id": spec.TaskID},
	})
	if spec.OnFinish != nil {
		spec.OnFinish(rec)
	}
	return rec
}

func (m *JobManager) buildEnv(extra map[string]string) []string {
	env := []string{"PATH=" + pathEnv()}
	if m.cfg.IsolatedHome {
		home := filepath.Join(m.cfg.StateDir, "home")
		_ = os.MkdirAll(home, 0o700)
		env = append(env, "HOME="+home)
	}
	for k, v := range extra {
		if validEnvName(k) {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func (m *JobManager) registerActive(id string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[id] = cancel
}

func (m *JobManager) unregisterActive(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.active, id)
}

func (m *JobManager) writeRecord(rec JobRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.cfg.StateDir, "jobs", rec.ID+".json")
	tmp := path + ".tmp"
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type limitedWriter struct {
	w         io.Writer
	max       int64
	written   int64
	truncated bool
	buf       bytes.Buffer
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.max <= 0 {
		w.max = defaultMaxJobLogBytes
	}
	remaining := w.max - w.written
	if remaining <= 0 {
		w.truncated = true
		return len(p), nil
	}
	write := p
	if int64(len(write)) > remaining {
		write = write[:remaining]
		w.truncated = true
	}
	n, err := w.w.Write(write)
	w.written += int64(n)
	if n < len(write) && err == nil {
		err = io.ErrShortWrite
	}
	return len(p), err
}

func commandPreview(cmd []string) string {
	parts := make([]string, len(cmd))
	for i, p := range cmd {
		if strings.ContainsAny(p, " \t\n") {
			parts[i] = fmt.Sprintf("%q", p)
		} else {
			parts[i] = p
		}
	}
	return strings.Join(parts, " ")
}
