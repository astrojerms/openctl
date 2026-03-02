package errors

import (
	"errors"
	"fmt"
)

// Common error types
var (
	ErrPluginNotFound   = errors.New("plugin not found")
	ErrResourceNotFound = errors.New("resource not found")
	ErrInvalidManifest  = errors.New("invalid manifest")
	ErrConfigNotFound   = errors.New("configuration not found")
	ErrAuthentication   = errors.New("authentication failed")
)

// PluginError represents an error from a plugin
type PluginError struct {
	PluginName string
	Code       string
	Message    string
	Details    string
}

func (e *PluginError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s: %s (%s)", e.PluginName, e.Message, e.Details)
	}
	return fmt.Sprintf("%s: %s", e.PluginName, e.Message)
}

// NewPluginError creates a new plugin error
func NewPluginError(pluginName, code, message, details string) *PluginError {
	return &PluginError{
		PluginName: pluginName,
		Code:       code,
		Message:    message,
		Details:    details,
	}
}

// ConfigError represents a configuration error
type ConfigError struct {
	Message string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("configuration error: %s", e.Message)
}

// NewConfigError creates a new config error
func NewConfigError(message string) *ConfigError {
	return &ConfigError{Message: message}
}
