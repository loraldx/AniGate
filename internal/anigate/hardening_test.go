package anigate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// file.search must not read file contents through a workspace-internal symlink
// that points outside the workspace root (path-confinement bypass).
func TestFileSearchDoesNotFollowEscapingSymlink(t *testing.T) {
	svc, root := testService(t)
	// A secret outside the workspace, and an in-tree symlink pointing at it.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOPSECRET-needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	got, err := svc.fileSearch(map[string]any{"workspace": "test", "query": "TOPSECRET-needle"})
	if err != nil {
		t.Fatal(err)
	}
	if results := got["results"].([]map[string]any); len(results) != 0 {
		t.Fatalf("file.search leaked out-of-tree content through a symlink: %#v", results)
	}
}

// Issue #34: limits above the documented maximum clamp to the maximum instead
// of silently resetting to a small default.
func TestLimitsClampToDocumentedMax(t *testing.T) {
	svc, root := testService(t)
	// fs.list: 120 files and a request for 501 must yield >100 entries (old
	// behavior reset to the default of 100).
	for i := 0; i < 120; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.fsList(map[string]any{"workspace": "test", "max_entries": float64(501)})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got["entries"].([]map[string]any)); n <= 100 {
		t.Fatalf("fs.list reset to the old default: %d entries", n)
	}
	// events.Tail: a request for 500 must clamp to 200, not reset to 50.
	for i := 0; i < 250; i++ {
		if err := svc.events.Append(Event{Kind: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := svc.events.Tail(500, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 200 {
		t.Fatalf("events.Tail(500) returned %d events, want 200", len(events))
	}
}

// Issue #37: file.search must keep matching after lines longer than bufio's
// 64 KiB scanner token limit.
func TestFileSearchSurvivesLongLines(t *testing.T) {
	svc, root := testService(t)
	svc.cfg.MaxSearchFileBytes = 512 * 1024
	content := strings.Repeat("x", 128*1024) + "\nneedle-after-long-line\n"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := svc.fileSearch(map[string]any{"workspace": "test", "query": "needle-after-long-line"})
	if err != nil {
		t.Fatal(err)
	}
	results := got["results"].([]map[string]any)
	if len(results) != 1 || results[0]["line"] != 2 {
		t.Fatalf("match after a long line was lost: %#v", results)
	}
}

// Issue #38: validName must reject dot-only names outright.
func TestValidNameRejectsDotOnlyNames(t *testing.T) {
	for _, bad := range []string{".", "..", "..."} {
		if validName(bad) {
			t.Fatalf("validName(%q) must be false", bad)
		}
	}
	for _, good := range []string{"a.b", "20200101T000000-abcdef012345", "v1.2.3"} {
		if !validName(good) {
			t.Fatalf("validName(%q) must be true", good)
		}
	}
}

// Issue #39: audit.summary recent_failures must hold the newest failures.
func TestAuditSummaryRecentFailuresAreNewest(t *testing.T) {
	svc, _ := testService(t)
	for i := 0; i < 15; i++ {
		if err := svc.events.Append(Event{Kind: "tool_call", Tool: "fs.read", OK: false, Message: fmt.Sprintf("fail-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := svc.auditSummary(map[string]any{"since_sec": float64(3600)})
	if err != nil {
		t.Fatal(err)
	}
	recent := summary["recent_failures"].([]Event)
	if len(recent) != 10 {
		t.Fatalf("expected 10 recent failures, got %d", len(recent))
	}
	if recent[len(recent)-1].Message != "fail-14" {
		t.Fatalf("newest failure missing from recent_failures, tail is %q", recent[len(recent)-1].Message)
	}
}

// Issue #40: fs.write_preview's create flag must only tolerate missing files,
// not arbitrary read errors.
func TestWritePreviewCreateOnlyCoversMissingFiles(t *testing.T) {
	svc, root := testService(t)
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Reading a directory fails with a non-NotExist error; create must not hide it.
	if _, err := svc.fsWritePreview(map[string]any{"workspace": "test", "path": "adir", "content": "x", "create": true}); err == nil {
		t.Fatal("expected a read error for a directory target")
	}
	got, err := svc.fsWritePreview(map[string]any{"workspace": "test", "path": "new.txt", "content": "x", "create": true})
	if err != nil {
		t.Fatalf("missing file with create should still preview: %v", err)
	}
	if got["would_write"] != true {
		t.Fatalf("unexpected preview result: %#v", got)
	}
}

// Issue #35: an oversized stdio frame must fail alone, not kill the server.
func TestServeStdioSurvivesOversizedFrame(t *testing.T) {
	svc, _ := testService(t)
	huge := strings.Repeat("x", maxStdioLineBytes+10)
	valid := `{"jsonrpc":"2.0","id":7,"method":"ping"}`
	in := strings.NewReader(huge + "\n" + valid + "\n")
	var out bytes.Buffer
	code := ServeStdio(in, &out, svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if code != 0 {
		t.Fatalf("server exited with %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 responses (error + pong), got %d", len(lines))
	}
	if !strings.Contains(lines[0], "-32700") {
		t.Fatalf("first response should be a -32700 error: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"id":7`) {
		t.Fatalf("second response should answer the valid request: %s", lines[1])
	}
}

// Issue #48: JSON-RPC conformance — id:0 must be echoed, empty list results
// must carry their field, and initialize echoes a supported client version.
func TestProtocolConformance(t *testing.T) {
	svc, _ := testService(t)
	resp, ok := dispatchJSON([]byte(`{"jsonrpc":"2.0","id":0,"method":"ping"}`), svc)
	if !ok {
		t.Fatal("ping with id:0 must produce a response")
	}
	b, _ := json.Marshal(resp)
	if !strings.Contains(string(b), `"id":0`) {
		t.Fatalf("id:0 was not echoed: %s", b)
	}
	resp, _ = dispatchJSON([]byte(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`), svc)
	b, _ = json.Marshal(resp)
	if !strings.Contains(string(b), `"resources":[]`) {
		t.Fatalf("resources/list missing empty resources field: %s", b)
	}
	resp, _ = dispatchJSON([]byte(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`), svc)
	b, _ = json.Marshal(resp)
	if !strings.Contains(string(b), `"protocolVersion":"2024-11-05"`) {
		t.Fatalf("supported client protocolVersion was not echoed: %s", b)
	}
	resp, _ = dispatchJSON([]byte(`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`), svc)
	b, _ = json.Marshal(resp)
	if !strings.Contains(string(b), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("unsupported version should fall back to latest: %s", b)
	}
}

// Issue #46: HTTP mode shuts down gracefully and reports a clean exit.
func TestServeHTTPGracefulShutdown(t *testing.T) {
	svc, _ := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveHTTP(ctx, "127.0.0.1:0", svc, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown should return nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down after cancellation")
	}
}

// Fresh-audit F1: a forged artifact record must not read files outside the
// artifacts dir — readArtifactRecord reconstructs the path from the id.
func TestArtifactRecordPathCannotEscape(t *testing.T) {
	svc, root := testService(t)
	artDir := filepath.Join(svc.cfg.StateDir, "artifacts")
	if err := os.MkdirAll(artDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "outside-secret.txt")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A forged record whose Path points at an arbitrary absolute file.
	forged := ArtifactRecord{ID: "evil", Kind: "text", Path: secret, Bytes: 9}
	b, _ := json.Marshal(forged)
	if err := os.WriteFile(filepath.Join(artDir, "evil.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	rec, err := svc.readArtifactRecord("evil")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Path == secret {
		t.Fatal("readArtifactRecord trusted the forged absolute path")
	}
	want := filepath.Join(artDir, "evil.txt")
	if rec.Path != want {
		t.Fatalf("path not reconstructed: got %q want %q", rec.Path, want)
	}
	// artifact.read_range must not return the out-of-workspace file's bytes.
	got, err := svc.artifactReadRange(map[string]any{"artifact_id": "evil"})
	if err == nil {
		if text, _ := got["text"].(string); strings.Contains(text, "TOPSECRET") {
			t.Fatal("artifact.read_range leaked an out-of-workspace file")
		}
	}
}

// Fresh-audit F10: a preset that renders to an empty argv must be rejected, not
// panic run() (which would crash the process for async jobs).
func TestEmptyRenderedCommandRejected(t *testing.T) {
	svc, _ := testService(t)
	_, _, err := svc.jobs.RunCommand(contextWithBackground(), JobSpec{Kind: "command", Workspace: "test", Cwd: ".", Command: []string{}}, false)
	if err == nil {
		t.Fatal("expected an empty-argv command to be rejected")
	}
	if !strings.Contains(err.Error(), "empty argv") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Fresh-audit F6: a preset whose command uses {prompt} with no matching arg is
// rejected at config load instead of failing every execution.
func TestPresetPromptPlaceholderRejectedAtLoad(t *testing.T) {
	err := validateCommandPlaceholders("preset x", []string{"tool", "{prompt}"}, map[string]bool{})
	if err == nil {
		t.Fatal("expected {prompt} with no matching preset arg to be rejected")
	}
	// A declared arg named prompt is still accepted.
	if err := validateCommandPlaceholders("preset x", []string{"tool", "{prompt}"}, map[string]bool{"prompt": true}); err != nil {
		t.Fatalf("declared prompt arg should be accepted: %v", err)
	}
}

// Fresh-audit F11: when run() cannot open its log file, it must still emit
// job_finished and call OnFinish so agent sessions do not wedge in "running".
func TestJobFinishBookkeepingOnLogOpenFailure(t *testing.T) {
	svc, root := testService(t)
	var finished bool
	spec := JobSpec{
		Kind:      "command",
		Workspace: "test",
		Cwd:       ".",
		Command:   []string{"true"},
		EventTool: "app.run_preset",
		OnFinish:  func(JobRecord) { finished = true },
	}
	rec, err := svc.jobs.newRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Force the log open to fail by pointing LogPath at a directory.
	badLog := filepath.Join(root, "logdir")
	if err := os.MkdirAll(badLog, 0o755); err != nil {
		t.Fatal(err)
	}
	rec.LogPath = badLog
	out := svc.jobs.run(contextWithBackground(), spec, rec)
	if out.State != JobFailed {
		t.Fatalf("expected JobFailed on log-open failure, got %s", out.State)
	}
	if !finished {
		t.Fatal("OnFinish was not called on the log-open failure path")
	}
	events, _ := svc.events.Tail(20, EventFilter{Kind: "job_finished"})
	if len(events) == 0 {
		t.Fatal("no job_finished event emitted on the log-open failure path")
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
