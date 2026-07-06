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
