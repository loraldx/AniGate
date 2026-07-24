package anigate

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) (*Service, string) {
	return testServiceWithProduct(t, ProductLineMax)
}

func testServiceWithProduct(t *testing.T, productLine ProductLine) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		StateDir:           filepath.Join(root, "state"),
		MaxReadBytes:       1024,
		MaxSearchFileBytes: 1024,
		MaxSearchResults:   10,
		MaxJobLogBytes:     1024,
		Workspaces: []Workspace{{
			Name:     "test",
			Path:     root,
			ReadOnly: true,
			Profile:  "agent",
		}},
		Presets: []Preset{
			{
				Name:        "echo_ok",
				Description: "echo ok",
				Workspace:   "test",
				Cwd:         ".",
				Command:     []string{"printf", "{word}"},
				Args: []PresetArg{{
					Name:    "word",
					Type:    "string",
					Default: "ok",
					Enum:    []string{"ok", "hello"},
				}},
				TimeoutSec: 5,
			},
			{
				Name:        "sleep_short",
				Description: "sleep for cancellation tests",
				Workspace:   "test",
				Cwd:         ".",
				Command:     []string{"sleep", "5"},
				TimeoutSec:  30,
				Async:       true,
			},
		},
		Agents: []Agent{{
			Name:               "echo_agent",
			Description:        "echo test agent",
			Provider:           "echo",
			Workspace:          "test",
			Cwd:                ".",
			Command:            []string{"printf", "{prompt}"},
			TimeoutSec:         5,
			MaxHistoryMessages: 10,
		}},
	}
	svc, err := NewServiceWithProductLine(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)), productLine)
	if err != nil {
		t.Fatal(err)
	}
	return svc, root
}

func TestPathPolicyRejectsEscape(t *testing.T) {
	svc, _ := testService(t)
	_, err := svc.policy.resolve("test", "../outside")
	if err == nil {
		t.Fatal("expected path escape rejection")
	}
}

func TestFSRead(t *testing.T) {
	svc, _ := testService(t)
	got, err := svc.fsRead(map[string]any{"workspace": "test", "path": "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != "alpha\nbeta\ngamma\n" {
		t.Fatalf("unexpected text: %#v", got["text"])
	}
}

func TestFileSearch(t *testing.T) {
	svc, _ := testService(t)
	got, err := svc.fileSearch(map[string]any{"workspace": "test", "path": ".", "query": "beta"})
	if err != nil {
		t.Fatal(err)
	}
	results := got["results"].([]map[string]any)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if results[0]["line"] != 2 {
		t.Fatalf("expected line 2, got %#v", results[0]["line"])
	}
}

func TestPolicyStatTreeAndPresetArgs(t *testing.T) {
	svc, _ := testService(t)
	if got, err := svc.policyInfo(); err != nil || got["version"] != Version {
		t.Fatalf("bad policy info: %#v err=%v", got, err)
	}
	if got, err := svc.fsStat(map[string]any{"workspace": "test", "path": "hello.txt"}); err != nil || got["is_dir"].(bool) {
		t.Fatalf("bad stat: %#v err=%v", got, err)
	}
	tree, err := svc.fsTree(map[string]any{"workspace": "test", "path": ".", "depth": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if tree["count"].(int) == 0 {
		t.Fatal("expected tree entries")
	}
	job, tail, err := svc.jobs.RunPreset(contextWithBackground(), "echo_ok", map[string]any{"word": "hello"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != JobDone || !strings.Contains(tail, "hello") {
		t.Fatalf("bad preset result: %#v tail=%q", job, tail)
	}
	if _, _, err := svc.jobs.RunPreset(contextWithBackground(), "echo_ok", map[string]any{"word": "bad"}, false); err == nil {
		t.Fatal("expected enum rejection")
	}
}

func TestMCPToolsCall(t *testing.T) {
	svc, _ := testService(t)
	args, _ := json.Marshal(map[string]any{"workspace": "test", "path": "hello.txt"})
	req, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params:  mustJSON(t, toolCallParams{Name: "fs.read", Arguments: args}),
	})
	resp, ok := dispatchJSON(req, svc)
	if !ok || resp.Error != nil {
		t.Fatalf("bad response: %#v", resp)
	}
}

func TestMiniProductToolsArePreviewCore(t *testing.T) {
	svc, _ := testServiceWithProduct(t, ProductLineMini)
	got := toolNamesForTest(svc.Tools())
	want := []string{
		"policy.info",
		"sys.info",
		"fs.list",
		"fs.read",
		"fs.stat",
		"fs.tree",
		"file.search",
		"artifact.list",
		"artifact.read_range",
		"artifact.search",
		"artifact.stats",
		"fs.write_preview",
		"git.status",
		"git.diff",
		"git.log",
		"git.show",
		"context.health",
		"handoff.create",
		"handoff.resume",
		"handoff.search",
		"handoff.digest",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected mini tools:\nwant %v\ngot  %v", want, got)
	}
	for _, name := range got {
		if strings.HasPrefix(name, "agent.") || strings.HasPrefix(name, "publish.") || name == "file.edit_apply" || name == "patch.apply" {
			t.Fatalf("mini exposed max tool %q", name)
		}
	}
}

func TestMiniProductCallToolRejectsMaxTools(t *testing.T) {
	svc, _ := testServiceWithProduct(t, ProductLineMini)
	for _, name := range []string{"file.edit_apply", "patch.apply", "agent.message_send", "publish.preview"} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.CallTool(name, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "not available in mini product line") {
				t.Fatalf("expected mini product gate for %s, got %v", name, err)
			}
		})
	}
}

