package anigate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type TaskRecord struct {
	ID          string    `json:"id"`
	Project     string    `json:"project"`
	Title       string    `json:"title,omitempty"`
	State       string    `json:"state"`
	Workspace   string    `json:"workspace"`
	Worktree    string    `json:"worktree"`
	WorktreeRel string    `json:"worktree_rel"`
	RepoPath    string    `json:"repo_path"`
	Branch      string    `json:"branch"`
	BaseBranch  string    `json:"base_branch"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type publishTokenRecord struct {
	Token     string    `json:"token"`
	TaskID    string    `json:"task_id"`
	Project   string    `json:"project"`
	Branch    string    `json:"branch"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) projectList() (map[string]any, error) {
	projects := make([]map[string]any, 0, len(s.cfg.Projects))
	for _, p := range s.cfg.Projects {
		rp, _ := s.resolveProjectPath(p)
		projects = append(projects, map[string]any{
			"name":           p.Name,
			"description":    p.Description,
			"workspace":      p.Workspace,
			"path":           p.Path,
			"local_path":     rp.Abs,
			"remote_url":     redactRemoteURL(p.RemoteURL),
			"default_branch": p.DefaultBranch,
			"provider":       p.Provider,
			"allow_push":     p.AllowPush,
			"allow_pr":       p.AllowPR,
			"default_agent":  p.DefaultAgent,
		})
	}
	return map[string]any{"projects": projects, "count": len(projects)}, nil
}

func (s *Service) projectEnsure(args map[string]any) (map[string]any, error) {
	project, err := s.requireProject(stringArg(args, "project"))
	if err != nil {
		return nil, err
	}
	if err := s.workspaceAllows(project.Workspace, "write"); err != nil {
		return nil, err
	}
	rp, err := s.resolveProjectPath(project)
	if err != nil {
		return nil, err
	}
	created := false
	if _, err := os.Stat(filepath.Join(rp.Abs, ".git")); err == nil {
		// These two commands carry the remote URL in args/stderr; they must go
		// through the sanitized runner so embedded credentials never reach errors.
		if err := runGitExternal(rp.Abs, "remote", "set-url", "origin", project.RemoteURL); err != nil {
			return nil, err
		}
		if err := runGitNetwork(rp.Abs, "fetch", "--all", "--prune"); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(rp.Abs), 0o755); err != nil {
			return nil, err
		}
		if _, err := os.Stat(rp.Abs); err == nil {
			return nil, fmt.Errorf("project path exists but is not a git repository")
		}
		if err := runGitNetwork(filepath.Dir(rp.Abs), "clone", project.RemoteURL, rp.Abs); err != nil {
			return nil, err
		}
		created = true
	}
	s.events.Append(Event{Kind: "project_ensured", Tool: "project.ensure", Workspace: project.Workspace, OK: true, Fields: map[string]any{"project": project.Name, "created": created}})
	return map[string]any{
		"project":    projectInfo(project, rp),
		"created":    created,
		"remote_url": redactRemoteURL(project.RemoteURL),
	}, nil
}

func (s *Service) projectOpen(args map[string]any) (map[string]any, error) {
	project, err := s.requireProject(stringArg(args, "project"))
	if err != nil {
		return nil, err
	}
	rp, err := s.resolveProjectPath(project)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": projectInfo(project, rp)}, nil
}

func (s *Service) projectPreflight(args map[string]any) (map[string]any, error) {
	project, err := s.requireProject(stringArg(args, "project"))
	if err != nil {
		return nil, err
	}
	rp, err := s.resolveProjectPath(project)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"project": projectInfo(project, rp)}
	if _, err := os.Stat(filepath.Join(rp.Abs, ".git")); err != nil {
		checks := s.doctorProjectChecks(project)
		out["ok"] = false
		out["message"] = "project is not cloned"
		out["checks"] = checks
		return out, nil
	}
	status, _ := s.runGitOutput(rp.Abs, "status", "--porcelain=v1", "--branch")
	branch, _ := s.runGitOutput(rp.Abs, "branch", "--show-current")
	remote, _ := s.runGitOutput(rp.Abs, "remote", "get-url", "origin")
	out["ok"] = true
	out["branch"] = strings.TrimSpace(branch)
	out["status"] = trimPreview(status, 4000)
	out["remote_url"] = redactRemoteURL(strings.TrimSpace(remote))
	out["remote_matches_allowlist"] = strings.TrimSpace(remote) == project.RemoteURL
	checks := s.doctorProjectChecks(project)
	out["checks"] = checks
	out["ok"] = checksOK(checks)
	return out, nil
}

