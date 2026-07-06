package anigate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type AgentSession struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Provider  string    `json:"provider"`
	Workspace string    `json:"workspace"`
	TaskID    string    `json:"task_id,omitempty"`
	Cwd       string    `json:"cwd"`
	CwdRel    string    `json:"cwd_rel"`
	Title     string    `json:"title,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	LastJobID string    `json:"last_job_id,omitempty"`
}

type AgentMessage struct {
	Time  time.Time `json:"time"`
	Role  string    `json:"role"`
	Text  string    `json:"text"`
	JobID string    `json:"job_id,omitempty"`
}

// maxAgentMessageBytes caps a single stored message so one oversized turn can
// never produce an NDJSON line the reader cannot scan back.
const maxAgentMessageBytes = 256 * 1024

func (s *Service) agentSessionStart(args map[string]any) (map[string]any, error) {
	name := stringArg(args, "agent")
	if name == "" {
		return nil, fmt.Errorf("agent is required")
	}
	agent, ok := s.findAgent(name)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", name)
	}
	if err := s.workspaceAllows(agent.Workspace, "agent"); err != nil {
		return nil, err
	}
	workspace := agent.Workspace
	requestedPath := stringArgDefault(args, "path", agent.Cwd)
	taskID := stringArg(args, "task_id")
	if taskID != "" {
		task, err := s.readTask(taskID)
		if err != nil {
			return nil, err
		}
		workspace = task.Workspace
		requestedPath = task.WorktreeRel
		if err := s.workspaceAllows(workspace, "agent"); err != nil {
			return nil, err
		}
	}
	rp, err := s.policy.resolve(workspace, requestedPath)
	if err != nil {
		return nil, err
	}
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	session := AgentSession{
		ID:        id,
		Agent:     agent.Name,
		Provider:  agent.Provider,
		Workspace: workspace,
		TaskID:    taskID,
		Cwd:       rp.Abs,
		CwdRel:    rp.Rel,
		Title:     stringArg(args, "title"),
		State:     "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.writeAgentSession(session); err != nil {
		return nil, err
	}
	s.events.Append(Event{Kind: "agent_session_started", Tool: "agent.session_start", Workspace: session.Workspace, OK: true, Fields: map[string]any{"session_id": id, "agent": agent.Name, "task_id": taskID}})
	return map[string]any{"session": session}, nil
}

func (s *Service) agentMessageSend(args map[string]any) (map[string]any, error) {
	sessionID := stringArg(args, "session_id")
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	message := stringArg(args, "message")
	if strings.TrimSpace(message) == "" {
		return nil, fmt.Errorf("message is required")
	}
	session, err := s.readAgentSession(sessionID)
	if err != nil {
		return nil, err
	}
	agent, ok := s.findAgent(session.Agent)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q", session.Agent)
	}
	if err := s.workspaceAllows(session.Workspace, "agent"); err != nil {
		return nil, err
	}
	userMsg := AgentMessage{Time: time.Now().UTC(), Role: "user", Text: message}
	if err := s.appendAgentMessage(session.ID, userMsg); err != nil {
		return nil, err
	}
	history, err := s.readAgentMessages(session.ID, agent.MaxHistoryMessages)
	if err != nil {
		return nil, err
	}
	prompt := buildAgentPrompt(session, history)
	command, stdin, err := renderAgentCommand(agent, prompt)
	if err != nil {
		return nil, err
	}
	spec := JobSpec{
		Kind:       "agent",
		Name:       agent.Name,
		SessionID:  session.ID,
		TaskID:     session.TaskID,
		Workspace:  session.Workspace,
		Cwd:        session.CwdRel,
		Command:    command,
		Stdin:      stdin,
		TimeoutSec: agent.TimeoutSec,
		Env:        agent.Env,
		EventTool:  "agent.message_send",
	}
	spec.OnFinish = func(rec JobRecord) {
		text, _ := s.jobs.LogsTail(rec.ID, s.cfg.MaxJobLogBytes)
		text = trimJobOutput(text)
		role := "assistant"
		if rec.State != JobDone {
			role = "system"
			if text == "" {
				text = rec.Error
			}
		}
		_ = s.appendAgentMessage(session.ID, AgentMessage{Time: time.Now().UTC(), Role: role, Text: text, JobID: rec.ID})
		_, _ = s.updateAgentSession(session.ID, func(cur *AgentSession) {
			cur.State = "active"
			if rec.State == JobFailed || rec.State == JobCancelled {
				cur.State = string(rec.State)
			}
			cur.LastJobID = rec.ID
		})
	}
	async := true
	if raw, ok := args["async"].(bool); ok {
		async = raw
	}
	if _, err := s.updateAgentSession(session.ID, func(cur *AgentSession) {
		cur.State = "running"
	}); err != nil {
		return nil, err
	}
	job, tail, err := s.jobs.RunCommand(contextWithBackground(), spec, async)
	if err != nil {
		return nil, err
	}
	// Record the job id even before an async job finishes, without clobbering a
	// terminal state OnFinish may have already written (sync path).
	latest, err := s.updateAgentSession(session.ID, func(cur *AgentSession) {
		if cur.LastJobID == "" {
			cur.LastJobID = job.ID
		}
	})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"session": latest, "job": job}
	if tail != "" {
		out["logs_tail"] = tail
	}
	return out, nil
}

// updateAgentSession serializes a read-modify-write of a session record so the
// OnFinish job goroutine and the request goroutine cannot race on the same
// struct or clobber each other's write.
func (s *Service) updateAgentSession(id string, mutate func(*AgentSession)) (AgentSession, error) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	session, err := s.readAgentSession(id)
	if err != nil {
		return AgentSession{}, err
	}
	mutate(&session)
	session.UpdatedAt = time.Now().UTC()
	if err := s.writeAgentSession(session); err != nil {
		return AgentSession{}, err
	}
	return session, nil
}

func (s *Service) agentSessionStatus(args map[string]any) (map[string]any, error) {
	session, err := s.readAgentSession(stringArg(args, "session_id"))
	if err != nil {
		return nil, err
	}
	out := map[string]any{"session": session}
	if session.LastJobID != "" {
		if job, err := s.jobs.Status(session.LastJobID); err == nil {
			out["last_job"] = job
		}
	}
	return out, nil
}

func (s *Service) agentMessagesTail(args map[string]any) (map[string]any, error) {
	sessionID := stringArg(args, "session_id")
	limit := intArgDefault(args, "limit", 20)
	messages, err := s.readAgentMessages(sessionID, limit)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"session_id": sessionID, "messages": messages, "count": len(messages)}
	if b, err := json.Marshal(messages); err == nil {
		_, _, ref, err := s.boundedTextArtifact("agent.messages_tail", sessionID, string(b), s.cfg.MaxReadBytes, map[string]any{"session_id": sessionID})
		if err != nil {
			return nil, err
		}
		s.addArtifactFields(out, ref)
	}
	return out, nil
}

func (s *Service) agentSessionList(args map[string]any) (map[string]any, error) {
	limit := intArgDefault(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	dir := filepath.Join(s.cfg.StateDir, "agents", "sessions")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{"sessions": []AgentSession{}, "count": 0}, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []AgentSession
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		session, err := s.readAgentSession(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil {
			sessions = append(sessions, session)
		}
	}
	sortAgentSessions(sessions)
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return map[string]any{"sessions": sessions, "count": len(sessions)}, nil
}

func (s *Service) findAgent(name string) (Agent, bool) {
	for _, agent := range s.cfg.Agents {
		if agent.Name == name {
			return agent, true
		}
	}
	return Agent{}, false
}

func (s *Service) writeAgentSession(session AgentSession) error {
	dir := filepath.Join(s.cfg.StateDir, "agents", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, session.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Service) readAgentSession(id string) (AgentSession, error) {
	if !validName(id) {
		return AgentSession{}, fmt.Errorf("invalid session_id")
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "agents", "sessions", id+".json"))
	if err != nil {
		return AgentSession{}, err
	}
	var session AgentSession
	if err := json.Unmarshal(b, &session); err != nil {
		return AgentSession{}, err
	}
	return session, nil
}

func (s *Service) appendAgentMessage(sessionID string, msg AgentMessage) error {
	if msg.Time.IsZero() {
		msg.Time = time.Now().UTC()
	}
	if len(msg.Text) > maxAgentMessageBytes {
		msg.Text = msg.Text[:maxAgentMessageBytes] + "\n[anigate: message truncated]"
	}
	dir := filepath.Join(s.cfg.StateDir, "agents", "messages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, sessionID+".ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Service) readAgentMessages(sessionID string, limit int) ([]AgentMessage, error) {
	if !validName(sessionID) {
		return nil, fmt.Errorf("invalid session_id")
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	f, err := os.Open(filepath.Join(s.cfg.StateDir, "agents", "messages", sessionID+".ndjson"))
	if errors.Is(err, os.ErrNotExist) {
		return []AgentMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var messages []AgentMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxEventLineBytes)
	for scanner.Scan() {
		var msg AgentMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
		if len(messages) > limit {
			copy(messages, messages[len(messages)-limit:])
			messages = messages[:limit]
		}
	}
	return messages, scanner.Err()
}

func buildAgentPrompt(session AgentSession, messages []AgentMessage) string {
	var b strings.Builder
	b.WriteString("AniGate long-running Linux agent session.\n")
	b.WriteString("Session: " + session.ID + "\n")
	b.WriteString("Workspace: " + session.Workspace + "\n")
	b.WriteString("CWD: " + session.Cwd + "\n\n")
	b.WriteString("Conversation:\n")
	for _, msg := range messages {
		b.WriteString(strings.ToUpper(msg.Role) + ": " + msg.Text + "\n")
	}
	return b.String()
}

func renderAgentCommand(agent Agent, prompt string) ([]string, string, error) {
	usedPrompt := false
	command := make([]string, 0, len(agent.Command))
	for _, token := range agent.Command {
		if strings.Contains(token, "{prompt}") {
			usedPrompt = true
			command = append(command, strings.ReplaceAll(token, "{prompt}", prompt))
		} else {
			command = append(command, token)
		}
	}
	if !usedPrompt && !agent.PromptStdin {
		return nil, "", fmt.Errorf("agent command must include {prompt} or set prompt_stdin")
	}
	stdin := ""
	if agent.PromptStdin {
		stdin = prompt
	}
	return command, stdin, nil
}

func trimJobOutput(text string) string {
	idx := strings.IndexByte(text, '\n')
	if idx >= 0 {
		text = text[idx+1:]
	}
	return strings.TrimSpace(text)
}

func sortAgentSessions(sessions []AgentSession) {
	for i := 1; i < len(sessions); i++ {
		for j := i; j > 0 && sessions[j].UpdatedAt.After(sessions[j-1].UpdatedAt); j-- {
			sessions[j], sessions[j-1] = sessions[j-1], sessions[j]
		}
	}
}
