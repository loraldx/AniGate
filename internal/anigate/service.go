package anigate

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Service struct {
	cfg         Config
	productLine ProductLine
	policy      pathPolicy
	jobs        *JobManager
	events      *EventLog
	log         *slog.Logger
	sessionMu   sync.Mutex
}

func NewService(cfg Config, log *slog.Logger) (*Service, error) {
	return NewServiceWithProductLine(cfg, log, ProductLineMax)
}

func NewServiceWithProductLine(cfg Config, log *slog.Logger, productLine ProductLine) (*Service, error) {
	line, err := normalizeProductLine(productLine)
	if err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	events, err := NewEventLog(cfg.StateDir, log)
	if err != nil {
		return nil, err
	}
	policy := newPathPolicy(cfg.Workspaces, cfg.StateDir)
	jobs, err := NewJobManager(cfg, policy, events, log)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, productLine: line, policy: policy, jobs: jobs, events: events, log: log}, nil
}

func (s *Service) Tools() []MCPTool {
	tools := allTools()
	if s.productLine != ProductLineMini {
		return tools
	}
	filtered := tools[:0]
	for _, tool := range tools {
		if s.toolAllowedForProduct(tool.Name) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func allTools() []MCPTool {
	return []MCPTool{
		{Name: "policy.info", Description: "Return AniGate capabilities, limits, workspaces, profiles, presets, and agents.", InputSchema: objectSchema(map[string]any{})},
		{Name: "sys.info", Description: "Return bounded system and AniGate configuration information.", InputSchema: objectSchema(map[string]any{})},
		{Name: "fs.list", Description: "List a directory inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"max_entries": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": 500,
			},
		})},
		{Name: "fs.read", Description: "Read a bounded byte range from a file inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"offset":    map[string]any{"type": "integer", "minimum": 0},
			"max_bytes": map[string]any{"type": "integer", "minimum": 1},
		})},
		{Name: "fs.stat", Description: "Return file metadata inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
		})},
		{Name: "fs.tree", Description: "Return a depth-limited directory tree inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace":   map[string]any{"type": "string"},
			"path":        map[string]any{"type": "string"},
			"depth":       map[string]any{"type": "integer", "minimum": 0, "maximum": 8},
			"max_entries": map[string]any{"type": "integer", "minimum": 1, "maximum": 2000},
		})},
		{Name: "file.search", Description: "Search text files inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace":      map[string]any{"type": "string"},
			"path":           map[string]any{"type": "string"},
			"query":          map[string]any{"type": "string"},
			"case_sensitive": map[string]any{"type": "boolean"},
			"max_results":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		})},
		{Name: "artifact.list", Description: "List local artifacts created from large AniGate outputs.", InputSchema: objectSchema(map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			"kind":  map[string]any{"type": "string"},
		})},
		{Name: "artifact.read_range", Description: "Read a bounded byte range from a local artifact.", InputSchema: objectSchema(map[string]any{
			"artifact_id": map[string]any{"type": "string"},
			"offset":      map[string]any{"type": "integer", "minimum": 0},
			"max_bytes":   map[string]any{"type": "integer", "minimum": 1},
		})},
		{Name: "artifact.search", Description: "Search local artifact text without returning the whole artifact.", InputSchema: objectSchema(map[string]any{
			"query":          map[string]any{"type": "string"},
			"kind":           map[string]any{"type": "string"},
			"case_sensitive": map[string]any{"type": "boolean"},
			"max_results":    map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		})},
		{Name: "artifact.stats", Description: "Summarize local artifact count and bytes.", InputSchema: objectSchema(map[string]any{})},
		{Name: "fs.write_preview", Description: "Preview a file write as a diff; does not modify disk.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
			"create":    map[string]any{"type": "boolean"},
		})},
		{Name: "file.edit_apply", Description: "Apply a direct Web GPT single-file edit inside a writable workspace when no configured agent should be used.", InputSchema: objectSchema(map[string]any{
			"workspace":         map[string]any{"type": "string"},
			"path":              map[string]any{"type": "string"},
			"content":           map[string]any{"type": "string"},
			"create":            map[string]any{"type": "boolean"},
			"expected_sha256":   map[string]any{"type": "string"},
			"expected_old_text": map[string]any{"type": "string"},
		})},
		{Name: "git.status", Description: "Return git status for a repository inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"max_bytes": map[string]any{"type": "integer", "minimum": 1},
		})},
		{Name: "git.diff", Description: "Return a bounded git diff for a repository inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"cached":    map[string]any{"type": "boolean"},
			"max_bytes": map[string]any{"type": "integer", "minimum": 1},
			"paths":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		})},
		{Name: "git.log", Description: "Return bounded git log output for a repository inside an allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			"max_bytes": map[string]any{"type": "integer", "minimum": 1},
			"paths":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		})},
		{Name: "git.show", Description: "Return bounded git show output for a safe revision.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
			"rev":       map[string]any{"type": "string"},
			"max_bytes": map[string]any{"type": "integer", "minimum": 1},
			"paths":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		})},
		{Name: "patch.apply", Description: "Apply or check a unified patch inside a writable allowed workspace.", InputSchema: objectSchema(map[string]any{
			"workspace":  map[string]any{"type": "string"},
			"path":       map[string]any{"type": "string"},
			"patch":      map[string]any{"type": "string"},
			"check_only": map[string]any{"type": "boolean"},
		})},
		{Name: "audit.events_tail", Description: "Read recent AniGate events from the file-backed audit log.", InputSchema: objectSchema(map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			"kind":  map[string]any{"type": "string"},
			"tool":  map[string]any{"type": "string"},
		})},
		{Name: "audit.summary", Description: "Summarize recent AniGate audit events by kind and tool.", InputSchema: objectSchema(map[string]any{
			"since_sec": map[string]any{"type": "integer", "minimum": 1},
		})},
		{Name: "workspace.snapshot", Description: "Return a bounded workspace snapshot with tree, git, jobs, agents, audit, and context health.", InputSchema: objectSchema(map[string]any{
			"workspace": map[string]any{"type": "string"},
			"path":      map[string]any{"type": "string"},
		})},
		{Name: "gate.stats", Description: "Return AniGate local state counters and context-health inputs.", InputSchema: objectSchema(map[string]any{})},
		{Name: "gate.doctor", Description: "Run structured AniGate configuration, workspace, project, GitHub CLI, and state-dir checks.", InputSchema: objectSchema(map[string]any{
			"project": map[string]any{"type": "string"},
		})},
		{Name: "context.health", Description: "Estimate AniGate-related context pressure and recommend handoff when needed.", InputSchema: objectSchema(map[string]any{})},
		{Name: "handoff.create", Description: "Create a compact handoff package and next-chat prompt.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
			"title":   map[string]any{"type": "string"},
			"goal":    map[string]any{"type": "string"},
			"notes":   map[string]any{"type": "string"},
		})},
		{Name: "handoff.resume", Description: "Resume from a handoff id with a compact summary and searchable pointers.", InputSchema: objectSchema(map[string]any{
			"handoff_id": map[string]any{"type": "string"},
		})},
		{Name: "handoff.search", Description: "Search handoff records without loading full history.", InputSchema: objectSchema(map[string]any{
			"query":       map[string]any{"type": "string"},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		})},
		{Name: "handoff.digest", Description: "Return a short digest for a handoff or task.", InputSchema: objectSchema(map[string]any{
			"handoff_id": map[string]any{"type": "string"},
			"task_id":    map[string]any{"type": "string"},
		})},
		{Name: "app.run_preset", Description: "Run a configured preset command; no arbitrary shell is exposed.", InputSchema: objectSchema(map[string]any{
			"name":  map[string]any{"type": "string"},
			"async": map[string]any{"type": "boolean"},
			"args":  map[string]any{"type": "object"},
		})},
		{Name: "job.list", Description: "List recent file-backed jobs.", InputSchema: objectSchema(map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
			"state": map[string]any{"type": "string"},
		})},
		{Name: "job.status", Description: "Read file-backed status for a job.", InputSchema: objectSchema(map[string]any{
			"job_id": map[string]any{"type": "string"},
		})},
		{Name: "job.cancel", Description: "Cancel an active job in this AniGate process.", InputSchema: objectSchema(map[string]any{
			"job_id": map[string]any{"type": "string"},
		})},
		{Name: "job.logs_tail", Description: "Return the tail of a job log.", InputSchema: objectSchema(map[string]any{
			"job_id": map[string]any{"type": "string"},
			"bytes":  map[string]any{"type": "integer", "minimum": 1},
		})},
		{Name: "agent.session_start", Description: "Start a file-backed long-running agent conversation session.", InputSchema: objectSchema(map[string]any{
			"agent":   map[string]any{"type": "string"},
			"path":    map[string]any{"type": "string"},
			"title":   map[string]any{"type": "string"},
			"task_id": map[string]any{"type": "string"},
		})},
		{Name: "agent.message_send", Description: "Append a user message and run the configured agent as a cancellable job.", InputSchema: objectSchema(map[string]any{
			"session_id": map[string]any{"type": "string"},
			"message":    map[string]any{"type": "string"},
			"async":      map[string]any{"type": "boolean"},
		})},
		{Name: "agent.session_status", Description: "Read agent session state and last job.", InputSchema: objectSchema(map[string]any{
			"session_id": map[string]any{"type": "string"},
		})},
		{Name: "agent.messages_tail", Description: "Read recent messages from an agent session.", InputSchema: objectSchema(map[string]any{
			"session_id": map[string]any{"type": "string"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		})},
		{Name: "agent.session_list", Description: "List recent agent sessions.", InputSchema: objectSchema(map[string]any{
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		})},
		{Name: "project.list", Description: "List configured remote Git project allowlist entries.", InputSchema: objectSchema(map[string]any{})},
		{Name: "project.ensure", Description: "Clone or fetch an allowlisted remote Git project.", InputSchema: objectSchema(map[string]any{
			"project": map[string]any{"type": "string"},
		})},
		{Name: "project.open", Description: "Return local coordinates for an allowlisted project.", InputSchema: objectSchema(map[string]any{
			"project": map[string]any{"type": "string"},
		})},
		{Name: "project.preflight", Description: "Check local project git state before task or publish work.", InputSchema: objectSchema(map[string]any{
			"project": map[string]any{"type": "string"},
		})},
		{Name: "project.snapshot", Description: "Return bounded project status, recent log, and task pointers.", InputSchema: objectSchema(map[string]any{
			"project": map[string]any{"type": "string"},
		})},
		{Name: "project.lock_status", Description: "Show active task locks for a project.", InputSchema: objectSchema(map[string]any{
			"project": map[string]any{"type": "string"},
		})},
		{Name: "task.start", Description: "Create a locked branch/worktree task for an allowlisted project.", InputSchema: objectSchema(map[string]any{
			"project":     map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"slug":        map[string]any{"type": "string"},
			"base_branch": map[string]any{"type": "string"},
		})},
		{Name: "task.status", Description: "Read file-backed task state.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
		})},
		{Name: "task.recover", Description: "Recover task state, workspace coordinates, and related pointers.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
		})},
		{Name: "task.digest", Description: "Return a compact task digest for continuation.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
		})},
		{Name: "task.finish_preview", Description: "Preview task diff/status before publish.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
		})},
		{Name: "task.commit_preview", Description: "Preview task changes and return a diff fingerprint before committing.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
			"message": map[string]any{"type": "string"},
		})},
		{Name: "task.commit", Description: "Commit task worktree changes after a matching task.commit_preview fingerprint.", InputSchema: objectSchema(map[string]any{
			"task_id":              map[string]any{"type": "string"},
			"message":              map[string]any{"type": "string"},
			"expected_diff_sha256": map[string]any{"type": "string"},
		})},
		{Name: "task.timeline", Description: "Read recent audit events related to a task.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
			"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		})},
		{Name: "task.search", Description: "Search task records and timeline snippets.", InputSchema: objectSchema(map[string]any{
			"query":       map[string]any{"type": "string"},
			"max_results": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		})},
		{Name: "publish.preview", Description: "Create a short-lived publish confirmation token for a task.", InputSchema: objectSchema(map[string]any{
			"task_id": map[string]any{"type": "string"},
		})},
		{Name: "publish.branch", Description: "Push a task branch after publish.preview confirmation.", InputSchema: objectSchema(map[string]any{
			"task_id":       map[string]any{"type": "string"},
			"confirm_token": map[string]any{"type": "string"},
		})},
		{Name: "publish.pr_create", Description: "Create a GitHub PR after publish.preview confirmation.", InputSchema: objectSchema(map[string]any{
			"task_id":       map[string]any{"type": "string"},
			"confirm_token": map[string]any{"type": "string"},
			"title":         map[string]any{"type": "string"},
			"body":          map[string]any{"type": "string"},
			"draft":         map[string]any{"type": "boolean"},
		})},
	}
}

