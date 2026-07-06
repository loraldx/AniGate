package anigate

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxEventLineBytes bounds a single scanned NDJSON line. Event payloads are
// capped well below this when written, so a larger buffer keeps the audit read
// path resilient to unusually large (but legitimate) records.
const maxEventLineBytes = 8 * 1024 * 1024

type Event struct {
	Time      time.Time      `json:"time"`
	Kind      string         `json:"kind"`
	Tool      string         `json:"tool,omitempty"`
	JobID     string         `json:"job_id,omitempty"`
	Workspace string         `json:"workspace,omitempty"`
	Path      string         `json:"path,omitempty"`
	Preset    string         `json:"preset,omitempty"`
	OK        bool           `json:"ok"`
	Message   string         `json:"message,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

type EventLog struct {
	path string
	log  *slog.Logger
	mu   sync.Mutex
}

type EventFilter struct {
	Kind string
	Tool string
}

func NewEventLog(stateDir string, log *slog.Logger) (*EventLog, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	return &EventLog{path: filepath.Join(stateDir, "events.ndjson"), log: log}, nil
}

// Append durably records an audit event. It returns an error (and logs it) on
// any failure instead of silently dropping the event, so a lost security record
// is never invisible to the operator.
func (l *EventLog) Append(ev Event) error {
	if l == nil {
		return errors.New("event log is not configured")
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	if err := l.append(ev); err != nil {
		if l.log != nil {
			l.log.Error("audit event dropped", "kind", ev.Kind, "tool", ev.Tool, "err", err)
		}
		return err
	}
	return nil
}

func (l *EventLog) append(ev Event) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (l *EventLog) Tail(limit int, filter EventFilter) ([]Event, error) {
	if l == nil {
		return nil, errors.New("event log is not configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxEventLineBytes)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if filter.Kind != "" && ev.Kind != filter.Kind {
			continue
		}
		if filter.Tool != "" && ev.Tool != filter.Tool {
			continue
		}
		events = append(events, ev)
		if len(events) > limit {
			copy(events, events[len(events)-limit:])
			events = events[:limit]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}
