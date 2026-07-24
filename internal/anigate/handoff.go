package anigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HandoffRecord struct {
	ID        string         `json:"id"`
	Title     string         `json:"title,omitempty"`
	Goal      string         `json:"goal,omitempty"`
	TaskID    string         `json:"task_id,omitempty"`
	Summary   string         `json:"summary"`
	Prompt    string         `json:"prompt"`
	Pointers  map[string]any `json:"pointers,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

func (s *Service) workspaceSnapshot(args map[string]any) (map[string]any, error) {
	workspace := stringArg(args, "workspace")
	path := stringArgDefault(args, "path", ".")
	rp, err := s.policy.resolve(workspace, path)
	if err != nil {
		return nil, err
	}
	tree, _ := s.fsTree(map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel, "depth": float64(2), "max_entries": float64(120)})
	status := ""
	logOut := ""
	if _, err := os.Stat(filepath.Join(rp.Abs, ".git")); err == nil {
		status = mustString(s.runGitOutput(rp.Abs, "status", "--porcelain=v1", "--branch"))
		logOut = mustString(s.runGitOutput(rp.Abs, "log", "--oneline", "--decorate", "-n", "10"))
	}
	jobs, _ := s.jobs.List(10, "")
	agents, _ := s.agentSessionList(map[string]any{"limit": float64(10)})
	audit, _ := s.auditSummary(map[string]any{"since_sec": float64(24 * 3600)})
	health, _ := s.contextHealth()
	return map[string]any{
		"workspace": rp.Workspace.Name,
		"path":      rp.Rel,
		"tree":      tree,
		"git": map[string]any{
			"status": trimPreview(status, 4000),
			"log":    trimPreview(logOut, 4000),
		},
		"jobs":           jobs,
		"agent_sessions": agents,
		"audit":          audit,
		"context_health": health,
		"next":           []string{"handoff.create", "artifact.search", "task.recover"},
	}, nil
}

func (s *Service) gateStats() (map[string]any, error) {
	artifactStats, _ := s.artifactStats()
	jobCount, _ := s.jobs.Count("")
	tasks, _ := s.listTasks("", 1000)
	agents, _ := s.agentSessionList(map[string]any{"limit": float64(200)})
	handoffs, _ := s.listHandoffs(1000)
	stateBytes := int64(0)
	_ = filepath.WalkDir(s.cfg.StateDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			stateBytes += info.Size()
		}
		return nil
	})
	return map[string]any{
		"version":          Version,
		"state_dir":        s.cfg.StateDir,
		"state_bytes":      stateBytes,
		"tools":            len(s.Tools()),
		"workspaces":       len(s.cfg.Workspaces),
		"projects":         len(s.cfg.Projects),
		"jobs":             jobCount,
		"tasks":            len(tasks),
		"agent_sessions":   agents["count"],
		"handoffs":         len(handoffs),
		"artifact_stats":   artifactStats,
		"isolated_home":    s.cfg.IsolatedHome,
		"env_allowlist_on": len(s.cfg.EnvAllowlist) > 0,
	}, nil
}

func (s *Service) contextHealth() (map[string]any, error) {
	artifacts, _ := s.listArtifacts(1000, "")
	jobCount, _ := s.jobs.Count("")
	tasks, _ := s.listTasks("", 1000)
	events, _ := s.events.Tail(200, EventFilter{})
	handoffs, _ := s.listHandoffs(1000)
	score := len(artifacts)*4 + jobCount*2 + len(tasks)*6 + len(events)
	level := "green"
	recommend := false
	if score >= 180 || len(tasks) >= 8 {
		level = "red"
		recommend = true
	} else if score >= 80 || len(tasks) >= 3 || jobCount >= 20 {
		level = "yellow"
		recommend = true
	}
	return map[string]any{
		"level":             level,
		"score":             score,
		"recommend_handoff": recommend,
		"known_limits":      "AniGate cannot see ChatGPT Web's total token count; this is based only on AniGate state and returned-output pressure.",
		"inputs": map[string]any{
			"artifacts": len(artifacts),
			"jobs":      jobCount,
			"tasks":     len(tasks),
			"events":    len(events),
			"handoffs":  len(handoffs),
		},
		"next": []string{"handoff.create", "workspace.snapshot", "artifact.search"},
	}, nil
}

func (s *Service) handoffCreate(args map[string]any) (map[string]any, error) {
	taskID := stringArg(args, "task_id")
	title := stringArg(args, "title")
	goal := stringArg(args, "goal")
	notes := stringArg(args, "notes")
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	var task *TaskRecord
	var digest string
	if taskID != "" {
		t, err := s.readTask(taskID)
		if err != nil {
			return nil, err
		}
		task = &t
		if got, err := s.taskDigest(map[string]any{"task_id": taskID}); err == nil {
			digest, _ = got["digest"].(string)
		}
		if title == "" {
			title = t.Title
		}
	}
	health, _ := s.contextHealth()
	artifactStats, _ := s.artifactStats()
	if goal == "" {
		goal = "Continue the AniGate task with bounded context and searchable local state."
	}
	summary := buildHandoffSummary(title, goal, notes, digest, health)
	pointers := map[string]any{
		"task_id":        taskID,
		"artifact_stats": artifactStats,
		"context_health": health,
		"tools":          []string{"handoff.resume", "handoff.search", "artifact.search", "task.recover", "workspace.snapshot"},
	}
	if task != nil {
		pointers["project"] = task.Project
		pointers["workspace"] = task.Workspace
		pointers["worktree_rel"] = task.WorktreeRel
		pointers["branch"] = task.Branch
	}
	prompt := buildHandoffPrompt(id, summary, pointers)
	rec := HandoffRecord{
		ID:        id,
		Title:     title,
		Goal:      goal,
		TaskID:    taskID,
		Summary:   summary,
		Prompt:    prompt,
		Pointers:  pointers,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.writeHandoff(rec); err != nil {
		return nil, err
	}
	s.events.Append(Event{Kind: "handoff_created", Tool: "handoff.create", OK: true, Fields: map[string]any{"handoff_id": id, "task_id": taskID}})
	return map[string]any{"handoff": rec, "next_prompt": prompt, "next": []string{"handoff.resume", "handoff.search"}}, nil
}

func (s *Service) handoffResume(args map[string]any) (map[string]any, error) {
	rec, err := s.readHandoff(stringArg(args, "handoff_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"handoff":  rec,
		"summary":  rec.Summary,
		"pointers": rec.Pointers,
		"next":     []string{"task.recover", "artifact.search", "handoff.search", "workspace.snapshot"},
	}, nil
}

func (s *Service) handoffSearch(args map[string]any) (map[string]any, error) {
	query := strings.ToLower(stringArg(args, "query"))
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := intArgDefault(args, "max_results", 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	records, err := s.listHandoffs(1000)
	if err != nil {
		return nil, err
	}
	var results []map[string]any
	for _, rec := range records {
		b, _ := json.Marshal(rec)
		if strings.Contains(strings.ToLower(string(b)), query) {
			results = append(results, map[string]any{"handoff_id": rec.ID, "title": rec.Title, "task_id": rec.TaskID, "preview": trimPreview(rec.Summary, 320)})
			if len(results) >= limit {
				break
			}
		}
	}
	return map[string]any{"query": query, "results": results, "count": len(results)}, nil
}

func (s *Service) handoffDigest(args map[string]any) (map[string]any, error) {
	if id := stringArg(args, "handoff_id"); id != "" {
		rec, err := s.readHandoff(id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"handoff_id": rec.ID, "digest": rec.Summary, "prompt": rec.Prompt}, nil
	}
	if taskID := stringArg(args, "task_id"); taskID != "" {
		return s.taskDigest(map[string]any{"task_id": taskID})
	}
	return nil, fmt.Errorf("handoff_id or task_id is required")
}

func (s *Service) writeHandoff(rec HandoffRecord) error {
	dir := filepath.Join(s.cfg.StateDir, "handoffs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, rec.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Service) readHandoff(id string) (HandoffRecord, error) {
	if !validName(id) {
		return HandoffRecord{}, fmt.Errorf("invalid handoff_id")
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "handoffs", id+".json"))
	if err != nil {
		return HandoffRecord{}, err
	}
	var rec HandoffRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return HandoffRecord{}, err
	}
	return rec, nil
}

func (s *Service) listHandoffs(limit int) ([]HandoffRecord, error) {
	dir := filepath.Join(s.cfg.StateDir, "handoffs")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []HandoffRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []HandoffRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := s.readHandoff(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil {
			records = append(records, rec)
		}
	}
	for i := 1; i < len(records); i++ {
		for j := i; j > 0 && records[j].CreatedAt.After(records[j-1].CreatedAt); j-- {
			records[j], records[j-1] = records[j-1], records[j]
		}
	}
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func buildHandoffSummary(title, goal, notes, digest string, health map[string]any) string {
	var b strings.Builder
	if title != "" {
		b.WriteString("Title: " + title + "\n")
	}
	b.WriteString("Goal: " + goal + "\n")
	if notes != "" {
		b.WriteString("Notes: " + notes + "\n")
	}
	if digest != "" {
		b.WriteString("\nTask digest:\n" + trimPreview(digest, 3000) + "\n")
	}
	if health != nil {
		b.WriteString("\nContext health: " + fmt.Sprint(health["level"]) + "\n")
	}
	return strings.TrimSpace(b.String())
}

func buildHandoffPrompt(id, summary string, pointers map[string]any) string {
	b, _ := json.MarshalIndent(pointers, "", "  ")
	return "Continue this AniGate work from handoff_id " + id + ".\n" +
		"First call handoff.resume with this id, then use task.recover/artifact.search only as needed; do not request all history at once.\n\n" +
		"Compact summary:\n" + summary + "\n\nPointers:\n" + string(b)
}
