// Package config loads aish settings from ~/.config/aish/config.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultProvider is the provider of a config naming none. It lives here,
// not in llm, so that a profile can tell whether it names the top level's.
const DefaultProvider = "anthropic"

type Config struct {
	// Provider names the API, one that package llm registers. Empty:
	// DefaultProvider.
	Provider string `toml:"provider"`
	// BaseURL of the API. Empty: the provider's own.
	BaseURL string `toml:"base_url"`
	// APIKey is used as-is if set; otherwise it is read from APIKeyEnv.
	APIKey    string `toml:"api_key"`
	APIKeyEnv string `toml:"api_key_env"`
	// HTTPProxy, HTTPSProxy and AllProxy are the proxies of the requests
	// to the model, as curl takes them: AllProxy for a scheme without one
	// of its own. NoProxy names the hosts reached directly. Any of them
	// set, the environment (http_proxy and the rest) is not read.
	HTTPProxy  string `toml:"http_proxy"`
	HTTPSProxy string `toml:"https_proxy"`
	AllProxy   string `toml:"all_proxy"`
	NoProxy    string `toml:"no_proxy"`
	Model      string `toml:"model"`
	// Effort is how hard the model thinks, one of the provider's levels.
	// Empty leaves it to the model.
	Effort string `toml:"effort"`
	// MaxTokens bounds a reply; 0 leaves it to the provider, by the effort.
	MaxTokens int64 `toml:"max_tokens"`
	// Profile names the one of Profiles laid over the keys above and
	// context_window; "" is none. After Load it is the profile in force,
	// $AISH_PROFILE's if set.
	Profile  string             `toml:"profile"`
	Profiles map[string]Profile `toml:"profiles"`

	// MaxSteps bounds the number of LLM round-trips per user request.
	MaxSteps int `toml:"max_steps"`
	// MaxOutputBytes is how much of each command's output the LLM sees.
	MaxOutputBytes int `toml:"max_output_bytes"`
	// Mask lists regexps for secrets to hide in what the LLM sees (command
	// output, files, tool results); MaskDefaults adds the built-in ones.
	// A capturing group hides only its value.
	Mask         []string `toml:"mask"`
	MaskDefaults bool     `toml:"mask_defaults"`
	// FoldLines is how many lines of an agent command's output are shown
	// before the rest is folded (Ctrl+O expands). 0 shows only the command
	// and a status line; negative disables folding.
	FoldLines int `toml:"fold_lines"`
	// HideWork sums the agent's tool calls up in one line redrawn in place
	// ("Read 3 files, ran 2 commands"); each is kept for Ctrl+O.
	HideWork bool `toml:"hide_work"`
	// Markdown renders the assistant's replies as markdown.
	Markdown bool `toml:"markdown"`
	// CodeStyle is the chroma style for code blocks in replies.
	CodeStyle string `toml:"code_style"`
	// ContextWindow is the model's context size in tokens; 0 asks the API.
	ContextWindow int `toml:"context_window"`
	// CompactAt is the share of the window past which the agent sums the
	// session up before its next turn, as `aish compact` does; 0 never.
	CompactAt float64 `toml:"compact_at"`
	// AskTimeout is how long the form of ask_user waits for the user's
	// answers: past it the form is closed and the model is told nobody
	// answered. A Go duration (5m, 1h) or days (1d); "0" waits for ever.
	AskTimeout string `toml:"ask_timeout"`
	// CacheTTL is how long the provider keeps a session cached after its
	// last turn: past it a request in a session of ColdWarnTokens or more
	// is told that it pays for all of it again. A Go duration (5m, 1h) or
	// days (1d); "0" does not check.
	CacheTTL string `toml:"cache_ttl"`
	// ColdWarnTokens is the size of a session, in tokens, from which a
	// cache that expired or is not read is worth a word; 0 never.
	ColdWarnTokens int `toml:"cold_warn_tokens"`
	// PromptStatus shows the context size and the model at the right of the
	// prompt.
	PromptStatus bool `toml:"prompt_status"`

	// JournalIgnore are shell patterns, as HISTIGNORE takes them, for the
	// commands whose output stays out of the journal: a line with a simple
	// command matching one (`sudo env` too) is recorded without its output.
	JournalIgnore []string `toml:"journal_ignore"`
	// StateIgnore are shell patterns for the variables that stay out of a
	// session's shell state.
	StateIgnore []string `toml:"state_ignore"`

	PolicyDir   string `toml:"policy_dir"`
	Policy      Policy `toml:"policy"`
	HooksDir    string `toml:"hooks_dir"`
	HooksFail   string `toml:"hooks_fail"` // "allow" or "deny" a call whose pre-tool hook gave no answer; "" is allow
	ToolsDir    string `toml:"tools_dir"`
	SessionsDir string `toml:"sessions_dir"`
	// SessionsTTL is how long a saved session lives unmodified before aish
	// removes it at start: days (30d) or hours (720h); "0" keeps them all.
	SessionsTTL string `toml:"sessions_ttl"`
	// MCPConfig lists MCP servers (YAML).
	MCPConfig string `toml:"mcp_config"`
	// Shell is the shell to run, a name or a path: one whose file is named
	// zsh (zsh, /usr/bin/zsh, zsh-5.9) is a zsh, fish is an error, any
	// other a bash. Empty: $SHELL if it is a bash, else the first bash in
	// PATH; a zsh only by name.
	Shell string `toml:"shell"`
	// Route decides which lines typed at the prompt are requests.
	Route Route `toml:"route"`

	// SystemPrompt is appended to the built-in system prompt.
	SystemPrompt string `toml:"system_prompt"`

	// Untrusted names the keys of the project file that Project left out,
	// those that run code from the repository: the file is not trusted.
	// Not a key of any file.
	Untrusted []string `toml:"-"`
}

