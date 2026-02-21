package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Level represents a log level
type Level int

const (
	LevelQuiet Level = iota
	LevelNormal
	LevelVerbose
	LevelDebug
)

// Logger handles logging output
type Logger struct {
	level  Level
	writer io.Writer
}

var defaultLogger = &Logger{
	level:  LevelNormal,
	writer: os.Stderr,
}

// SetLevel sets the global log level
func SetLevel(level Level) {
	defaultLogger.level = level
}

// SetVerbose enables verbose logging
func SetVerbose(verbose bool) {
	if verbose {
		defaultLogger.level = LevelVerbose
	}
}

// SetDebug enables debug logging
func SetDebug(debug bool) {
	if debug {
		defaultLogger.level = LevelDebug
	}
}

// GetLevel returns the current log level
func GetLevel() Level {
	return defaultLogger.level
}

// IsVerbose returns true if verbose logging is enabled
func IsVerbose() bool {
	return defaultLogger.level >= LevelVerbose
}

// IsDebug returns true if debug logging is enabled
func IsDebug() bool {
	return defaultLogger.level >= LevelDebug
}

// Info prints an info message (always shown unless quiet)
func Info(format string, args ...any) {
	if defaultLogger.level >= LevelNormal {
		fmt.Fprintf(defaultLogger.writer, format+"\n", args...)
	}
}

// Verbose prints a verbose message
func Verbose(format string, args ...any) {
	if defaultLogger.level >= LevelVerbose {
		fmt.Fprintf(defaultLogger.writer, "[verbose] "+format+"\n", args...)
	}
}

// Debug prints a debug message
func Debug(format string, args ...any) {
	if defaultLogger.level >= LevelDebug {
		fmt.Fprintf(defaultLogger.writer, "[debug] "+format+"\n", args...)
	}
}

// DebugJSON prints a JSON value with pretty formatting
func DebugJSON(label string, v any) {
	if defaultLogger.level >= LevelDebug {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			Debug("%s: (failed to marshal: %v)", label, err)
			return
		}
		// Indent each line for readability
		lines := strings.Split(string(data), "\n")
		Debug("%s:", label)
		for _, line := range lines {
			fmt.Fprintf(defaultLogger.writer, "  %s\n", line)
		}
	}
}

// VerboseJSON prints a JSON value (compact) in verbose mode
func VerboseJSON(label string, v any) {
	if defaultLogger.level >= LevelVerbose {
		data, err := json.Marshal(v)
		if err != nil {
			Verbose("%s: (failed to marshal: %v)", label, err)
			return
		}
		// Truncate if too long
		s := string(data)
		if len(s) > 500 {
			s = s[:500] + "..."
		}
		Verbose("%s: %s", label, s)
	}
}
