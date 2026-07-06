package anigate

import (
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