// Policy is the [policy] table, the simple rules checked next to the
// Cedar policies of PolicyDir: patterns, where * takes spaces and slashes
// too, for the commands to deny or to ask about, and what to do with a
// file tool writing outside the home directory.
type Policy struct {
	Deny []string `toml:"deny"`
	Ask  []string `toml:"ask"`
	// WriteOutsideHome is "allow", "ask" or "deny"; empty is allow.
	WriteOutsideHome string `toml:"write_outside_home"`
	// Hints, by a pattern of Deny or Ask as written there, and
	// WriteOutsideHomeHint are texts for the model, in the system prompt
	// while the rule is: what it forbids and what to do instead.
	Hints                map[string]string `toml:"hints"`
	WriteOutsideHomeHint string            `toml:"write_outside_home_hint"`
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
		APIKeyEnv:      "AISH_API_KEY",
		Model:          "claude-opus-5",
		MaxSteps:       50,
		MaxOutputBytes: 16000,
		MaskDefaults:   true,
		FoldLines:      0,
		Markdown:       true,
		CodeStyle:      "monokai",
		PromptStatus:   true,
		CompactAt:      0.8,
		AskTimeout:     "5m",
		CacheTTL:       "5m",
		ColdWarnTokens: 50000,
		JournalIgnore:  []string{"*secret*", "env", "printenv", "cat *credentials*", "history"},
		StateIgnore:    []string{"*TOKEN*", "*SECRET*", "*KEY*", "*PASSWORD*", "AWS_*"},
		PolicyDir:      filepath.Join(Dir(), "policy"),
		HooksDir:       filepath.Join(Dir(), "hooks"),
		HooksFail:      "allow",
		ToolsDir:       filepath.Join(Dir(), "tools"),
		SessionsDir:    filepath.Join(dataDir(), "sessions"),
		SessionsTTL:    "0",
		MCPConfig:      filepath.Join(Dir(), "mcp.yaml"),
		Route:          Route{Capital: true, NotFound: true, Suffix: "?", MinWords: 2},
	}
}

// Load reads the config file (if any) over the defaults, with the profile
// $AISH_PROFILE or the profile key selects laid over its top level. The
// path can be overridden with $AISH_CONFIG. Unknown keys, inside profiles
// too, negative limits and an unknown profile are errors: otherwise a typo
// leaves the default in force without a word.
func Load() (Config, error) { return LoadEnv(os.Getenv) }

// loadWith is LoadEnv with the profile named by profile, if not nil.
func loadWith(profile *string, getenv func(string) string) (Config, error) {
	path := configFile()
	data, err := os.ReadFile(path)
	return parse(path, data, err, profile, getenv)
}

// configFile is config.toml: $AISH_CONFIG, or config.toml in Dir.
func configFile() string {
	if path := os.Getenv("AISH_CONFIG"); path != "" {
		return path
	}
	return filepath.Join(Dir(), "config.toml")
}