func (s *Service) projectSnapshot(args map[string]any) (map[string]any, error) {
	project, err := s.requireProject(stringArg(args, "project"))
	if err != nil {
		return nil, err
	}
	rp, err := s.resolveProjectPath(project)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"project": projectInfo(project, rp)}
	if _, err := os.Stat(filepath.Join(rp.Abs, ".git")); err == nil {
		status, _, _, err := s.boundedTextArtifact("project.snapshot", project.Name+" status", mustString(s.runGitOutput(rp.Abs, "status", "--porcelain=v1", "--branch")), 8192, map[string]any{"project": project.Name})
		if err != nil {
			return nil, err
		}
		logOut, _, ref, err := s.boundedTextArtifact("project.snapshot", project.Name+" log", mustString(s.runGitOutput(rp.Abs, "log", "--oneline", "--decorate", "-n", "20")), 8192, map[string]any{"project": project.Name})
		if err != nil {
			return nil, err
		}
		out["status"] = status
		out["log"] = logOut
		s.addArtifactFields(out, ref)
	}
	tasks, _ := s.listTasks(project.Name, 20)
	out["tasks"] = tasks
	out["task_count"] = len(tasks)
	return out, nil
}

func (s *Service) projectLockStatus(args map[string]any) (map[string]any, error) {
	project, err := s.requireProject(stringArg(args, "project"))
	if err != nil {
		return nil, err
	}
	tasks, err := s.listTasks(project.Name, 200)
	if err != nil {
		return nil, err
	}
	var active []TaskRecord
	for _, task := range tasks {
		if task.State == "active" {
			active = append(active, task)
		}
	}
	return map[string]any{"project": project.Name, "active_locks": active, "count": len(active)}, nil
}

func (s *Service) taskStart(args map[string]any) (map[string]any, error) {
	project, err := s.requireProject(stringArg(args, "project"))
	if err != nil {
		return nil, err
	}
	if err := s.workspaceAllows(project.Workspace, "write"); err != nil {
		return nil, err
	}
	ensure, err := s.projectEnsure(map[string]any{"project": project.Name})
	if err != nil {
		return nil, err
	}
	_ = ensure
	rp, err := s.resolveProjectPath(project)
	if err != nil {
		return nil, err
	}
	base := stringArgDefault(args, "base_branch", project.DefaultBranch)
	if !validGitRev(base) {
		return nil, fmt.Errorf("invalid base_branch")
	}
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(stringArg(args, "title"))
	slug := safeSlug(stringArgDefault(args, "slug", title))
	if slug == "" {
		slug = "task"
	}
	branch := "anigate/" + id + "-" + slug
	if len(branch) > 120 {
		branch = branch[:120]
	}
	worktreesRoot := filepath.Join(filepath.Dir(rp.Abs), ".anigate-worktrees", project.Name)
	worktree := filepath.Join(worktreesRoot, id)
	if err := os.MkdirAll(worktreesRoot, 0o700); err != nil {
		return nil, err
	}
	if err := runGitExternal(rp.Abs, "worktree", "add", "-b", branch, worktree, base); err != nil {
		return nil, err
	}
	worktreeRel, err := filepath.Rel(filepath.Clean(s.mustWorkspace(project.Workspace).Path), worktree)
	if err != nil || strings.HasPrefix(worktreeRel, "..") {
		worktreeRel = worktree
	}
	now := time.Now().UTC()
	task := TaskRecord{
		ID:          id,
		Project:     project.Name,
		Title:       title,
		State:       "active",
		Workspace:   project.Workspace,
		Worktree:    worktree,
		WorktreeRel: filepath.ToSlash(worktreeRel),
		RepoPath:    rp.Abs,
		Branch:      branch,
		BaseBranch:  base,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.writeTask(task); err != nil {
		return nil, err
	}
	s.events.Append(Event{Kind: "task_started", Tool: "task.start", Workspace: project.Workspace, OK: true, Fields: map[string]any{"task_id": id, "project": project.Name, "branch": branch}})
	return map[string]any{"task": task, "next": []string{"task.status", "agent.session_start", "task.finish_preview", "handoff.create"}}, nil
}

func (s *Service) taskStatus(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"task": task}
	if _, err := os.Stat(filepath.Join(task.Worktree, ".git")); err == nil {
		status, _ := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "--branch")
		out["status"] = trimPreview(status, 4000)
	}
	return out, nil
}