func TestMiniProductWritePreviewDoesNotWrite(t *testing.T) {
	svc, root := testServiceWithProduct(t, ProductLineMini)
	args, _ := json.Marshal(map[string]any{"workspace": "test", "path": "hello.txt", "content": "preview\n"})
	got, err := svc.CallTool("fs.write_preview", args)
	if err != nil {
		t.Fatalf("mini write preview failed: %v", err)
	}
	result := got.(map[string]any)
	if result["would_write"] != true {
		t.Fatalf("bad preview result: %#v", result)
	}
	b, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("write preview modified disk: %q", b)
	}
}

func TestMaxProductToolsRemainComplete(t *testing.T) {
	svc, _ := testServiceWithProduct(t, ProductLineMax)
	got := toolNamesForTest(svc.Tools())
	if len(got) != 56 {
		t.Fatalf("expected 56 max tools, got %d: %v", len(got), got)
	}
	wantPresent := []string{"file.edit_apply", "patch.apply", "agent.message_send", "publish.preview"}
	have := map[string]bool{}
	for _, name := range got {
		have[name] = true
	}
	for _, name := range wantPresent {
		if !have[name] {
			t.Fatalf("max tools missing %s", name)
		}
	}
}

func TestPolicyInfoReportsProductLineAndFilteredTools(t *testing.T) {
	mini, _ := testServiceWithProduct(t, ProductLineMini)
	info, err := mini.policyInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info["product_line"] != ProductLineMini {
		t.Fatalf("unexpected product line: %#v", info["product_line"])
	}
	tools := info["tools"].([]MCPTool)
	names := toolNamesForTest(tools)
	if len(names) != 21 {
		t.Fatalf("expected 21 mini policy tools, got %d", len(names))
	}
	for _, name := range names {
		if name == "file.edit_apply" || name == "patch.apply" || strings.HasPrefix(name, "agent.") || strings.HasPrefix(name, "publish.") {
			t.Fatalf("policy.info exposed max tool in mini: %s", name)
		}
	}
	productLines := info["product_lines"].(map[string]any)
	if productLines["mini"] == nil || productLines["max"] == nil {
		t.Fatalf("missing product line metadata: %#v", productLines)
	}
}

