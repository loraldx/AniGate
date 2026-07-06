package anigate

import (
	"os"
	"path/filepath"
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