func (s *Service) taskRecover(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	timeline, _ := s.taskTimeline(map[string]any{"task_id": task.ID, "limit": float64(50)})
	return map[string]any{
		"task":        task,
		"timeline":    timeline,
		"coordinates": map[string]any{"workspace": task.Workspace, "path": task.WorktreeRel},
		"next":        []string{"task.digest", "agent.session_start", "task.finish_preview", "handoff.create"},
	}, nil
}

func (s *Service) taskDigest(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	status, _ := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "--branch")
	diffStat, _ := s.runGitOutput(task.Worktree, "diff", "--stat")
	digest := strings.TrimSpace(fmt.Sprintf("Task %s (%s)\nProject: %s\nBranch: %s\nState: %s\nStatus:\n%s\nDiff stat:\n%s",
		task.ID, task.Title, task.Project, task.Branch, task.State, trimPreview(status, 4000), trimPreview(diffStat, 4000)))
	return map[string]any{"task": task, "digest": digest}, nil
}

func (s *Service) taskFinishPreview(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	status, _ := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "--branch")
	diff, err := s.runGitOutput(task.Worktree, "diff")
	if err != nil {
		return nil, err
	}
	text, truncated, ref, err := s.boundedTextArtifact("task.finish_preview", task.ID, diff, s.cfg.MaxReadBytes, map[string]any{"task_id": task.ID, "project": task.Project})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"task": task, "status": trimPreview(status, 4000), "diff": text, "truncated": truncated, "next": []string{"task.commit_preview", "handoff.create"}}
	s.addArtifactFields(out, ref)
	return out, nil
}

func (s *Service) taskCommitPreview(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	status, _ := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "--branch")
	diff, err := s.runGitOutput(task.Worktree, "diff")
	if err != nil {
		return nil, err
	}
	diffStat, _ := s.runGitOutput(task.Worktree, "diff", "--stat")
	fingerprint, err := s.taskChangeFingerprint(task)
	if err != nil {
		return nil, err
	}
	pending, err := taskHasPendingChanges(task.Worktree)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, fmt.Errorf("task has no pending changes to commit")
	}
	message := strings.TrimSpace(stringArg(args, "message"))
	if message == "" {
		message = task.Title
	}
	if message == "" {
		message = "AniGate task " + task.ID
	}
	text, truncated, ref, err := s.boundedTextArtifact("task.commit_preview", task.ID, diff, s.cfg.MaxReadBytes, map[string]any{"task_id": task.ID, "project": task.Project})
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"task":             task,
		"status":           trimPreview(status, 4000),
		"diff":             text,
		"diff_stat":        trimPreview(diffStat, 4000),
		"diff_sha256":      fingerprint,
		"proposed_message": message,
		"truncated":        truncated,
		"next":             []string{"task.commit", "handoff.create"},
	}
	s.addArtifactFields(out, ref)
	return out, nil
}

func (s *Service) taskCommit(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	// Refuse to commit while a job (e.g. an async agent turn) bound to this task
	// is still writing to the worktree; otherwise `git add -A` could stage
	// in-flight, unpreviewed edits that were never fingerprinted.
	if running, err := s.taskHasRunningJob(task.ID); err != nil {
		return nil, err
	} else if running {
		return nil, fmt.Errorf("task has a running job; wait for it to finish before committing")
	}
	message := strings.TrimSpace(stringArg(args, "message"))
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	expected := stringArg(args, "expected_diff_sha256")
	if expected == "" {
		return nil, fmt.Errorf("expected_diff_sha256 is required")
	}
	pending, err := taskHasPendingChanges(task.Worktree)
	if err != nil {
		return nil, err
	}
	if !pending {
		return nil, fmt.Errorf("task has no pending changes to commit")
	}
	fingerprint, err := s.taskChangeFingerprint(task)
	if err != nil {
		return nil, err
	}
	if fingerprint != expected {
		return nil, fmt.Errorf("expected_diff_sha256 does not match current task changes")
	}
	if err := runGitExternal(task.Worktree, "add", "-A"); err != nil {
		return nil, err
	}
	if err := runGitExternal(task.Worktree, "commit", "-m", message); err != nil {
		return nil, err
	}
	commit, _ := s.runGitOutput(task.Worktree, "rev-parse", "HEAD")
	status, _ := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "--branch")
	task.UpdatedAt = time.Now().UTC()
	_ = s.writeTask(task)
	s.events.Append(Event{Kind: "task_committed", Tool: "task.commit", Workspace: task.Workspace, OK: true, Fields: map[string]any{"task_id": task.ID, "project": task.Project, "commit": strings.TrimSpace(commit)}})
	return map[string]any{
		"task":    task,
		"commit":  strings.TrimSpace(commit),
		"status":  status,
		"message": message,
		"next":    []string{"publish.preview", "handoff.create"},
	}, nil
}