// parse is loadWith of data, what reading path gave, with readErr: a file
// that is not there leaves the defaults.
func parse(path string, data []byte, readErr error, profile *string, getenv func(string) string) (Config, error) {
	cfg := Default()
	err := readErr
	var md toml.MetaData
	if err == nil {
		md, err = toml.Decode(string(data), &cfg)
	}
	if err == nil {
		err = unknown(md)
	}
	if err == nil {
		err = cfg.check()
	}
	if err == nil {
		err = checkProfiles(cfg.Profiles)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if cfg, err = cfg.pick(profile, getenv); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if m := getenv("AISH_MODEL"); m != "" && profile == nil {
		cfg.Model = m
	}
	if e := getenv("AISH_EFFORT"); e != "" && profile == nil {
		cfg.Effort = e
	}
	cfg.PolicyDir = expand(cfg.PolicyDir)
	cfg.HooksDir = expand(cfg.HooksDir)
	cfg.ToolsDir = expand(cfg.ToolsDir)
	cfg.SessionsDir = expand(cfg.SessionsDir)
	cfg.MCPConfig = expand(cfg.MCPConfig)
	cfg.Shell = expand(cfg.Shell)
	return cfg, nil
}

// unknown names the keys no field took. A table is named once, not with
// every key inside it.
func unknown(md toml.MetaData) error {
	var keys []toml.Key
	for _, k := range md.Undecoded() {
		inside := func(t toml.Key) bool { return len(t) < len(k) && slices.Equal(t, k[:len(t)]) }
		if !slices.ContainsFunc(keys, inside) {
			keys = append(keys, k)
		}
	}
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = strconv.Quote(k.String())
	}
	switch len(names) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("unknown key %s", names[0])
	}
	return fmt.Errorf("unknown keys %s", strings.Join(names, ", "))
}

// check rejects negative limits, which the code would quietly take for 0,
// mask patterns that do not compile and ignore patterns that are malformed,
// which would quietly match nothing. fold_lines is not here: negative
// there means "do not fold".
func (c Config) check() error {
	for _, p := range c.Mask {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("mask %q: %w", p, err)
		}
	}
	if c.CompactAt < 0 || c.CompactAt >= 1 {
		return fmt.Errorf("compact_at = %v: a share of the window, at least 0 (off) and less than 1", c.CompactAt)
	}
	for _, f := range []struct {
		key string
		n   int64
	}{
		{"max_tokens", c.MaxTokens},
		{"max_steps", int64(c.MaxSteps)},
		{"max_output_bytes", int64(c.MaxOutputBytes)},
		{"context_window", int64(c.ContextWindow)},
		{"cold_warn_tokens", int64(c.ColdWarnTokens)},
		{"route.min_words", int64(c.Route.MinWords)},
	} {
		if f.n < 0 {
			return fmt.Errorf("%s = %d: must not be negative", f.key, f.n)
		}
	}
	if err := checkProxies("", &c.HTTPProxy, &c.HTTPSProxy, &c.AllProxy); err != nil {
		return err
	}
	if _, err := ParseAge(c.SessionsTTL); err != nil {
		return fmt.Errorf("sessions_ttl = %q: %w", c.SessionsTTL, err)
	}
	if _, err := ParseAge(c.AskTimeout); err != nil {
		return fmt.Errorf("ask_timeout = %q: %w", c.AskTimeout, err)
	}
	if _, err := ParseAge(c.CacheTTL); err != nil {
		return fmt.Errorf("cache_ttl = %q: %w", c.CacheTTL, err)
	}
	switch c.Policy.WriteOutsideHome {
	case "", "allow", "ask", "deny":
	default:
		return fmt.Errorf("policy.write_outside_home = %q: want \"allow\", \"ask\" or \"deny\"", c.Policy.WriteOutsideHome)
	}
	if err := c.Policy.checkHints(); err != nil {
		return err
	}
	switch c.HooksFail {
	case "", "allow", "deny":
	default:
		return fmt.Errorf("hooks_fail = %q: want \"allow\" or \"deny\"", c.HooksFail)
	}
	// The shell reads it as a line of $AISH_RUN/route.
	if strings.ContainsAny(c.Route.Suffix, "\r\n") {
		return fmt.Errorf("route.suffix %q: must be one line", c.Route.Suffix)
	}
	for _, l := range []struct {
		key      string
		patterns []string
	}{
		{"journal_ignore", c.JournalIgnore},
		{"state_ignore", c.StateIgnore},
	} {
		for _, p := range l.patterns {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("%s: pattern %q: %w", l.key, p, err)
			}
		}
	}
	return nil
}

// KeyFrom returns the API key: api_key as is, or else the variable
// api_key_env names, or else fallback, the provider's own variable (llm.Key
// knows it). The variables are read through getenv: the shell's, which the
// proxy's own stops matching once the user exports the key there.
func (c Config) KeyFrom(getenv func(string) string, fallback string) string {
	if c.APIKey != "" {
		return c.APIKey
	}
	if k := getenv(c.APIKeyEnv); k != "" {
		return k
	}
	return getenv(fallback)
}

func expand(p string) string {
	if len(p) > 1 && p[0] == '~' && p[1] == '/' {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
