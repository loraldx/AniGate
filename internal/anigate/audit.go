package anigate

import (
	"time"
)

func (s *Service) auditSummary(args map[string]any) (map[string]any, error) {
	sinceSec := int64ArgDefault(args, "since_sec", 24*3600)
	if sinceSec <= 0 {
		sinceSec = 24 * 3600
	}
	if sinceSec > 31*24*3600 {
		sinceSec = 31 * 24 * 3600
	}
	cutoff := time.Now().UTC().Add(-time.Duration(sinceSec) * time.Second)
	byKind := map[string]int{}
	byTool := map[string]int{}
	failures := 0
	scanned := 0
	var recentFailures []Event
	err := s.events.scanEvents(EventFilter{}, func(ev Event) {
		if ev.Time.Before(cutoff) {
			return
		}
		scanned++
		byKind[ev.Kind]++
		if ev.Tool != "" {
			byTool[ev.Tool]++
		}
		if !ev.OK {
			failures++
			// Keep a sliding tail so the field really holds the newest failures.
			recentFailures = append(recentFailures, ev)
			if len(recentFailures) > 10 {
				copy(recentFailures, recentFailures[1:])
				recentFailures = recentFailures[:10]
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"since":           cutoff.Format(time.RFC3339),
		"events_scanned":  scanned,
		"by_kind":         byKind,
		"by_tool":         byTool,
		"failures":        failures,
		"recent_failures": recentFailures,
	}, nil
}