func objectSchema(props map[string]any) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
}

func (s *Service) CallTool(name string, raw json.RawMessage) (any, error) {
	var args map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if err := s.requireToolForProduct(name); err != nil {
		ev := Event{Kind: "tool_call", Tool: name, OK: false, Message: errorString(err)}
		enrichToolCallEvent(&ev, args)
		s.events.Append(ev)
		return nil, err
	}
	var result any
	var err error
	switch name {
	case "policy.info":
		result, err = s.policyInfo()
	case "sys.info":
		result, err = s.sysInfo()
	case "fs.list":
		result, err = s.fsList(args)
	case "fs.read":
		result, err = s.fsRead(args)
	case "fs.stat":
		result, err = s.fsStat(args)
	case "fs.tree":
		result, err = s.fsTree(args)
	case "file.search":
		result, err = s.fileSearch(args)
	case "artifact.list":
		result, err = s.artifactList(args)
	case "artifact.read_range":
		result, err = s.artifactReadRange(args)
	case "artifact.search":
		result, err = s.artifactSearch(args)
	case "artifact.stats":
		result, err = s.artifactStats()
	case "fs.write_preview":
		result, err = s.fsWritePreview(args)
	case "file.edit_apply":
		result, err = s.fileEditApply(args)
	case "git.status":
		result, err = s.gitStatus(args)
	case "git.diff":
		result, err = s.gitDiff(args)
	case "git.log":
		result, err = s.gitLog(args)
	case "git.show":
		result, err = s.gitShow(args)
	case "patch.apply":
		result, err = s.patchApply(args)
	case "audit.events_tail":
		result, err = s.auditEventsTail(args)
	case "audit.summary":
		result, err = s.auditSummary(args)
	case "workspace.snapshot":
		result, err = s.workspaceSnapshot(args)
	case "gate.stats":
		result, err = s.gateStats()
	case "gate.doctor":
		result, err = s.gateDoctor(args)
	case "context.health":
		result, err = s.contextHealth()
	case "handoff.create":
		result, err = s.handoffCreate(args)
	case "handoff.resume":
		result, err = s.handoffResume(args)
	case "handoff.search":
		result, err = s.handoffSearch(args)
	case "handoff.digest":
		result, err = s.handoffDigest(args)
	case "app.run_preset":
		result, err = s.appRunPreset(args)
	case "job.list":
		result, err = s.jobList(args)
	case "job.status":
		result, err = s.jobStatus(args)
	case "job.cancel":
		result, err = s.jobCancel(args)
	case "job.logs_tail":
		result, err = s.jobLogsTail(args)
	case "agent.session_start":
		result, err = s.agentSessionStart(args)
	case "agent.message_send":
		result, err = s.agentMessageSend(args)
	case "agent.session_status":
		result, err = s.agentSessionStatus(args)
	case "agent.messages_tail":
		result, err = s.agentMessagesTail(args)
	case "agent.session_list":
		result, err = s.agentSessionList(args)
	case "project.list":
		result, err = s.projectList()
	case "project.ensure":
		result, err = s.projectEnsure(args)
	case "project.open":
		result, err = s.projectOpen(args)
	case "project.preflight":
		result, err = s.projectPreflight(args)
	case "project.snapshot":
		result, err = s.projectSnapshot(args)
	case "project.lock_status":
		result, err = s.projectLockStatus(args)
	case "task.start":
		result, err = s.taskStart(args)
	case "task.status":
		result, err = s.taskStatus(args)
	case "task.recover":
		result, err = s.taskRecover(args)
	case "task.digest":
		result, err = s.taskDigest(args)
	case "task.finish_preview":
		result, err = s.taskFinishPreview(args)
	case "task.commit_preview":
		result, err = s.taskCommitPreview(args)
	case "task.commit":
		result, err = s.taskCommit(args)
	case "task.timeline":
		result, err = s.taskTimeline(args)
	case "task.search":
		result, err = s.taskSearch(args)
	case "publish.preview":
		result, err = s.publishPreview(args)
	case "publish.branch":
		result, err = s.publishBranch(args)
	case "publish.pr_create":
		result, err = s.publishPRCreate(args)
	default:
		err = fmt.Errorf("unknown tool %q", name)
	}
	ev := Event{Kind: "tool_call", Tool: name, OK: err == nil, Message: errorString(err)}
	if err != nil {
		enrichToolCallEvent(&ev, args)
	}
	s.events.Append(ev)
	return result, err
}

