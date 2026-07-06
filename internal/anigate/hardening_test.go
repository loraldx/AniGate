package anigate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