func TestInitializeReportsVersion(t *testing.T) {
	svc, _ := testService(t)
	req, _ := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "initialize",
	})
	resp, ok := dispatchJSON(req, svc)
	if !ok || resp.Error != nil {
		t.Fatalf("bad response: %#v", resp)
	}
	b, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(b), Version) {
		t.Fatalf("initialize result missing version %s: %s", Version, b)
	}
}

func toolNamesForTest(tools []MCPTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestVersionFileMatchesConstant(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != Version {
		t.Fatalf("VERSION file %q does not match code %q", strings.TrimSpace(string(b)), Version)
	}
}

func TestGitStatusAndDiff(t *testing.T) {
	svc, root := testService(t)
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "add", "hello.txt")
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("alpha\nbeta\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := svc.gitStatus(map[string]any{"workspace": "test", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status["status"].(string), "hello.txt") {
		t.Fatalf("status missing file: %#v", status["status"])
	}

	diff, err := svc.gitDiff(map[string]any{"workspace": "test", "path": ".", "paths": []any{"hello.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff["diff"].(string), "+delta") {
		t.Fatalf("diff missing changed line: %#v", diff["diff"])
	}
}

func TestGitLogShowAndPatchApply(t *testing.T) {
	svc, root := testService(t)
	svc.cfg.Workspaces[0].ReadOnly = false
	svc.policy = newPathPolicy(svc.cfg.Workspaces, svc.cfg.StateDir)
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.email", "test@example.com")
	runGitForTest(t, root, "config", "user.name", "Tester")
	runGitForTest(t, root, "add", "hello.txt")
	runGitForTest(t, root, "commit", "-m", "initial")

	logOut, err := svc.gitLog(map[string]any{"workspace": "test", "path": ".", "limit": float64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logOut["log"].(string), "initial") {
		t.Fatalf("git log missing commit: %#v", logOut["log"])
	}
	showOut, err := svc.gitShow(map[string]any{"workspace": "test", "path": ".", "rev": "HEAD", "max_bytes": float64(4096)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(showOut["show"].(string), "initial") {
		t.Fatalf("git show missing commit: %#v", showOut["show"])
	}

	preview, err := svc.fsWritePreview(map[string]any{"workspace": "test", "path": "hello.txt", "content": "alpha\nbeta\npatched\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview["diff"].(string), "+patched") {
		t.Fatalf("preview missing diff: %#v", preview["diff"])
	}
	edited, err := svc.fileEditApply(map[string]any{"workspace": "test", "path": "hello.txt", "content": "alpha\nbeta\nweb\n", "expected_old_text": "gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if edited["actor"] != "web_gpt_direct" || !strings.Contains(edited["diff"].(string), "+web") {
		t.Fatalf("bad direct edit result: %#v", edited)
	}
	if _, err := svc.fileEditApply(map[string]any{"workspace": "test", "path": "hello.txt", "content": "bad\n", "expected_sha256": "bad"}); err == nil {
		t.Fatal("expected sha mismatch rejection")
	}
	if _, err := svc.fileEditApply(map[string]any{"workspace": "test", "path": "new.txt", "content": "new\n"}); err == nil {
		t.Fatal("expected create=false rejection")
	}
	patch := "diff --git a/hello.txt b/hello.txt\n--- a/hello.txt\n+++ b/hello.txt\n@@ -1,3 +1,3 @@\n alpha\n beta\n-gamma\n+patched\n"
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	applied, err := svc.patchApply(map[string]any{"workspace": "test", "path": ".", "patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	if applied["applied"] != true {
		t.Fatalf("expected patch applied: %#v", applied)
	}
	b, _ := os.ReadFile(filepath.Join(root, "hello.txt"))
	if !strings.Contains(string(b), "patched") {
		t.Fatalf("file not patched: %s", b)
	}
	if _, err := validatePatchPaths("diff --git a/../x b/../x\n--- a/../x\n+++ b/../x\n"); err == nil {
		t.Fatal("expected escaping patch path rejection")
	}
}

func TestFileEditApplyRejectsReadOnlyWorkspace(t *testing.T) {
	svc, _ := testService(t)
	if _, err := svc.fileEditApply(map[string]any{"workspace": "test", "path": "hello.txt", "content": "blocked\n"}); err == nil {
		t.Fatal("expected read-only workspace rejection")
	}
}

func TestMiniReaderAllowsPreviewButRejectsMutation(t *testing.T) {
	svc, root := testService(t)
	ws := Workspace{Name: "test", Path: root, ReadOnly: true, Profile: "reader"}
	svc.cfg.Workspaces = []Workspace{ws}
	svc.policy = newPathPolicy(svc.cfg.Workspaces, svc.cfg.StateDir)
	svc.jobs.policy = svc.policy

	preview, err := svc.fsWritePreview(map[string]any{"workspace": "test", "path": "hello.txt", "content": "preview\n"})
	if err != nil {
		t.Fatalf("mini reader should allow write preview: %v", err)
	}
	if preview["would_write"] != true {
		t.Fatalf("expected preview result: %#v", preview)
	}
	if _, err := svc.fileEditApply(map[string]any{"workspace": "test", "path": "hello.txt", "content": "blocked\n"}); err == nil {
		t.Fatal("expected mini reader to reject direct edit")
	}
	if _, err := svc.patchApply(map[string]any{"workspace": "test", "path": ".", "patch": "diff --git a/hello.txt b/hello.txt\n"}); err == nil {
		t.Fatal("expected mini reader to reject patch apply")
	}
	if _, _, err := svc.jobs.RunPreset(contextWithBackground(), "echo_ok", nil, false); err == nil {
		t.Fatal("expected mini reader to reject preset execution")
	}
	if _, err := svc.agentSessionStart(map[string]any{"agent": "echo_agent"}); err == nil {
		t.Fatal("expected mini reader to reject agent sessions")
	}
}

func TestJobListCancelAndAuditSummary(t *testing.T) {
	svc, _ := testService(t)
	job, _, err := svc.jobs.RunPreset(contextWithBackground(), "sleep_short", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.jobs.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var rec JobRecord
	for time.Now().Before(deadline) {
		rec, _ = svc.jobs.Status(job.ID)
		if rec.State == JobCancelled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rec.State != JobCancelled {
		t.Fatalf("expected cancelled job, got %#v", rec)
	}
	list, err := svc.jobs.List(10, "")
	if err != nil || len(list) == 0 {
		t.Fatalf("bad job list len=%d err=%v", len(list), err)
	}
	summary, err := svc.auditSummary(map[string]any{"since_sec": float64(3600)})
	if err != nil {
		t.Fatal(err)
	}
	if summary["events_scanned"].(int) == 0 {
		t.Fatal("expected audit events")
	}
}

func TestHTTPAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if authorized(req, "secret") {
		t.Fatal("expected auth failure")
	}
	req.Header.Set("Authorization", "Bearer secret")
	if !authorized(req, "secret") {
		t.Fatal("expected bearer auth success")
	}
	if err := httpListenRequiresToken("127.0.0.1:8787", ""); err != nil {
		t.Fatalf("loopback without token should be allowed: %v", err)
	}
	if err := httpListenRequiresToken("0.0.0.0:8787", ""); err == nil {
		t.Fatal("expected non-loopback without token rejection")
	}
	if err := httpListenRequiresToken("0.0.0.0:8787", "secret"); err != nil {
		t.Fatalf("token should allow non-loopback: %v", err)
	}
}

func TestAgentSessionSync(t *testing.T) {
	svc, _ := testService(t)
	started, err := svc.agentSessionStart(map[string]any{"agent": "echo_agent", "title": "test"})
	if err != nil {
		t.Fatal(err)
	}
	session := started["session"].(AgentSession)
	if _, err := svc.agentMessageSend(map[string]any{"session_id": session.ID, "message": "hello linux", "async": false}); err != nil {
		t.Fatal(err)
	}
	tail, err := svc.agentMessagesTail(map[string]any{"session_id": session.ID, "limit": float64(10)})
	if err != nil {
		t.Fatal(err)
	}
	messages := tail["messages"].([]AgentMessage)
	if len(messages) < 2 {
		t.Fatalf("expected user and assistant messages: %#v", messages)
	}
	if !strings.Contains(messages[len(messages)-1].Text, "hello linux") {
		t.Fatalf("assistant output missing conversation: %#v", messages[len(messages)-1].Text)
	}
}

func TestAuditEventsTail(t *testing.T) {
	svc, _ := testService(t)
	if _, err := svc.CallTool("sys.info", nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.auditEventsTail(map[string]any{"limit": float64(10), "tool": "sys.info"})
	if err != nil {
		t.Fatal(err)
	}
	if got["count"].(int) == 0 {
		t.Fatalf("expected at least one audit event")
	}
}

func TestArtifactAndHandoff(t *testing.T) {
	svc, _ := testService(t)
	out := map[string]any{"next": []string{"publish.preview"}}
	svc.addArtifactFields(out, &ArtifactRef{ID: "a", Next: []string{"artifact.search", "publish.preview"}})
	merged := out["next"].([]string)
	if strings.Join(merged, ",") != "publish.preview,artifact.search" {
		t.Fatalf("unexpected merged next: %#v", merged)
	}
	svc.cfg.MaxReadBytes = 8
	got, err := svc.fsRead(map[string]any{"workspace": "test", "path": "hello.txt", "max_bytes": float64(8)})
	if err != nil {
		t.Fatal(err)
	}
	if got["artifact"] == nil {
		t.Fatalf("expected artifact for truncated read: %#v", got)
	}
	ref := got["artifact"].(*ArtifactRef)
	read, err := svc.artifactReadRange(map[string]any{"artifact_id": ref.ID, "offset": float64(0), "max_bytes": float64(32)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read["text"].(string), "alpha") {
		t.Fatalf("artifact range missing expected prefix: %#v", read["text"])
	}
	search, err := svc.artifactSearch(map[string]any{"query": "gamma"})
	if err != nil {
		t.Fatal(err)
	}
	if search["count"].(int) == 0 {
		t.Fatal("expected artifact search result")
	}
	health, err := svc.contextHealth()
	if err != nil {
		t.Fatal(err)
	}
	if health["level"] == "" {
		t.Fatalf("missing context health level: %#v", health)
	}
	handoff, err := svc.handoffCreate(map[string]any{"title": "artifact handoff", "goal": "continue safely", "notes": "test note"})
	if err != nil {
		t.Fatal(err)
	}
	rec := handoff["handoff"].(HandoffRecord)
	resume, err := svc.handoffResume(map[string]any{"handoff_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resume["summary"].(string), "continue safely") {
		t.Fatalf("bad handoff resume: %#v", resume)
	}
}

func TestProjectTaskPublishAndAgentBinding(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, source, "init", "-b", "main")
	runGitForTest(t, source, "config", "user.email", "test@example.com")
	runGitForTest(t, source, "config", "user.name", "Tester")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, source, "add", "README.md")
	runGitForTest(t, source, "commit", "-m", "seed")
	runGitForTest(t, root, "clone", "--bare", source, remote)

	cfg := Config{
		StateDir:         filepath.Join(root, "state"),
		MaxReadBytes:     1024,
		MaxArtifactBytes: 4096,
		MaxJobLogBytes:   1024,
		Workspaces: []Workspace{{
			Name:     "work",
			Path:     root,
			ReadOnly: false,
			Profile:  "agent",
		}},
		Projects: []Project{{
			Name:          "demo",
			Workspace:     "work",
			Path:          "clone",
			RemoteURL:     remote,
			DefaultBranch: "main",
			Provider:      "generic_git",
			AllowPush:     true,
			DefaultAgent:  "echo_agent",
		}},
		Agents: []Agent{{
			Name:               "echo_agent",
			Workspace:          "work",
			Command:            []string{"printf", "{prompt}"},
			TimeoutSec:         5,
			MaxHistoryMessages: 10,
		}},
	}
	svc, err := NewService(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.projectEnsure(map[string]any{"project": "demo"}); err != nil {
		t.Fatal(err)
	}
	started, err := svc.taskStart(map[string]any{"project": "demo", "title": "Change README"})
	if err != nil {
		t.Fatal(err)
	}
	task := started["task"].(TaskRecord)
	if task.Worktree == "" || task.Branch == "" {
		t.Fatalf("bad task: %#v", task)
	}
	sessionResult, err := svc.agentSessionStart(map[string]any{"agent": "echo_agent", "task_id": task.ID, "title": "task agent"})
	if err != nil {
		t.Fatal(err)
	}
	session := sessionResult["session"].(AgentSession)
	if session.TaskID != task.ID || session.Cwd != task.Worktree {
		t.Fatalf("agent session not bound to task: %#v task=%#v", session, task)
	}
	if err := os.WriteFile(filepath.Join(task.Worktree, "README.md"), []byte("seed\nchange\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preview, err := svc.taskFinishPreview(map[string]any{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview["diff"].(string), "+change") {
		t.Fatalf("preview missing diff: %#v", preview)
	}
	if _, err := svc.publishPreview(map[string]any{"task_id": task.ID}); err == nil {
		t.Fatal("expected dirty task publish preview rejection")
	}
	runGitForTest(t, task.Worktree, "config", "user.email", "test@example.com")
	runGitForTest(t, task.Worktree, "config", "user.name", "Tester")
	commitPreview, err := svc.taskCommitPreview(map[string]any{"task_id": task.ID, "message": "change readme"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commitPreview["diff"].(string), "+change") {
		t.Fatalf("commit preview missing diff: %#v", commitPreview)
	}
	if _, err := svc.taskCommit(map[string]any{"task_id": task.ID, "message": "change readme", "expected_diff_sha256": "bad"}); err == nil {
		t.Fatal("expected commit fingerprint mismatch")
	}
	committed, err := svc.taskCommit(map[string]any{"task_id": task.ID, "message": "change readme", "expected_diff_sha256": commitPreview["diff_sha256"]})
	if err != nil {
		t.Fatal(err)
	}
	if committed["commit"].(string) == "" {
		t.Fatalf("missing commit sha: %#v", committed)
	}
	pub, err := svc.publishPreview(map[string]any{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.publishBranch(map[string]any{"task_id": task.ID, "confirm_token": "bad"}); err == nil {
		t.Fatal("expected bad token rejection")
	}
	token := pub["confirm_token"].(string)
	if _, err := svc.publishBranch(map[string]any{"task_id": task.ID, "confirm_token": token}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.publishBranch(map[string]any{"task_id": task.ID, "confirm_token": token}); err == nil {
		t.Fatal("expected consumed publish token rejection")
	}
	branches := mustGitOutput(t, remote, "branch")
	if !strings.Contains(branches, task.Branch) {
		t.Fatalf("remote missing pushed branch %q: %s", task.Branch, branches)
	}
	digest, err := svc.handoffCreate(map[string]any{"task_id": task.ID, "goal": "continue project task"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(digest["next_prompt"].(string), task.ID) {
		t.Fatalf("handoff prompt missing task id: %#v", digest["next_prompt"])
	}
	doctor, err := svc.gateDoctor(map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if doctor["ok"] != true {
		t.Fatalf("expected doctor ok: %#v", doctor)
	}
	preflight, err := svc.projectPreflight(map[string]any{"project": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if preflight["checks"] == nil {
		t.Fatalf("expected preflight checks: %#v", preflight)
	}
	sampleCredentialURL := "https://" + "user:placeholder" + "@example.com/org/repo.git"
	if got := sanitizeCommandText("git " + sampleCredentialURL + " failed"); strings.Contains(got, "placeholder@") {
		t.Fatalf("expected sanitized command text, got %q", got)
	}
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func mustGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