// enrichToolCallEvent attaches the caller-supplied workspace, path, and common
// identifiers to a failed tool-call audit event so gate rejections and
// path-escape probes are attributable during forensic review.
func enrichToolCallEvent(ev *Event, args map[string]any) {
	if args == nil {
		return
	}
	if ws := stringArg(args, "workspace"); ws != "" {
		ev.Workspace = ws
	}
	if p := stringArg(args, "path"); p != "" {
		ev.Path = p
	}
	fields := map[string]any{}
	for _, key := range []string{"project", "task_id", "session_id", "name"} {
		if v := stringArg(args, key); v != "" {
			fields[key] = v
		}
	}
	if len(fields) > 0 {
		ev.Fields = fields
	}
}

func (s *Service) sysInfo() (map[string]any, error) {
	host, _ := os.Hostname()
	uptime := ""
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		uptime = strings.Fields(string(b))[0]
	}
	workspaces := make([]map[string]any, 0, len(s.cfg.Workspaces))
	for _, ws := range s.cfg.Workspaces {
		workspaces = append(workspaces, map[string]any{
			"name":      ws.Name,
			"path":      ws.Path,
			"read_only": ws.ReadOnly,
			"profile":   ws.Profile,
		})
	}
	presets := make([]map[string]any, 0, len(s.cfg.Presets))
	for _, p := range s.cfg.Presets {
		presets = append(presets, map[string]any{
			"name":        p.Name,
			"description": p.Description,
			"workspace":   p.Workspace,
			"async":       p.Async,
			"command":     commandPreview(p.Command),
		})
	}
	return map[string]any{
		"server":         "AniGate",
		"version":        Version,
		"version_scheme": VersionScheme,
		"product_line":   s.productLine,
		"mode":           "controlled-linux-gateway",
		"hostname":       host,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
		"go_version":     runtime.Version(),
		"uptime_proc":    uptime,
		"state_dir":      s.cfg.StateDir,
		"isolated_home":  s.cfg.IsolatedHome,
		"workspaces":     workspaces,
		"projects":       len(s.cfg.Projects),
		"presets":        presets,
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *Service) fsList(args map[string]any) (map[string]any, error) {
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	maxEntries := intArgDefault(args, "max_entries", 100)
	if maxEntries <= 0 {
		maxEntries = 100
	}
	if maxEntries > 500 {
		maxEntries = 500
	}
	entries, err := os.ReadDir(rp.Abs)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})
	out := make([]map[string]any, 0, min(len(entries), maxEntries))
	for i, e := range entries {
		if i >= maxEntries {
			break
		}
		info, _ := e.Info()
		size := int64(0)
		mod := ""
		if info != nil {
			size = info.Size()
			mod = info.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, map[string]any{
			"name":     e.Name(),
			"path":     filepath.ToSlash(filepath.Join(rp.Rel, e.Name())),
			"is_dir":   e.IsDir(),
			"size":     size,
			"modified": mod,
		})
	}
	return map[string]any{
		"workspace": rp.Workspace.Name,
		"path":      rp.Rel,
		"entries":   out,
		"truncated": len(entries) > maxEntries,
	}, nil
}

