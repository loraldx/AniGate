package anigate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ArtifactRecord struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name,omitempty"`
	Path      string         `json:"path"`
	Bytes     int64          `json:"bytes"`
	Truncated bool           `json:"truncated,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type ArtifactRef struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Path      string   `json:"path"`
	Bytes     int64    `json:"bytes"`
	Truncated bool     `json:"truncated,omitempty"`
	Next      []string `json:"next,omitempty"`
}

func (s *Service) artifactList(args map[string]any) (map[string]any, error) {
	limit := intArgDefault(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	kind := stringArg(args, "kind")
	records, err := s.listArtifacts(limit, kind)
	if err != nil {
		return nil, err
	}
	return map[string]any{"artifacts": records, "count": len(records)}, nil
}

func (s *Service) artifactReadRange(args map[string]any) (map[string]any, error) {
	rec, err := s.readArtifactRecord(stringArg(args, "artifact_id"))
	if err != nil {
		return nil, err
	}
	offset := int64ArgDefault(args, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	maxBytes := int64ArgDefault(args, "max_bytes", s.cfg.MaxReadBytes)
	if maxBytes <= 0 || maxBytes > s.cfg.MaxReadBytes {
		maxBytes = s.cfg.MaxReadBytes
	}
	f, err := os.Open(rec.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if offset > info.Size() {
		offset = info.Size()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, maxBytes+1)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	truncated := int64(n) > maxBytes
	if truncated {
		n = int(maxBytes)
	}
	return map[string]any{
		"artifact":  rec,
		"offset":    offset,
		"bytes":     n,
		"size":      info.Size(),
		"truncated": truncated || offset+int64(n) < info.Size(),
		"text":      string(buf[:n]),
	}, nil
}

func (s *Service) artifactSearch(args map[string]any) (map[string]any, error) {
	query := stringArg(args, "query")
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	maxResults := intArgDefault(args, "max_results", 50)
	if maxResults <= 0 {
		maxResults = 50
	}
	if maxResults > 200 {
		maxResults = 200
	}
	caseSensitive := boolArg(args, "case_sensitive")
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(query)
	}
	records, err := s.listArtifacts(1000, stringArg(args, "kind"))
	if err != nil {
		return nil, err
	}
	var results []map[string]any
	for _, rec := range records {
		if len(results) >= maxResults {
			break
		}
		f, err := os.Open(rec.Path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			hay := line
			if !caseSensitive {
				hay = strings.ToLower(line)
			}
			if strings.Contains(hay, needle) {
				results = append(results, map[string]any{
					"artifact_id": rec.ID,
					"kind":        rec.Kind,
					"name":        rec.Name,
					"line":        lineNo,
					"preview":     trimPreview(line, 240),
				})
				if len(results) >= maxResults {
					break
				}
			}
		}
		_ = f.Close()
	}
	return map[string]any{
		"query":     query,
		"results":   results,
		"count":     len(results),
		"truncated": len(results) >= maxResults,
	}, nil
}

func (s *Service) artifactStats() (map[string]any, error) {
	records, err := s.listArtifacts(10000, "")
	if err != nil {
		return nil, err
	}
	byKind := map[string]int{}
	var total int64
	for _, rec := range records {
		byKind[rec.Kind]++
		total += rec.Bytes
	}
	return map[string]any{
		"count":        len(records),
		"bytes":        total,
		"by_kind":      byKind,
		"state_subdir": filepath.Join(s.cfg.StateDir, "artifacts"),
	}, nil
}

func (s *Service) boundedTextArtifact(kind, name, text string, maxBytes int64, meta map[string]any) (string, bool, *ArtifactRef, error) {
	if maxBytes <= 0 || maxBytes > s.cfg.MaxReadBytes {
		maxBytes = s.cfg.MaxReadBytes
	}
	truncated := int64(len(text)) > maxBytes
	var ref *ArtifactRef
	if truncated {
		rec, err := s.saveArtifactText(kind, name, text, meta)
		if err != nil {
			return "", false, nil, err
		}
		ref = &ArtifactRef{
			ID:        rec.ID,
			Kind:      rec.Kind,
			Path:      rec.Path,
			Bytes:     rec.Bytes,
			Truncated: rec.Truncated,
			Next: []string{
				"artifact.read_range",
				"artifact.search",
			},
		}
		text = text[:maxBytes]
	}
	return text, truncated, ref, nil
}

func (s *Service) addArtifactFields(out map[string]any, ref *ArtifactRef) {
	if ref == nil {
		return
	}
	out["artifact"] = ref
	out["next"] = mergeNext(out["next"], ref.Next)
}

func (s *Service) saveArtifactText(kind, name, text string, meta map[string]any) (ArtifactRecord, error) {
	if !validName(kind) {
		kind = "text"
	}
	id, err := newJobID()
	if err != nil {
		return ArtifactRecord{}, err
	}
	dir := filepath.Join(s.cfg.StateDir, "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ArtifactRecord{}, err
	}
	limit := s.cfg.MaxArtifactBytes
	if limit <= 0 {
		limit = defaultMaxArtifactBytes
	}
	truncated := int64(len(text)) > limit
	if truncated {
		text = text[:limit]
	}
	path := filepath.Join(dir, id+".txt")
	if err := writeFileAtomic(path, []byte(text), 0o600); err != nil {
		return ArtifactRecord{}, err
	}
	rec := ArtifactRecord{
		ID:        id,
		Kind:      kind,
		Name:      trimPreview(name, 120),
		Path:      path,
		Bytes:     int64(len(text)),
		Truncated: truncated,
		CreatedAt: time.Now().UTC(),
		Meta:      meta,
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return ArtifactRecord{}, err
	}
	if err := writeFileAtomic(filepath.Join(dir, id+".json"), b, 0o600); err != nil {
		return ArtifactRecord{}, err
	}
	s.events.Append(Event{Kind: "artifact_created", OK: true, Fields: map[string]any{"artifact_id": id, "kind": kind, "name": name}})
	return rec, nil
}

func mergeNext(existing any, more []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	switch v := existing.(type) {
	case []string:
		for _, name := range v {
			add(name)
		}
	case []any:
		for _, raw := range v {
			if name, ok := raw.(string); ok {
				add(name)
			}
		}
	}
	for _, name := range more {
		add(name)
	}
	return out
}

func (s *Service) readArtifactRecord(id string) (ArtifactRecord, error) {
	if !validName(id) {
		return ArtifactRecord{}, fmt.Errorf("invalid artifact_id")
	}
	b, err := os.ReadFile(filepath.Join(s.cfg.StateDir, "artifacts", id+".json"))
	if err != nil {
		return ArtifactRecord{}, err
	}
	var rec ArtifactRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return ArtifactRecord{}, err
	}
	// Never trust the stored Path: artifact content always lives at
	// <state_dir>/artifacts/<id>.txt. Reconstructing it from the validated id
	// stops a forged record's Path (writable if state_dir sits inside a
	// workspace) from escaping into an arbitrary os.Open outside confinement.
	rec.Path = filepath.Join(s.cfg.StateDir, "artifacts", id+".txt")
	return rec, nil
}

func (s *Service) listArtifacts(limit int, kind string) ([]ArtifactRecord, error) {
	dir := filepath.Join(s.cfg.StateDir, "artifacts")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []ArtifactRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []ArtifactRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := s.readArtifactRecord(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		if kind != "" && rec.Kind != kind {
			continue
		}
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.After(records[j].CreatedAt)
	})
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}
