package anigate

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #5: audit events must never be silently dropped.
func TestEventLogAppendSurfacesError(t *testing.T) {
	dir := t.TempDir()
	// A directory can't be opened for writing, so append must fail loudly.
	badPath := filepath.Join(dir, "isdir")
	if err := os.Mkdir(badPath, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := &EventLog{path: badPath}
	if err := broken.Append(Event{Kind: "test"}); err == nil {
		t.Fatal("expected Append to surface an error when the log path is unwritable")
	}

	good, err := NewEventLog(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := good.Append(Event{Kind: "ok", Tool: "unit"}); err != nil {
		t.Fatalf("append of a well-formed event should succeed: %v", err)
	}
	events, err := good.Tail(10, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "ok" {
		t.Fatalf("event did not round-trip: %#v", events)
	}
}

// Issue #6: fs.write_preview must bound the diff and spill to an artifact
// instead of returning an unbounded read of the existing file.
func TestWritePreviewBoundsLargeFile(t *testing.T) {
	svc, root := testService(t) // MaxReadBytes == 1024
	big := strings.Repeat("line of text\n", 500)
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.fsWritePreview(map[string]any{"workspace": "test", "path": "big.txt", "content": "new\n"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := got["diff"].(string); int64(len(diff)) > svc.cfg.MaxReadBytes {
		t.Fatalf("diff not bounded: %d bytes exceeds cap %d", len(diff), svc.cfg.MaxReadBytes)
	}
	if got["old_truncated"] != true {
		t.Fatalf("expected old_truncated=true, got %#v", got["old_truncated"])
	}
	if got["artifact"] == nil {
		t.Fatal("expected an artifact ref for the spilled diff")
	}
}

// Issue #7: a job left "running" by a crashed process is finalized when a new
// manager boots over the same state dir.
func TestReconcileInterruptedJobsOnStartup(t *testing.T) {
	svc, _ := testService(t)
	rec := JobRecord{
		ID:        "20200101T000000-deadbeefcafe",
		Kind:      "preset",
		Preset:    "ghost",
		State:     JobRunning,
		StartedAt: time.Now().UTC().Add(-time.Hour),
		Workspace: "test",
		LogPath:   filepath.Join(svc.cfg.StateDir, "logs", "ghost.log"),
	}
	if err := svc.jobs.writeRecord(rec); err != nil {
		t.Fatal(err)
	}
	// Booting a fresh manager over the same state dir must reconcile the orphan.
	jm, err := NewJobManager(svc.cfg, svc.policy, svc.events, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := jm.Status(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != JobFailed || !strings.Contains(got.Error, "interrupted") {
		t.Fatalf("expected interrupted->failed, got state=%s err=%q", got.State, got.Error)
	}
}

// Issue #8: an oversized agent message must not brick the session's read path.
func TestAgentMessageOversizedDoesNotBrickSession(t *testing.T) {
	svc, _ := testService(t)
	sid := "20200101T000000-aaaaaaaaaaaa"
	huge := strings.Repeat("x", 2*1024*1024)
	if err := svc.appendAgentMessage(sid, AgentMessage{Role: "assistant", Text: huge}); err != nil {
		t.Fatal(err)
	}
	if err := svc.appendAgentMessage(sid, AgentMessage{Role: "user", Text: "next"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := svc.readAgentMessages(sid, 10)
	if err != nil {
		t.Fatalf("session read bricked by oversized message: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if len(msgs[0].Text) > maxAgentMessageBytes+64 {
		t.Fatalf("oversized message was not capped: %d bytes", len(msgs[0].Text))
	}
}

// Issue #9: audit.summary must aggregate the whole time window, not the last ~50.
func TestAuditSummaryCoversWholeWindow(t *testing.T) {
	svc, _ := testService(t)
	const n = 120 // well past the old 50-event Tail clamp
	for i := 0; i < n; i++ {
		if err := svc.events.Append(Event{Kind: "tool_call", Tool: "fs.read", OK: false}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := svc.auditSummary(map[string]any{"since_sec": float64(3600)})
	if err != nil {
		t.Fatal(err)
	}
	if got := summary["events_scanned"].(int); got < n {
		t.Fatalf("expected >= %d events scanned, got %d", n, got)
	}
	if got := summary["failures"].(int); got < n {
		t.Fatalf("expected >= %d failures counted, got %d", n, got)
	}
}

// Issue #10: a task's timeline must survive a flood of unrelated events.
func TestTaskTimelineSurvivesUnrelatedActivity(t *testing.T) {
	svc, _ := testService(t)
	tid := "20200101T000000-bbbbbbbbbbbb"
	if err := svc.events.Append(Event{Kind: "task_started", Tool: "task.start", OK: true, Fields: map[string]any{"task_id": tid}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		if err := svc.events.Append(Event{Kind: "tool_call", Tool: "fs.read", OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.events.Append(Event{Kind: "task_committed", Tool: "task.commit", OK: true, Fields: map[string]any{"task_id": tid}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.taskTimeline(map[string]any{"task_id": tid})
	if err != nil {
		t.Fatal(err)
	}
	if got["count"].(int) != 2 {
		t.Fatalf("expected both task events retrievable after unrelated flood, got %v", got["count"])
	}
}

// Issue #12: free-form preset string args must not smuggle flags to the binary.
func TestPresetRejectsLeadingDashFlagInjection(t *testing.T) {
	p := Preset{Name: "x", Workspace: "w", Command: []string{"tool", "{arg}"}, Args: []PresetArg{{Name: "arg", Type: "string"}}}
	if _, _, err := RenderPresetCommand(p, map[string]any{"arg": "-rf"}); err == nil {
		t.Fatal("expected leading-dash value to be rejected")
	}
	if _, _, err := RenderPresetCommand(p, map[string]any{"arg": "safe"}); err != nil {
		t.Fatalf("safe value should be accepted: %v", err)
	}
	p.Args[0].AllowLeadingDash = true
	if _, _, err := RenderPresetCommand(p, map[string]any{"arg": "-v"}); err != nil {
		t.Fatalf("allow_leading_dash should permit flags: %v", err)
	}
}

// Issue #13: a failed tool call must record the offending workspace and path.
func TestFailedToolCallAuditIncludesContext(t *testing.T) {
	svc, _ := testService(t)
	raw, _ := json.Marshal(map[string]any{"workspace": "test", "path": "../../etc/shadow"})
	if _, err := svc.CallTool("fs.read", raw); err == nil {
		t.Fatal("expected a path-escape error")
	}
	events, err := svc.events.Tail(20, EventFilter{Kind: "tool_call"})
	if err != nil {
		t.Fatal(err)
	}
	var found *Event
	for i := range events {
		if events[i].Tool == "fs.read" && !events[i].OK {
			found = &events[i]
		}
	}
	if found == nil {
		t.Fatal("no failed fs.read audit event recorded")
	}
	if found.Path != "../../etc/shadow" || found.Workspace != "test" {
		t.Fatalf("audit event missing context: workspace=%q path=%q", found.Workspace, found.Path)
	}
}

// Issue #14: a new path under an escaping symlink must be rejected, not just
// checked lexically.
func TestResolveRejectsNewFileUnderEscapingSymlink(t *testing.T) {
	svc, root := testService(t)
	link := filepath.Join(root, "escape")
	if err := os.Symlink(filepath.Dir(root), link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if _, err := svc.policy.resolve("test", "escape/newfile"); err == nil {
		t.Fatal("expected resolve to reject a new path under an escaping symlink")
	}
	// A normal not-yet-existing path inside the workspace still resolves.
	if _, err := svc.policy.resolve("test", "sub/newfile"); err != nil {
		t.Fatalf("normal new path should resolve: %v", err)
	}
}

// Issue #15: git commit must succeed even though the subprocess env is stripped
// of the host's git identity (no HOME).
func TestGitCommitWorksWithoutHostIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := runGitExternal(dir, "init"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGitExternal(dir, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	// runGitExternal strips HOME, so this only works if AniGate injects an identity.
	if err := runGitExternal(dir, "commit", "-m", "bootstrap"); err != nil {
		t.Fatalf("commit failed without a host git identity: %v", err)
	}
}

// Issue #16: task.commit must refuse while a job bound to the task is running.
func TestTaskCommitRefusesWhileJobRunning(t *testing.T) {
	svc, root := testService(t)
	tid := "20200101T000000-cccccccccccc"
	if err := svc.writeTask(TaskRecord{ID: tid, Project: "p", State: "active", Workspace: "test", Worktree: filepath.Join(root, "wt"), Branch: "anigate/x"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.jobs.writeRecord(JobRecord{ID: "20200101T000000-dddddddddddd", State: JobRunning, TaskID: tid, StartedAt: time.Now().UTC(), LogPath: filepath.Join(root, "j.log")}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.taskCommit(map[string]any{"task_id": tid, "message": "m", "expected_diff_sha256": "x"})
	if err == nil || !strings.Contains(err.Error(), "running job") {
		t.Fatalf("expected refusal while a task job is running, got %v", err)
	}
}

// Issue #18: internal job counts must not be capped at 50.
func TestJobCountNotClampedForInternalStats(t *testing.T) {
	svc, root := testService(t)
	for i := 0; i < 60; i++ {
		id, err := newJobID()
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.jobs.writeRecord(JobRecord{ID: id, State: JobDone, StartedAt: time.Now().UTC(), LogPath: filepath.Join(root, "l.log")}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := svc.gateStats()
	if err != nil {
		t.Fatal(err)
	}
	if got := stats["jobs"].(int); got < 60 {
		t.Fatalf("expected >= 60 jobs counted, got %d (clamped?)", got)
	}
}

// Issue #29: project.ensure maintenance commands must not leak credentialed
// remote URLs into error messages.
func TestProjectEnsureRedactsRemoteURLInErrors(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "clone")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, repo, "init")
	secret := "sekrettoken123"
	remote := "https://bot:" + secret + "@127.0.0.1:1/private.git"
	cfg := Config{
		StateDir:   filepath.Join(root, "state"),
		Workspaces: []Workspace{{Name: "work", Path: root, Profile: "agent"}},
		Projects:   []Project{{Name: "demo", Workspace: "work", Path: "clone", RemoteURL: remote, DefaultBranch: "main"}},
	}
	svc, err := NewService(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The fresh repo has no "origin" remote, so `git remote set-url` fails with
	// the credentialed URL in its argv; the error must arrive redacted.
	_, err = svc.projectEnsure(map[string]any{"project": "demo"})
	if err == nil {
		t.Fatal("expected project.ensure to fail (no origin remote)")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the remote credential: %v", err)
	}
}

// Issue #31: the unified host-command runner must sanitize credentialed URLs
// in both argv and output previews of error messages.
func TestRunHostCommandSanitizesErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := runHostCommand(dir, hostCmdOpts{}, "git", "remote", "set-url", "origin", "https://u:p4ss@example.com/x.git")
	if err == nil {
		t.Fatal("expected failure outside a git repo")
	}
	if strings.Contains(err.Error(), "p4ss") {
		t.Fatalf("runner leaked credential: %v", err)
	}
}

// Issue #30: publish.preview must refuse, not fall open, when the worktree's
// cleanliness cannot be verified.
func TestPublishPreviewRefusesWhenGitStatusFails(t *testing.T) {
	root := t.TempDir()
	notARepo := filepath.Join(root, "not-a-repo")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StateDir:   filepath.Join(root, "state"),
		Workspaces: []Workspace{{Name: "work", Path: root, Profile: "agent"}},
		Projects:   []Project{{Name: "demo", Workspace: "work", Path: "not-a-repo", RemoteURL: "https://example.invalid/x.git", DefaultBranch: "main"}},
	}
	svc, err := NewService(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	task := TaskRecord{ID: "20200101T000000-eeeeeeeeeeee", Project: "demo", State: "active", Workspace: "work", Worktree: notARepo, Branch: "anigate/x"}
	if err := svc.writeTask(task); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.publishPreview(map[string]any{"task_id": task.ID}); err == nil {
		t.Fatal("expected publish.preview to fail when git status cannot run")
	}
	entries, _ := os.ReadDir(filepath.Join(cfg.StateDir, "publish_tokens"))
	if len(entries) != 0 {
		t.Fatalf("a publish token was minted despite the failure: %d files", len(entries))
	}
}

// publishFixture builds a service around a real single-commit git repo with a
// task record pointing at it, for exercising the publish token flow locally.
func publishFixture(t *testing.T) (*Service, TaskRecord, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "clone")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runGitExternal(repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGitExternal(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := runGitExternal(repo, "commit", "-m", "one"); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StateDir:   filepath.Join(root, "state"),
		Workspaces: []Workspace{{Name: "work", Path: root, Profile: "agent"}},
		Projects:   []Project{{Name: "demo", Workspace: "work", Path: "clone", RemoteURL: "https://example.invalid/x.git", DefaultBranch: "main", AllowPush: true}},
	}
	svc, err := NewService(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	task := TaskRecord{ID: "20200101T000000-ffffffffffff", Project: "demo", State: "active", Workspace: "work", Worktree: repo, Branch: "anigate/x"}
	if err := svc.writeTask(task); err != nil {
		t.Fatal(err)
	}
	return svc, task, repo
}

// Issue #33: a publish confirm token must be bound to the HEAD it previewed.
func TestPublishTokenBoundToHead(t *testing.T) {
	svc, task, repo := publishFixture(t)
	prev, err := svc.publishPreview(map[string]any{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	token := prev["confirm_token"].(string)
	if _, _, _, err := svc.verifyPublishToken(map[string]any{"task_id": task.ID, "confirm_token": token}); err != nil {
		t.Fatalf("token should verify while HEAD is unchanged: %v", err)
	}
	// A new commit lands after the preview: the stale token must be rejected.
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGitExternal(repo, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := runGitExternal(repo, "commit", "-m", "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.publishBranch(map[string]any{"task_id": task.ID, "confirm_token": token}); err == nil {
		t.Fatal("expected the stale token to be rejected after a new commit")
	}
}

// Issue #36: expired publish tokens are swept when a new preview is issued.
func TestPublishPreviewSweepsExpiredTokens(t *testing.T) {
	svc, task, _ := publishFixture(t)
	stale := publishTokenRecord{
		Token:     "deadbeefdeadbeefdeadbeefdeadbeef",
		TaskID:    task.ID,
		Project:   task.Project,
		Branch:    task.Branch,
		CreatedAt: time.Now().UTC().Add(-2 * time.Hour),
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
	}
	if err := svc.writePublishToken(stale); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.publishPreview(map[string]any{"task_id": task.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(svc.cfg.StateDir, "publish_tokens", stale.Token+".json")); !os.IsNotExist(err) {
		t.Fatalf("expired token file was not swept: %v", err)
	}
}

// Issue #19: agent session updates must be serialized and finalized cleanly.
func TestAgentSessionUpdatesAreSerialized(t *testing.T) {
	svc, _ := testService(t)
	start, err := svc.agentSessionStart(map[string]any{"agent": "echo_agent"})
	if err != nil {
		t.Fatal(err)
	}
	sid := start["session"].(AgentSession).ID
	res, err := svc.agentMessageSend(map[string]any{"session_id": sid, "message": "hello", "async": false})
	if err != nil {
		t.Fatal(err)
	}
	if sess := res["session"].(AgentSession); sess.LastJobID == "" {
		t.Fatal("expected LastJobID to be recorded in the returned session")
	}
	got, err := svc.readAgentSession(sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastJobID == "" || got.State == "running" {
		t.Fatalf("session not finalized after sync send: %#v", got)
	}
}