func (s *Service) fsRead(args map[string]any) (map[string]any, error) {
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArg(args, "path"))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(rp.Abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	offset := int64ArgDefault(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	maxBytes := int64ArgDefault(args, "max_bytes", s.cfg.MaxReadBytes)
	if maxBytes <= 0 || maxBytes > s.cfg.MaxReadBytes {
		maxBytes = s.cfg.MaxReadBytes
	}
	f, err := os.Open(rp.Abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, maxBytes+1)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	truncated := int64(n) > maxBytes
	if truncated {
		n = int(maxBytes)
	}
	out := map[string]any{
		"workspace": rp.Workspace.Name,
		"path":      rp.Rel,
		"offset":    offset,
		"bytes":     n,
		"size":      info.Size(),
		"truncated": truncated || offset+int64(n) < info.Size(),
		"text":      string(buf[:n]),
	}
	if out["truncated"].(bool) && info.Size() <= s.cfg.MaxArtifactBytes {
		if all, err := os.ReadFile(rp.Abs); err == nil && !looksBinary(all) {
			_, _, ref, err := s.boundedTextArtifact("fs.read", rp.Rel, string(all), maxBytes, map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel})
			if err != nil {
				return nil, err
			}
			s.addArtifactFields(out, ref)
		}
	}
	return out, nil
}

