package anigate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultMaxReadBytes       int64 = 64 * 1024
	defaultMaxSearchFileBytes int64 = 256 * 1024
	defaultMaxSearchResults         = 50
	defaultMaxJobLogBytes     int64 = 1024 * 1024
	defaultMaxArtifactBytes   int64 = 4 * 1024 * 1024
)

type Config struct {
	StateDir           string      `json:"state_dir"`
	AuthToken          string      `json:"auth_token"`
	MaxReadBytes       int64       `json:"max_read_bytes"`
	MaxSearchFileBytes int64       `json:"max_search_file_bytes"`
	MaxSearchResults   int         `json:"max_search_results"`
	MaxJobLogBytes     int64       `json:"max_job_log_bytes"`
	MaxArtifactBytes   int64       `json:"max_artifact_bytes"`
	EnvAllowlist       []string    `json:"env_allowlist"`
	IsolatedHome       bool        `json:"isolated_home"`
	Workspaces         []Workspace `json:"workspaces"`
	Projects           []Project   `json:"projects"`
	Presets            []Preset    `json:"presets"`
	Agents             []Agent     `json:"agents"`
}

type Workspace struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
	Profile  string `json:"profile"`
}

type Preset struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Workspace   string            `json:"workspace"`
	Cwd         string            `json:"cwd"`
	Command     []string          `json:"command"`
	Args        []PresetArg       `json:"args"`
	TimeoutSec  int               `json:"timeout_sec"`
	Async       bool              `json:"async"`
	Env         map[string]string `json:"env"`
}

type PresetArg struct {
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	Required         bool     `json:"required"`
	Default          any      `json:"default"`
	Enum             []string `json:"enum"`
	Pattern          string   `json:"pattern"`
	Min              *int64   `json:"min"`
	Max              *int64   `json:"max"`
	MaxLen           int      `json:"max_len"`
	MaxItems         int      `json:"max_items"`
	AllowLeadingDash bool     `json:"allow_leading_dash"`
}

type Agent struct {
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	Provider           string            `json:"provider"`
	Workspace          string            `json:"workspace"`
	Cwd                string            `json:"cwd"`
	Command            []string          `json:"command"`
	PromptStdin        bool              `json:"prompt_stdin"`
	TimeoutSec         int               `json:"timeout_sec"`
	MaxHistoryMessages int               `json:"max_history_messages"`
	Env                map[string]string `json:"env"`
}

type Project struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Workspace     string `json:"workspace"`
	Path          string `json:"path"`
	RemoteURL     string `json:"remote_url"`
	DefaultBranch string `json:"default_branch"`
	Provider      string `json:"provider"`
	AllowPush     bool   `json:"allow_push"`
	AllowPR       bool   `json:"allow_pr"`
	DefaultAgent  string `json:"default_agent"`
}

