package config

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"unicode"
)

// Profile is a [profiles.NAME] table: an endpoint and its model, laid over
// the top level of config.toml, which stays the profile of a config with
// none. A key the table does not set is the top level's, so an empty table
// is the top level itself. The API key is the exception, lest it go to
// another endpoint: a table setting api_key or api_key_env has both of its
// own, and one naming provider or base_url but no key has none, so the
// provider's variable gives it. So is effort, lest a level go to a provider
// that has none such: a table naming a provider other than the top level's
// ("" being DefaultProvider) but no effort has the provider's default.
// Pointers tell a key that is set from one that is not.
type Profile struct {
	Provider      *string `toml:"provider"`
	BaseURL       *string `toml:"base_url"`
	APIKey        *string `toml:"api_key"`
	APIKeyEnv     *string `toml:"api_key_env"`
	Model         *string `toml:"model"`
	Effort        *string `toml:"effort"`
	MaxTokens     *int64  `toml:"max_tokens"`
	ContextWindow *int    `toml:"context_window"`
}

// LoadProfile is Load with profile name in force, "" being the top level
// alone, whatever the profile key and $AISH_PROFILE select. $AISH_MODEL
// and $AISH_EFFORT are not read either: they are for the profile a shell
// starts with, and a shell switched to another by `aish model` runs with
// that profile's own.
func LoadProfile(name string) (Config, error) { return loadWith(&name, os.Getenv) }

// LoadEnv is Load with $AISH_PROFILE, $AISH_MODEL and $AISH_EFFORT read
// through getenv: the proxy asks with the environment of the shell, where
// they may have been exported or unset since it started. $AISH_CONFIG is
// the process's still, as for LoadProfile: the file must be the one the
// proxy reads the shell's profile from.
func LoadEnv(getenv func(string) string) (Config, error) { return loadWith(nil, getenv) }

// ProfileNames are the profiles of the config, sorted.
func (c Config) ProfileNames() []string { return slices.Sorted(maps.Keys(c.Profiles)) }

// pick lays over c, as decoded from the file, the profile name names, or
// else the one $AISH_PROFILE does, or else the profile key.
func (c Config) pick(name *string, getenv func(string) string) (Config, error) {
	if name != nil {
		return c.withProfile(*name, "")
	}
	if env := getenv("AISH_PROFILE"); env != "" {
		return c.withProfile(env, "$AISH_PROFILE: ")
	}
	return c.withProfile(c.Profile, "profile: ")
}

// withProfile lays profile name over the top level of c; from tells where
// the name came from, for the error.
func (c Config) withProfile(name, from string) (Config, error) {
	if name == Root {
		name = ""
	}
	c.Profile = name
	if name == "" {
		return c, nil
	}
	pr, ok := c.Profiles[name]
	if !ok {
		have := "no [profiles.*] tables"
		if len(c.Profiles) > 0 {
			have = "profiles: " + strings.Join(c.ProfileNames(), ", ")
		}
		return c, fmt.Errorf("%sno profile %q (%s)", from, name, have)
	}
	top := c.Provider
	lay(&c.Provider, pr.Provider)
	lay(&c.BaseURL, pr.BaseURL)
	lay(&c.APIKey, pr.APIKey)
	lay(&c.APIKeyEnv, pr.APIKeyEnv)
	lay(&c.Model, pr.Model)
	lay(&c.Effort, pr.Effort)
	lay(&c.MaxTokens, pr.MaxTokens)
	lay(&c.ContextWindow, pr.ContextWindow)
	// Whether the endpoint changed is told by what the table sets, not by
	// the values: a table naming provider or base_url has an endpoint of
	// its own, and the top level's key is for the top level's.
	switch {
	case pr.APIKey != nil || pr.APIKeyEnv != nil:
		if pr.APIKey == nil {
			c.APIKey = ""
		}
		if pr.APIKeyEnv == nil {
			c.APIKeyEnv = ""
		}
	case pr.Provider != nil || pr.BaseURL != nil:
		c.APIKey, c.APIKeyEnv = "", ""
	}
	// Effort goes by the provider itself: the top level's level is one the
	// same provider has, whatever base_url it is reached at.
	if pr.Provider != nil && pr.Effort == nil && provider(*pr.Provider) != provider(top) {
		c.Effort = ""
	}
	return c, nil
}

func lay[T any](dst, v *T) {
	if v != nil {
		*dst = *v
	}
}

// provider is the provider a config's provider key names.
func provider(name string) string {
	if name == "" {
		return DefaultProvider
	}
	return name
}

// Root is how `aish model` and the status name the top level of
// config.toml, the profile "", and so may the profile key and
// $AISH_PROFILE: a table of that name could not be told from it.
const Root = "root"

// checkProfiles rejects what check would at the top level, and names that
// cannot be given to `aish model` as one word.
func checkProfiles(ps map[string]Profile) error {
	for _, name := range slices.Sorted(maps.Keys(ps)) {
		if name == "" || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) {
			return fmt.Errorf("profiles.%q: a profile is named by one word", name)
		}
		if name == Root {
			return fmt.Errorf("profiles.%s: the name is the top level's (aish model %s)", Root, Root)
		}
		pr := ps[name]
		if pr.MaxTokens != nil && *pr.MaxTokens < 0 {
			return fmt.Errorf("profiles.%s.max_tokens = %d: must not be negative", name, *pr.MaxTokens)
		}
		if pr.ContextWindow != nil && *pr.ContextWindow < 0 {
			return fmt.Errorf("profiles.%s.context_window = %d: must not be negative", name, *pr.ContextWindow)
		}
	}
	return nil
}