func (s *Service) fileSearch(args map[string]any) (map[string]any, error) {
	query := stringArg(args, "query")
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	maxResults := intArgDefault(args, "max_results", s.cfg.MaxSearchResults)
	if maxResults <= 0 {
		maxResults = s.cfg.MaxSearchResults
	}
	if maxResults > 200 {
		maxResults = 200
	}
	caseSensitive := boolArg(args, "case_sensitive")
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(query)
	}
	var results []map[string]any
	var scanned int
	err = filepath.WalkDir(rp.Abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || len(results) >= maxResults {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".anigate" {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip symlinks: WalkDir Lstats entries, so a link's own size passes the
		// byte guard while os.ReadFile would follow it and read the target's
		// contents — an out-of-tree link (e.g. -> /etc/passwd) would bypass path
		// confinement. Every other content reader routes through policy.resolve,
		// which rejects such escapes; file.search must not be the exception.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > s.cfg.MaxSearchFileBytes {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || looksBinary(b) {
			return nil
		}
		scanned++
		// The file is already fully in memory (bounded by MaxSearchFileBytes);
		// split directly so lines beyond bufio.Scanner's 64 KiB token limit
		// cannot silently stop the scan mid-file.
		lineNo := 0
		for _, line := range strings.Split(string(b), "\n") {
			lineNo++
			line = strings.TrimSuffix(line, "\r")
			hay := line
			if !caseSensitive {
				hay = strings.ToLower(line)
			}
			if strings.Contains(hay, needle) {
				rel, _ := filepath.Rel(rp.Workspace.Path, path)
				results = append(results, map[string]any{
					"path":    filepath.ToSlash(rel),
					"line":    lineNo,
					"preview": trimPreview(line, 240),
				})
				if len(results) >= maxResults {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace": rp.Workspace.Name,
		"path":      rp.Rel,
		"query":     query,
		"scanned":   scanned,
		"results":   results,
		"truncated": len(results) >= maxResults,
	}, nil
}

func (s *Service) appRunPreset(args map[string]any) (map[string]any, error) {
	name := stringArg(args, "name")
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	presetArgs, err := objectArg(args, "args")
	if err != nil {
		return nil, err
	}
	job, tail, err := s.jobs.RunPreset(contextWithBackground(), name, presetArgs, boolArg(args, "async"))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"job": job}
	if tail != "" {
		out["logs_tail"] = tail
	}
	return out, nil
}

func (s *Service) auditEventsTail(args map[string]any) (map[string]any, error) {
	limit := intArgDefault(args, "limit", 50)
	events, err := s.events.Tail(limit, EventFilter{
		Kind: stringArg(args, "kind"),
		Tool: stringArg(args, "tool"),
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"events": events,
		"count":  len(events),
	}
	if b, err := json.Marshal(events); err == nil {
		_, _, ref, err := s.boundedTextArtifact("audit.events_tail", "events", string(b), s.cfg.MaxReadBytes, nil)
		if err != nil {
			return nil, err
		}
		s.addArtifactFields(out, ref)
	}
	return out, nil
}

func (s *Service) jobList(args map[string]any) (map[string]any, error) {
	state := JobState(stringArg(args, "state"))
	jobs, err := s.jobs.List(intArgDefault(args, "limit", 50), state)
	if err != nil {
		return nil, err
	}
	return map[string]any{"jobs": jobs, "count": len(jobs)}, nil
}

func (s *Service) jobStatus(args map[string]any) (map[string]any, error) {
	id := stringArg(args, "job_id")
	if id == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	rec, err := s.jobs.Status(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"job": rec}, nil
}

func (s *Service) jobCancel(args map[string]any) (map[string]any, error) {
	id := stringArg(args, "job_id")
	if id == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	rec, err := s.jobs.Cancel(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"job": rec, "cancel_requested": true}, nil
}

func (s *Service) jobLogsTail(args map[string]any) (map[string]any, error) {
	id := stringArg(args, "job_id")
	if id == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	maxBytes := int64ArgDefault(args, "bytes", 4096)
	tail, err := s.jobs.LogsTail(id, maxBytes)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"job_id": id, "text": tail}
	if rec, err := s.jobs.Status(id); err == nil {
		if full, err := os.ReadFile(rec.LogPath); err == nil {
			_, _, ref, err := s.boundedTextArtifact("job.logs_tail", id, string(full), maxBytes, map[string]any{"job_id": id})
			if err != nil {
				return nil, err
			}
			s.addArtifactFields(out, ref)
		}
	}
	return out, nil
}

func looksBinary(b []byte) bool {
	limit := min(len(b), 4096)
	for i := 0; i < limit; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

func trimPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func stringArgDefault(args map[string]any, key, def string) string {
	if s := stringArg(args, key); s != "" {
		return s
	}
	return def
}

func intArgDefault(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return def
}

func int64ArgDefault(args map[string]any, key string, def int64) int64 {
	if v, ok := args[key].(float64); ok {
		return int64(v)
	}
	return def
}

func boolArg(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func objectArg(args map[string]any, key string) (map[string]any, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return obj, nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