func LoadConfig(path string) (Config, error) {
	if path == "" {
		if env := os.Getenv("ANIGATE_CONFIG"); env != "" {
			path = env
		}
	}
	var cfg Config
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return Config{}, err
		}
		cfg = DefaultConfig(cwd)
	} else {
		b, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
		if cfg.StateDir != "" && !filepath.IsAbs(cfg.StateDir) {
			base := filepath.Dir(path)
			cfg.StateDir = filepath.Join(base, cfg.StateDir)
		}
		for i := range cfg.Workspaces {
			if cfg.Workspaces[i].Path != "" && !filepath.IsAbs(cfg.Workspaces[i].Path) {
				cfg.Workspaces[i].Path = filepath.Join(filepath.Dir(path), cfg.Workspaces[i].Path)
			}
		}
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func DefaultConfig(root string) Config {
	return Config{
		StateDir:           filepath.Join(root, ".anigate", "state"),
		MaxReadBytes:       defaultMaxReadBytes,
		MaxSearchFileBytes: defaultMaxSearchFileBytes,
		MaxSearchResults:   defaultMaxSearchResults,
		MaxJobLogBytes:     defaultMaxJobLogBytes,
		MaxArtifactBytes:   defaultMaxArtifactBytes,
		IsolatedHome:       true,
		Workspaces: []Workspace{{
			Name:     "default",
			Path:     root,
			ReadOnly: true,
			Profile:  "operator",
		}},
		Presets: []Preset{{
			Name:        "sys_uptime",
			Description: "Show system uptime",
			Workspace:   "default",
			Cwd:         ".",
			Command:     []string{"uptime"},
			TimeoutSec:  10,
		}},
	}
}

func (c *Config) applyDefaults() {
	if c.AuthToken == "" {
		c.AuthToken = os.Getenv("ANIGATE_AUTH_TOKEN")
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Join(".", ".anigate", "state")
	}
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = defaultMaxReadBytes
	}
	if c.MaxSearchFileBytes <= 0 {
		c.MaxSearchFileBytes = defaultMaxSearchFileBytes
	}
	if c.MaxSearchResults <= 0 {
		c.MaxSearchResults = defaultMaxSearchResults
	}
	if c.MaxJobLogBytes <= 0 {
		c.MaxJobLogBytes = defaultMaxJobLogBytes
	}
	if c.MaxArtifactBytes <= 0 {
		c.MaxArtifactBytes = defaultMaxArtifactBytes
	}
	for i := range c.Presets {
		if c.Presets[i].Cwd == "" {
			c.Presets[i].Cwd = "."
		}
		if c.Presets[i].TimeoutSec <= 0 {
			c.Presets[i].TimeoutSec = 30
		}
	}
	for i := range c.Workspaces {
		if c.Workspaces[i].Profile == "" {
			c.Workspaces[i].Profile = "reader"
		}
	}
	for i := range c.Agents {
		if c.Agents[i].Cwd == "" {
			c.Agents[i].Cwd = "."
		}
		if c.Agents[i].Provider == "" {
			c.Agents[i].Provider = c.Agents[i].Name
		}
		if c.Agents[i].TimeoutSec <= 0 {
			c.Agents[i].TimeoutSec = 600
		}
		if c.Agents[i].MaxHistoryMessages <= 0 {
			c.Agents[i].MaxHistoryMessages = 20
		}
	}
	for i := range c.Projects {
		if c.Projects[i].DefaultBranch == "" {
			c.Projects[i].DefaultBranch = "main"
		}
		if c.Projects[i].Provider == "" {
			c.Projects[i].Provider = "generic_git"
		}
	}
}

