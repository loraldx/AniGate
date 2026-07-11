package anigate

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const gitToolTimeout = 15 * time.Second

func (s *Service) gitStatus(args map[string]any) (map[string]any, error) {
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	maxBytes := int64ArgDefault(args, "max_bytes", s.cfg.MaxReadBytes)
	full, err := s.runGitOutput(rp.Abs, "status", "--porcelain=v1", "--branch")
	if err != nil {
		return nil, err
	}
	out, truncated, ref, err := s.boundedTextArtifact("git.status", rp.Rel, full, maxBytes, map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel})
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"workspace": rp.Workspace.Name,
		"path":      rp.Rel,
		"status":    out,
		"truncated": truncated,
	}
	s.addArtifactFields(result, ref)
	return result, nil
}

func (s *Service) gitDiff(args map[string]any) (map[string]any, error) {
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	maxBytes := int64ArgDefault(args, "max_bytes", s.cfg.MaxReadBytes)
	cmdArgs := []string{"diff"}
	if boolArg(args, "cached") {
		cmdArgs = append(cmdArgs, "--cached")
	}
	paths, err := stringSliceArg(args, "paths")
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		cmdArgs = append(cmdArgs, "--")
		for _, p := range paths {
			clean := filepath.Clean(p)
			if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
				return nil, fmt.Errorf("invalid diff path %q", p)
			}
			cmdArgs = append(cmdArgs, clean)
		}
	}
	full, err := s.runGitOutput(rp.Abs, cmdArgs...)
	if err != nil {
		return nil, err
	}
	out, truncated, ref, err := s.boundedTextArtifact("git.diff", rp.Rel, full, maxBytes, map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel})
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"workspace": rp.Workspace.Name,
		"path":      rp.Rel,
		"diff":      out,
		"truncated": truncated,
	}
	s.addArtifactFields(result, ref)
	return result, nil
}

func (s *Service) gitLog(args map[string]any) (map[string]any, error) {
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	limit := intArgDefault(args, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	maxBytes := int64ArgDefault(args, "max_bytes", s.cfg.MaxReadBytes)
	cmdArgs := []string{"log", "--oneline", "--decorate", "-n", fmt.Sprint(limit)}
	paths, err := stringSliceArg(args, "paths")
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		cmdArgs = append(cmdArgs, "--")
		for _, p := range paths {
			clean, err := cleanGitPath(p)
			if err != nil {
				return nil, err
			}
			cmdArgs = append(cmdArgs, clean)
		}
	}
	full, err := s.runGitOutput(rp.Abs, cmdArgs...)
	if err != nil {
		return nil, err
	}
	out, truncated, ref, err := s.boundedTextArtifact("git.log", rp.Rel, full, maxBytes, map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel, "log": out, "truncated": truncated}
	s.addArtifactFields(result, ref)
	return result, nil
}

func (s *Service) gitShow(args map[string]any) (map[string]any, error) {
	rp, err := s.policy.resolve(stringArg(args, "workspace"), stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	rev := stringArgDefault(args, "rev", "HEAD")
	if !validGitRev(rev) {
		return nil, fmt.Errorf("invalid git rev")
	}
	maxBytes := int64ArgDefault(args, "max_bytes", s.cfg.MaxReadBytes)
	cmdArgs := []string{"show", "--no-ext-diff", "--no-color", rev}
	paths, err := stringSliceArg(args, "paths")
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		cmdArgs = append(cmdArgs, "--")
		for _, p := range paths {
			clean, err := cleanGitPath(p)
			if err != nil {
				return nil, err
			}
			cmdArgs = append(cmdArgs, clean)
		}
	}
	full, err := s.runGitOutput(rp.Abs, cmdArgs...)
	if err != nil {
		return nil, err
	}
	out, truncated, ref, err := s.boundedTextArtifact("git.show", rev, full, maxBytes, map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel, "rev": rev})
	if err != nil {
		return nil, err
	}
	result := map[string]any{"workspace": rp.Workspace.Name, "path": rp.Rel, "rev": rev, "show": out, "truncated": truncated}
	s.addArtifactFields(result, ref)
	return result, nil
}

func (s *Service) patchApply(args map[string]any) (map[string]any, error) {
	workspace := stringArg(args, "workspace")
	if err := s.workspaceAllows(workspace, "write"); err != nil {
		return nil, err
	}
	rp, err := s.policy.resolve(workspace, stringArgDefault(args, "path", "."))
	if err != nil {
		return nil, err
	}
	patch := stringArg(args, "patch")
	if patch == "" {
		return nil, fmt.Errorf("patch is required")
	}
	if int64(len(patch)) > s.cfg.MaxReadBytes {
		return nil, fmt.Errorf("patch exceeds max_read_bytes")
	}
	paths, err := validatePatchPaths(patch)
	if err != nil {
		return nil, err
	}
	checkOnly := boolArg(args, "check_only")
	if err := runGitApply(rp.Abs, patch, true); err != nil {
		return nil, err
	}
	applied := false
	if !checkOnly {
		if err := runGitApply(rp.Abs, patch, false); err != nil {
			return nil, err
		}
		applied = true
	}
	return map[string]any{
		"workspace":  rp.Workspace.Name,
		"path":       rp.Rel,
		"check_only": checkOnly,
		"applied":    applied,
		"paths":      paths,
	}, nil
}

func (s *Service) runGit(cwd string, maxBytes int64, args ...string) (string, bool, error) {
	out, err := s.runGitOutput(cwd, args...)
	if err != nil {
		return "", false, err
	}
	if maxBytes <= 0 || maxBytes > s.cfg.MaxReadBytes {
		maxBytes = s.cfg.MaxReadBytes
	}
	truncated := int64(len(out)) > maxBytes
	if truncated {
		out = out[:maxBytes]
	}
	return out, truncated, nil
}

func (s *Service) runGitOutput(cwd string, args ...string) (string, error) {
	return runHostCommand(cwd, hostCmdOpts{}, "git", args...)
}

func runGitApply(cwd, patch string, check bool) error {
	args := []string{"apply", "--whitespace=nowarn"}
	if check {
		args = append(args, "--check")
	}
	_, err := runHostCommand(cwd, hostCmdOpts{Stdin: patch}, "git", args...)
	return err
}

func pathEnv() string {
	pathValue := getenv("PATH")
	if pathValue == "" {
		return "/usr/local/bin:/usr/bin:/bin"
	}
	return pathValue
}

func stringSliceArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must contain only strings", key)
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

var gitRevRe = regexp.MustCompile(`^[A-Za-z0-9._/@:~^-]+$`)

func validGitRev(rev string) bool {
	return rev != "" && !strings.HasPrefix(rev, "-") && !strings.Contains(rev, "..") && gitRevRe.MatchString(rev)
}

func cleanGitPath(p string) (string, error) {
	clean := filepath.Clean(p)
	if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid git path %q", p)
	}
	return clean, nil
}

func validatePatchPaths(patch string) ([]string, error) {
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(patch, "\n") {
		var candidates []string
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				candidates = append(candidates, strings.TrimPrefix(parts[2], "a/"), strings.TrimPrefix(parts[3], "b/"))
			}
		}
		if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] != "/dev/null" {
				candidates = append(candidates, strings.TrimPrefix(strings.TrimPrefix(fields[1], "a/"), "b/"))
			}
		}
		for _, candidate := range candidates {
			clean, err := cleanGitPath(candidate)
			if err != nil {
				return nil, err
			}
			if !seen[clean] {
				seen[clean] = true
				paths = append(paths, clean)
			}
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("patch contains no file paths")
	}
	return paths, nil
}