func (s *Service) taskTimeline(args map[string]any) (map[string]any, error) {
	taskID := stringArg(args, "task_id")
	if !validName(taskID) {
		return nil, fmt.Errorf("invalid task_id")
	}
	limit := intArgDefault(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// Filter by task_id during the scan so the window applies to this task's
	// events, not the last N events across every task and tool.
	selected, err := s.events.Tail(limit, EventFilter{TaskID: taskID})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": taskID, "events": selected, "count": len(selected)}, nil
}

func (s *Service) taskSearch(args map[string]any) (map[string]any, error) {
	query := strings.ToLower(stringArg(args, "query"))
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := intArgDefault(args, "max_results", 50)
	tasks, err := s.listTasks("", 1000)
	if err != nil {
		return nil, err
	}
	var results []map[string]any
	for _, task := range tasks {
		b, _ := json.Marshal(task)
		if strings.Contains(strings.ToLower(string(b)), query) {
			results = append(results, map[string]any{"task_id": task.ID, "project": task.Project, "title": task.Title, "state": task.State, "branch": task.Branch})
			if len(results) >= limit {
				break
			}
		}
	}
	return map[string]any{"query": query, "results": results, "count": len(results)}, nil
}

func (s *Service) publishPreview(args map[string]any) (map[string]any, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return nil, err
	}
	project, err := s.requireProject(task.Project)
	if err != nil {
		return nil, err
	}
	status, _ := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "--branch")
	diffStat, _ := s.runGitOutput(task.Worktree, "diff", "--stat")
	// A git failure must not fall open here: minting a publish token for a
	// worktree whose cleanliness could not be verified defeats the dirty check.
	pending, err := taskHasPendingChanges(task.Worktree)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, fmt.Errorf("task has uncommitted changes; call task.commit_preview then task.commit before publish.preview")
	}
	token, err := newPublishToken()
	if err != nil {
		return nil, err
	}
	rec := publishTokenRecord{Token: token, TaskID: task.ID, Project: task.Project, Branch: task.Branch, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	if err := s.writePublishToken(rec); err != nil {
		return nil, err
	}
	s.events.Append(Event{Kind: "publish_preview", Tool: "publish.preview", OK: true, Fields: map[string]any{"task_id": task.ID, "project": task.Project}})
	return map[string]any{
		"task":          task,
		"project":       project.Name,
		"remote_url":    redactRemoteURL(project.RemoteURL),
		"allow_push":    project.AllowPush,
		"allow_pr":      project.AllowPR,
		"status":        trimPreview(status, 4000),
		"diff_stat":     trimPreview(diffStat, 4000),
		"confirm_token": token,
		"expires_at":    rec.ExpiresAt,
		"next":          []string{"publish.branch", "publish.pr_create"},
	}, nil
}

func (s *Service) publishBranch(args map[string]any) (map[string]any, error) {
	task, project, rec, err := s.verifyPublishToken(args)
	if err != nil {
		return nil, err
	}
	if !project.AllowPush {
		return nil, fmt.Errorf("project %q does not allow push", project.Name)
	}
	if task.Branch == project.DefaultBranch || task.Branch == "main" || task.Branch == "master" {
		return nil, fmt.Errorf("refusing to push protected branch %q", task.Branch)
	}
	if err := s.deletePublishToken(rec.Token); err != nil {
		return nil, err
	}
	if err := runGitNetwork(task.Worktree, "push", "-u", "origin", task.Branch); err != nil {
		return nil, err
	}
	s.events.Append(Event{Kind: "publish_branch", Tool: "publish.branch", OK: true, Fields: map[string]any{"task_id": task.ID, "project": task.Project, "branch": task.Branch}})
	return map[string]any{"task": task, "pushed": true, "branch": task.Branch, "remote_url": redactRemoteURL(project.RemoteURL)}, nil
}