func (c Config) Validate() error {
	if len(c.Workspaces) == 0 {
		return errors.New("config must declare at least one workspace")
	}
	seenWorkspaces := map[string]bool{}
	for _, ws := range c.Workspaces {
		if !validName(ws.Name) {
			return fmt.Errorf("invalid workspace name %q", ws.Name)
		}
		if strings.TrimSpace(ws.Path) == "" {
			return fmt.Errorf("workspace %q has empty path", ws.Name)
		}
		if seenWorkspaces[ws.Name] {
			return fmt.Errorf("duplicate workspace %q", ws.Name)
		}
		if !validProfile(ws.Profile) {
			return fmt.Errorf("workspace %q has invalid profile %q", ws.Name, ws.Profile)
		}
		seenWorkspaces[ws.Name] = true
	}
	seenPresets := map[string]bool{}
	envAllowed := c.envAllowlistSet()
	for _, p := range c.Presets {
		if !validName(p.Name) {
			return fmt.Errorf("invalid preset name %q", p.Name)
		}
		if seenPresets[p.Name] {
			return fmt.Errorf("duplicate preset %q", p.Name)
		}
		seenPresets[p.Name] = true
		if p.Workspace == "" {
			return fmt.Errorf("preset %q missing workspace", p.Name)
		}
		if !seenWorkspaces[p.Workspace] {
			return fmt.Errorf("preset %q references unknown workspace %q", p.Name, p.Workspace)
		}
		if len(p.Command) == 0 || strings.TrimSpace(p.Command[0]) == "" {
			return fmt.Errorf("preset %q has empty command", p.Name)
		}
		for k := range p.Env {
			if !validEnvName(k) {
				return fmt.Errorf("preset %q has invalid env name %q", p.Name, k)
			}
			if len(envAllowed) > 0 && !envAllowed[k] {
				return fmt.Errorf("preset %q env %q is not in env_allowlist", p.Name, k)
			}
		}
		seenArgs := map[string]bool{}
		for _, arg := range p.Args {
			if !validName(arg.Name) {
				return fmt.Errorf("preset %q has invalid arg name %q", p.Name, arg.Name)
			}
			if seenArgs[arg.Name] {
				return fmt.Errorf("preset %q has duplicate arg %q", p.Name, arg.Name)
			}
			seenArgs[arg.Name] = true
			if !validArgType(arg.Type) {
				return fmt.Errorf("preset %q arg %q has invalid type %q", p.Name, arg.Name, arg.Type)
			}
		}
		if err := validateCommandPlaceholders("preset "+p.Name, p.Command, seenArgs); err != nil {
			return err
		}
	}
	seenAgents := map[string]bool{}
	for _, a := range c.Agents {
		if !validName(a.Name) {
			return fmt.Errorf("invalid agent name %q", a.Name)
		}
		if seenAgents[a.Name] {
			return fmt.Errorf("duplicate agent %q", a.Name)
		}
		seenAgents[a.Name] = true
		if a.Workspace == "" {
			return fmt.Errorf("agent %q missing workspace", a.Name)
		}
		if !seenWorkspaces[a.Workspace] {
			return fmt.Errorf("agent %q references unknown workspace %q", a.Name, a.Workspace)
		}
		if len(a.Command) == 0 || strings.TrimSpace(a.Command[0]) == "" {
			return fmt.Errorf("agent %q has empty command", a.Name)
		}
		for k := range a.Env {
			if !validEnvName(k) {
				return fmt.Errorf("agent %q has invalid env name %q", a.Name, k)
			}
			if len(envAllowed) > 0 && !envAllowed[k] {
				return fmt.Errorf("agent %q env %q is not in env_allowlist", a.Name, k)
			}
		}
	}
	seenProjects := map[string]bool{}
	for _, p := range c.Projects {
		if !validName(p.Name) {
			return fmt.Errorf("invalid project name %q", p.Name)
		}
		if seenProjects[p.Name] {
			return fmt.Errorf("duplicate project %q", p.Name)
		}
		seenProjects[p.Name] = true
		if p.Workspace == "" {
			return fmt.Errorf("project %q missing workspace", p.Name)
		}
		if !seenWorkspaces[p.Workspace] {
			return fmt.Errorf("project %q references unknown workspace %q", p.Name, p.Workspace)
		}
		if strings.TrimSpace(p.Path) == "" {
			return fmt.Errorf("project %q has empty path", p.Name)
		}
		if strings.TrimSpace(p.RemoteURL) == "" {
			return fmt.Errorf("project %q has empty remote_url", p.Name)
		}
		if p.DefaultAgent != "" && !seenAgents[p.DefaultAgent] {
			return fmt.Errorf("project %q references unknown default_agent %q", p.Name, p.DefaultAgent)
		}
		switch p.Provider {
		case "", "generic_git", "github_gh":
		default:
			return fmt.Errorf("project %q has invalid provider %q", p.Name, p.Provider)
		}
	}
	return nil
}

func (c Config) envAllowlistSet() map[string]bool {
	if len(c.EnvAllowlist) == 0 {
		return nil
	}
	out := make(map[string]bool, len(c.EnvAllowlist))
	for _, name := range c.EnvAllowlist {
		if validEnvName(name) {
			out[name] = true
		}
	}
	return out
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r >= 'A' && r <= 'Z' || r == '_' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func validProfile(s string) bool {
	switch s {
	case "reader", "operator", "agent":
		return true
	default:
		return false
	}
}

func validArgType(s string) bool {
	switch s {
	case "", "string", "int", "bool", "string_array":
		return true
	default:
		return false
	}
}
