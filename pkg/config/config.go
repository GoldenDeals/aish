package config

// Config is a module's section of the app config.
type Config interface {
	Map() map[string]any
}
