// Package config loads aish settings from ~/.config/aish/config.toml.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// Provider selects the wire protocol: "anthropic" or "openai".
	Provider string `toml:"provider"`
	// BaseURL of the API. Defaults to a local cliproxyapi instance.
	BaseURL string `toml:"base_url"`
	// APIKey is used as-is if set; otherwise it is read from APIKeyEnv.
	APIKey    string `toml:"api_key"`
	APIKeyEnv string `toml:"api_key_env"`
	Model     string `toml:"model"`
	// Effort is how hard the model thinks: "low" … "max" (and "none",
	// "minimal" for OpenAI). Empty leaves it to the model.
	Effort string `toml:"effort"`
	// MaxTokens bounds a reply; 0 picks it by the effort (see ReplyTokens).
	MaxTokens int64 `toml:"max_tokens"`

	// MaxSteps bounds the number of LLM round-trips per user request.
	MaxSteps int `toml:"max_steps"`
	// MaxOutputBytes is how much of each command's output the LLM sees.
	MaxOutputBytes int `toml:"max_output_bytes"`
	// FoldLines is how many lines of an agent command's output are shown
	// before the rest is folded (Ctrl+O expands). 0 shows only the command
	// and a status line; negative disables folding.
	FoldLines int `toml:"fold_lines"`
	// Markdown renders the assistant's replies as markdown.
	Markdown bool `toml:"markdown"`
	// CodeStyle is the chroma style for code blocks in replies.
	CodeStyle string `toml:"code_style"`
	// ContextWindow is the model's context size in tokens; 0 asks the API.
	ContextWindow int `toml:"context_window"`
	// PromptStatus shows the context size and the model at the right of the
	// prompt.
	PromptStatus bool `toml:"prompt_status"`

	PolicyDir   string `toml:"policy_dir"`
	ToolsDir    string `toml:"tools_dir"`
	SessionsDir string `toml:"sessions_dir"`
	// MCPConfig lists MCP servers (YAML).
	MCPConfig string `toml:"mcp_config"`

	// SystemPrompt is appended to the built-in system prompt.
	SystemPrompt string `toml:"system_prompt"`
}

func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "aish")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "aish")
}

// CacheDir holds what can be rebuilt, such as MCP tool lists.
func CacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "aish")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "aish")
}

func dataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "aish")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "aish")
}

func Default() Config {
	return Config{
		Provider:       "anthropic",
		BaseURL:        "http://127.0.0.1:8317",
		APIKeyEnv:      "AISH_API_KEY",
		Model:          "claude-opus-5",
		MaxSteps:       50,
		MaxOutputBytes: 16000,
		FoldLines:      0,
		Markdown:       true,
		CodeStyle:      "monokai",
		PromptStatus:   true,
		PolicyDir:      filepath.Join(Dir(), "policy"),
		ToolsDir:       filepath.Join(Dir(), "tools"),
		SessionsDir:    filepath.Join(dataDir(), "sessions"),
		MCPConfig:      filepath.Join(Dir(), "mcp.yaml"),
	}
}

// Load reads the config file (if any) over the defaults. The path can be
// overridden with $AISH_CONFIG.
func Load() (Config, error) {
	cfg := Default()
	path := os.Getenv("AISH_CONFIG")
	if path == "" {
		path = filepath.Join(Dir(), "config.toml")
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}
	if m := os.Getenv("AISH_MODEL"); m != "" {
		cfg.Model = m
	}
	if e := os.Getenv("AISH_EFFORT"); e != "" {
		cfg.Effort = e
	}
	cfg.PolicyDir = expand(cfg.PolicyDir)
	cfg.ToolsDir = expand(cfg.ToolsDir)
	cfg.SessionsDir = expand(cfg.SessionsDir)
	cfg.MCPConfig = expand(cfg.MCPConfig)
	return cfg, nil
}

// Key returns the API key, falling back to the provider's conventional env var.
func (c Config) Key() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	if k := os.Getenv(c.APIKeyEnv); k != "" {
		return k
	}
	if c.Provider == "openai" {
		return os.Getenv("OPENAI_API_KEY")
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// ReplyTokens is MaxTokens, or when it is 0 enough for the effort: at xhigh
// and max the model thinks long, and a reply cut short in thinking is lost.
func (c Config) ReplyTokens() int64 {
	switch {
	case c.MaxTokens > 0:
		return c.MaxTokens
	case c.Effort == "xhigh" || c.Effort == "max":
		return 64000
	}
	return 32000
}

func expand(p string) string {
	if len(p) > 1 && p[0] == '~' && p[1] == '/' {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