func (s *Service) publishPRCreate(args map[string]any) (map[string]any, error) {
	task, project, rec, err := s.verifyPublishToken(args)
	if err != nil {
		return nil, err
	}
	if !project.AllowPR {
		return nil, fmt.Errorf("project %q does not allow PR creation", project.Name)
	}
	if project.Provider != "github_gh" {
		return nil, fmt.Errorf("project %q provider %q does not support pr_create", project.Name, project.Provider)
	}
	title := stringArgDefault(args, "title", task.Title)
	if strings.TrimSpace(title) == "" {
		title = task.Branch
	}
	body := stringArgDefault(args, "body", "Created by AniGate publish.pr_create.")
	cmdArgs := []string{"pr", "create", "--base", project.DefaultBranch, "--head", task.Branch, "--title", title, "--body", body}
	if boolArg(args, "draft") {
		cmdArgs = append(cmdArgs, "--draft")
	}
	if err := s.deletePublishToken(rec.Token); err != nil {
		return nil, err
	}
	out, err := runHostCommand(task.Worktree, hostCmdOpts{Timeout: gitNetworkTimeout}, "gh", cmdArgs...)
	if err != nil {
		return nil, err
	}
	s.events.Append(Event{Kind: "publish_pr_created", Tool: "publish.pr_create", OK: true, Fields: map[string]any{"task_id": task.ID, "project": task.Project}})
	return map[string]any{"task": task, "pr": strings.TrimSpace(out)}, nil
}

func (s *Service) requireProject(name string) (Project, error) {
	if name == "" {
		return Project{}, fmt.Errorf("project is required")
	}
	for _, project := range s.cfg.Projects {
		if project.Name == name {
			return project, nil
		}
	}
	return Project{}, fmt.Errorf("unknown project %q", name)
}

func (s *Service) resolveProjectPath(project Project) (resolvedPath, error) {
	return s.policy.resolve(project.Workspace, project.Path)
}

func (s *Service) mustWorkspace(name string) Workspace {
	ws, _ := s.policy.workspace(name)
	return ws
}

func projectInfo(project Project, rp resolvedPath) map[string]any {
	return map[string]any{
		"name":           project.Name,
		"description":    project.Description,
		"workspace":      project.Workspace,
		"path":           filepath.ToSlash(rp.Rel),
		"local_path":     rp.Abs,
		"remote_url":     redactRemoteURL(project.RemoteURL),
		"default_branch": project.DefaultBranch,
		"provider":       project.Provider,
		"allow_push":     project.AllowPush,
		"allow_pr":       project.AllowPR,
		"default_agent":  project.DefaultAgent,
	}
}

func (s *Service) writeTask(task TaskRecord) error {
	dir := filepath.Join(s.cfg.StateDir, "tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, task.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Service) readTask(id string) (TaskRecord, error) {
	if !validName(id) {
		return TaskRecord{}, fmt.Errorf("invalid task_id")
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "tasks", id+".json"))
	if err != nil {
		return TaskRecord{}, err
	}
	var task TaskRecord
	if err := json.Unmarshal(b, &task); err != nil {
		return TaskRecord{}, err
	}
	return task, nil
}

func (s *Service) listTasks(project string, limit int) ([]TaskRecord, error) {
	dir := filepath.Join(s.cfg.StateDir, "tasks")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []TaskRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var tasks []TaskRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		task, err := s.readTask(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		if project != "" && task.Project != project {
			continue
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt) })
	if limit > 0 && len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks, nil
}

