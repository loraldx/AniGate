package anigate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type pathPolicy struct {
	workspaces map[string]Workspace
}

type resolvedPath struct {
	Workspace Workspace `json:"-"`
	Abs       string    `json:"abs"`
	Rel       string    `json:"rel"`
}

func newPathPolicy(workspaces []Workspace) pathPolicy {
	m := make(map[string]Workspace, len(workspaces))
	for _, ws := range workspaces {
		m[ws.Name] = ws
	}
	return pathPolicy{workspaces: m}
}

func (p pathPolicy) workspace(name string) (Workspace, error) {
	if name == "" {
		if len(p.workspaces) == 1 {
			for _, ws := range p.workspaces {
				return ws, nil
			}
		}
		return Workspace{}, fmt.Errorf("workspace is required")
	}
	ws, ok := p.workspaces[name]
	if !ok {
		return Workspace{}, fmt.Errorf("unknown workspace %q", name)
	}
	return ws, nil
}

func (p pathPolicy) resolve(workspaceName, requested string) (resolvedPath, error) {
	ws, err := p.workspace(workspaceName)
	if err != nil {
		return resolvedPath{}, err
	}
	root, err := filepath.Abs(ws.Path)
	if err != nil {
		return resolvedPath{}, err
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot
	}
	if requested == "" {
		requested = "."
	}
	var candidate string
	if filepath.IsAbs(requested) {
		candidate = filepath.Clean(requested)
	} else {
		candidate = filepath.Join(root, requested)
	}
	if _, err := os.Stat(candidate); err == nil {
		if realCandidate, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = realCandidate
		}
	} else {
		// The target does not exist yet, so EvalSymlinks can't resolve it.
		// Resolve the deepest existing ancestor instead so a new file under an
		// escaping symlink is still confined, not just checked lexically.
		candidate = resolveDeepestExisting(candidate)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return resolvedPath{}, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return resolvedPath{}, fmt.Errorf("path escapes workspace %q", ws.Name)
	}
	return resolvedPath{Workspace: ws, Abs: candidate, Rel: rel}, nil
}

// resolveDeepestExisting resolves the symlinks of the deepest existing ancestor
// of path and re-attaches the remaining (non-existent) suffix. This makes
// confinement of a not-yet-created path account for symlinked parents instead
// of trusting the lexical path.
func resolveDeepestExisting(path string) string {
	path = filepath.Clean(path)
	suffix := ""
	cur := path
	for {
		if _, err := os.Stat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				real = cur
			}
			if suffix == "" {
				return real
			}
			return filepath.Join(real, suffix)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return path
		}
		suffix = filepath.Join(filepath.Base(cur), suffix)
		cur = parent
	}
}
