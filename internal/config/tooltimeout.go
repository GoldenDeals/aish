package config

import "time"

// ToolLimit is tool_timeout as a duration: how long a tool call runs
// when the model gives it no timeout of its own; 0 is no limit.
func (c Config) ToolLimit() time.Duration {
	d, _ := ParseAge(c.ToolTimeout) // check and checkProfiles have made sure it parses
	return d
}