func (s *Service) taskChangeFingerprint(task TaskRecord) (string, error) {
	var b strings.Builder
	status, err := s.runGitOutput(task.Worktree, "status", "--porcelain=v1", "-z")
	if err != nil {
		return "", err
	}
	diff, err := s.runGitOutput(task.Worktree, "diff")
	if err != nil {
		return "", err
	}
	cached, err := s.runGitOutput(task.Worktree, "diff", "--cached")
	if err != nil {
		return "", err
	}
	b.WriteString("status\000")
	b.WriteString(status)
	b.WriteString("\000diff\000")
	b.WriteString(diff)
	b.WriteString("\000cached\000")
	b.WriteString(cached)
	untracked, err := s.runGitOutput(task.Worktree, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	for _, name := range strings.Split(untracked, "\x00") {
		if name == "" {
			continue
		}
		clean, err := cleanGitPath(name)
		if err != nil {
			return "", err
		}
		path := filepath.Join(task.Worktree, clean)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		b.WriteString("\000untracked\000")
		b.WriteString(clean)
		b.WriteString("\000")
		b.WriteString(sha256Hex(content))
	}
	return sha256Hex([]byte(b.String())), nil
}

func (s *Service) taskHasRunningJob(taskID string) (bool, error) {
	jobs, err := s.jobs.List(200, JobRunning)
	if err != nil {
		return false, err
	}
	for _, j := range jobs {
		if j.TaskID == taskID {
			return true, nil
		}
	}
	return false, nil
}

func taskHasPendingChanges(worktree string) (bool, error) {
	// StdoutOnly: stderr warnings must not be mistaken for pending changes.
	out, err := runHostCommand(worktree, hostCmdOpts{StdoutOnly: true}, "git", "status", "--porcelain=v1")
	if err != nil {
		return false, fmt.Errorf("git status failed in task worktree: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

func (s *Service) writePublishToken(rec publishTokenRecord) error {
	dir := filepath.Join(s.cfg.StateDir, "publish_tokens")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rec.Token+".json"), b, 0o600)
}

func (s *Service) deletePublishToken(token string) error {
	if !validName(token) {
		return fmt.Errorf("invalid confirm_token")
	}
	return os.Remove(filepath.Join(s.cfg.StateDir, "publish_tokens", token+".json"))
}

func (s *Service) verifyPublishToken(args map[string]any) (TaskRecord, Project, publishTokenRecord, error) {
	task, err := s.readTask(stringArg(args, "task_id"))
	if err != nil {
		return TaskRecord{}, Project{}, publishTokenRecord{}, err
	}
	token := stringArg(args, "confirm_token")
	if !validName(token) {
		return TaskRecord{}, Project{}, publishTokenRecord{}, fmt.Errorf("invalid confirm_token")
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "publish_tokens", token+".json"))
	if err != nil {
		return TaskRecord{}, Project{}, publishTokenRecord{}, err
	}
	var rec publishTokenRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return TaskRecord{}, Project{}, publishTokenRecord{}, err
	}
	if rec.TaskID != task.ID || rec.Branch != task.Branch || time.Now().UTC().After(rec.ExpiresAt) {
		return TaskRecord{}, Project{}, publishTokenRecord{}, fmt.Errorf("publish token is expired or does not match task")
	}
	project, err := s.requireProject(task.Project)
	if err != nil {
		return TaskRecord{}, Project{}, publishTokenRecord{}, err
	}
	return task, project, rec, nil
}

func newPublishToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func runGitExternal(cwd string, args ...string) error {
	_, err := runExternalOutput(cwd, "git", args...)
	return err
}

// gitIdentityEnv supplies a default author/committer identity for git
// subprocesses, whose environment is otherwise stripped of HOME and GIT_* vars.
func gitIdentityEnv() []string {
	return []string{
		"GIT_AUTHOR_NAME=AniGate",
		"GIT_AUTHOR_EMAIL=anigate@localhost",
		"GIT_COMMITTER_NAME=AniGate",
		"GIT_COMMITTER_EMAIL=anigate@localhost",
	}
}

func runExternalOutput(cwd, name string, args ...string) (string, error) {
	opts := hostCmdOpts{}
	if name == "git" {
		// The subprocess env is stripped (no HOME), so git cannot read the
		// user's global identity. Provide a default bot identity so task.commit
		// works out of the box instead of failing with "empty ident name".
		opts.ExtraEnv = gitIdentityEnv()
	}
	return runHostCommand(cwd, opts, name, args...)
}

// runGitNetwork is runGitExternal with the longer network timeout for git
// operations that contact the remote (clone, fetch, push).
func runGitNetwork(cwd string, args ...string) error {
	_, err := runHostCommand(cwd, hostCmdOpts{Timeout: gitNetworkTimeout, ExtraEnv: gitIdentityEnv()}, "git", args...)
	return err
}

func redactRemoteURL(remote string) string {
	if i := strings.Index(remote, "://"); i >= 0 {
		prefix := remote[:i+3]
		rest := remote[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			return prefix + "[redacted]@" + rest[at+1:]
		}
	}
	return remote
}

func sanitizeCommandText(text string) string {
	fields := strings.Fields(text)
	for i, field := range fields {
		fields[i] = redactRemoteURL(field)
	}
	if len(fields) > 0 {
		text = strings.Join(fields, " ")
	}
	return text
}

func safeSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func mustString(s string, _ error) string {
	return s
}
