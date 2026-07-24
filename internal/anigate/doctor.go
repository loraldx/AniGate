package anigate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DoctorCheck struct {
	ID       string `json:"id"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Next     string `json:"next,omitempty"`
}

func (s *Service) gateDoctor(args map[string]any) (map[string]any, error) {
	var checks []DoctorCheck
	checks = append(checks, s.doctorStateDirCheck())
	checks = append(checks, s.doctorHTTPTokenCheck())
	checks = append(checks, s.doctorGitCheck())
	for _, ws := range s.cfg.Workspaces {
		checks = append(checks, s.doctorWorkspaceCheck(ws))
	}
	projectName := stringArg(args, "project")
	for _, project := range s.cfg.Projects {
		if projectName != "" && project.Name != projectName {
			continue
		}
		checks = append(checks, s.doctorProjectChecks(project)...)
	}
	if projectName != "" {
		if _, err := s.requireProject(projectName); err != nil {
			return nil, err
		}
	}
	for _, agent := range s.cfg.Agents {
		checks = append(checks, doctorAgentCheck(agent))
	}
	return map[string]any{
		"ok":     checksOK(checks),
		"checks": checks,
		"count":  len(checks),
	}, nil
}

func (s *Service) doctorStateDirCheck() DoctorCheck {
	if err := os.MkdirAll(s.cfg.StateDir, 0o700); err != nil {
		return doctorFail("state_dir_writable", "error", "state_dir is not writable: "+err.Error(), "fix state_dir permissions or config")
	}
	tmp, err := os.CreateTemp(s.cfg.StateDir, ".doctor-*")
	if err != nil {
		return doctorFail("state_dir_writable", "error", "state_dir is not writable: "+err.Error(), "fix state_dir permissions or config")
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	return doctorOK("state_dir_writable", "state_dir is writable")
}

func (s *Service) doctorHTTPTokenCheck() DoctorCheck {
	if s.cfg.AuthToken == "" {
		return doctorFail("http_auth_token", "warn", "HTTP auth token is not configured", "set auth_token or ANIGATE_AUTH_TOKEN before exposing HTTP beyond loopback")
	}
	return doctorOK("http_auth_token", "HTTP auth token is configured")
}

func (s *Service) doctorGitCheck() DoctorCheck {
	if _, err := exec.LookPath("git"); err != nil {
		return doctorFail("git_available", "error", "git executable not found in PATH", "install git or update PATH")
	}
	return doctorOK("git_available", "git executable is available")
}

func (s *Service) doctorWorkspaceCheck(ws Workspace) DoctorCheck {
	info, err := os.Stat(ws.Path)
	if err != nil {
		return doctorFail("workspace_"+ws.Name, "error", "workspace path is not accessible: "+err.Error(), "fix workspace path")
	}
	if !info.IsDir() {
		return doctorFail("workspace_"+ws.Name, "error", "workspace path is not a directory", "fix workspace path")
	}
	return doctorOK("workspace_"+ws.Name, "workspace path is accessible")
}

func (s *Service) doctorProjectChecks(project Project) []DoctorCheck {
	var checks []DoctorCheck
	rp, err := s.resolveProjectPath(project)
	if err != nil {
		return []DoctorCheck{doctorFail("project_"+project.Name+"_path", "error", err.Error(), "fix project workspace/path config")}
	}
	gitDir := filepath.Join(rp.Abs, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return []DoctorCheck{doctorFail("project_"+project.Name+"_cloned", "error", "project is not cloned", "call project.ensure")}
	}
	checks = append(checks, doctorOK("project_"+project.Name+"_cloned", "project repository is present"))
	remote, err := s.runGitOutput(rp.Abs, "remote", "get-url", "origin")
	if err != nil {
		checks = append(checks, doctorFail("project_"+project.Name+"_remote", "error", err.Error(), "set or fetch origin remote"))
	} else if strings.TrimSpace(remote) != project.RemoteURL {
		checks = append(checks, doctorFail("project_"+project.Name+"_remote", "error", "origin remote does not match allowlist", "call project.ensure or fix config"))
	} else {
		checks = append(checks, doctorOK("project_"+project.Name+"_remote", "origin remote matches allowlist"))
	}
	if _, err := s.runGitOutput(rp.Abs, "rev-parse", "--verify", project.DefaultBranch); err != nil {
		if _, originErr := s.runGitOutput(rp.Abs, "rev-parse", "--verify", "origin/"+project.DefaultBranch); originErr != nil {
			checks = append(checks, doctorFail("project_"+project.Name+"_default_branch", "error", "default branch cannot be resolved", "fetch project or fix default_branch"))
		} else {
			checks = append(checks, doctorOK("project_"+project.Name+"_default_branch", "origin default branch resolves"))
		}
	} else {
		checks = append(checks, doctorOK("project_"+project.Name+"_default_branch", "default branch resolves"))
	}
	if project.Provider == "github_gh" {
		if _, err := exec.LookPath("gh"); err != nil {
			checks = append(checks, doctorFail("project_"+project.Name+"_gh_available", "error", "gh executable not found in PATH", "install GitHub CLI"))
		} else {
			checks = append(checks, doctorOK("project_"+project.Name+"_gh_available", "gh executable is available"))
			if _, err := runHostCommand(rp.Abs, hostCmdOpts{Timeout: gitNetworkTimeout}, "gh", "auth", "status"); err != nil {
				checks = append(checks, doctorFail("project_"+project.Name+"_gh_auth", "error", "gh auth status failed", "run gh auth login"))
			} else {
				checks = append(checks, doctorOK("project_"+project.Name+"_gh_auth", "gh auth is configured"))
			}
		}
	}
	return checks
}

func doctorAgentCheck(agent Agent) DoctorCheck {
	if len(agent.Command) == 0 || strings.TrimSpace(agent.Command[0]) == "" {
		return doctorFail("agent_"+agent.Name, "error", "agent command is empty", "fix agent command config")
	}
	cmd := agent.Command[0]
	if filepath.IsAbs(cmd) {
		if _, err := os.Stat(cmd); err != nil {
			return doctorFail("agent_"+agent.Name, "error", "agent command is not accessible: "+err.Error(), "fix agent command path")
		}
		return doctorOK("agent_"+agent.Name, "agent command path is accessible")
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return doctorFail("agent_"+agent.Name, "error", "agent command not found in PATH", "install command or update config")
	}
	return doctorOK("agent_"+agent.Name, "agent command is available")
}

func checksOK(checks []DoctorCheck) bool {
	for _, check := range checks {
		if !check.OK && check.Severity == "error" {
			return false
		}
	}
	return true
}

func doctorOK(id, message string) DoctorCheck {
	return DoctorCheck{ID: id, OK: true, Severity: "info", Message: message}
}

func doctorFail(id, severity, message, next string) DoctorCheck {
	return DoctorCheck{ID: id, OK: false, Severity: severity, Message: message, Next: next}
}
