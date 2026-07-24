package anigate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type pathPolicy struct {
	workspaces map[string]Workspace
	// stateDir is the resolved gateway state directory. Any workspace path that
	// lands inside it is rejected, so a caller cannot reach state_dir through a
	// workspace (e.g. to forge publish tokens, job/task/artifact records, or the
	// audit stream) even when state_dir physically sits inside a workspace root.
	stateDir string
}

type resolvedPath struct {
	Workspace Workspace `json:"-"`
	Abs       string    `json:"abs"`
	Rel       string    `json:"rel"`
}

func newPathPolicy(workspaces []Workspace, stateDir string) pathPolicy {
	m := make(map[string]Workspace, len(workspaces))
	for _, ws := range workspaces {
		m[ws.Name] = ws
	}
	return pathPolicy{workspaces: m, stateDir: resolveStateDir(stateDir)}
}

// resolveStateDir returns the absolute, symlink-resolved state directory used
// for the confinement check. Best-effort: an unresolvable value falls back to
// the lexical absolute path so the check still applies.
func resolveStateDir(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return filepath.Clean(stateDir)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
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
	if p.insideStateDir(candidate) {
		return resolvedPath{}, fmt.Errorf("path is inside the gateway state directory")
	}
	return resolvedPath{Workspace: ws, Abs: candidate, Rel: rel}, nil
}

// insideStateDir reports whether the resolved candidate is the state directory
// or a path under it.
func (p pathPolicy) insideStateDir(candidate string) bool {
	if p.stateDir == "" {
		return false
	}
	rel, err := filepath.Rel(p.stateDir, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
