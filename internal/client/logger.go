// Package client wraps go.mau.fi/whatsmeow and persists messages to the local
// message cache in internal/store. It deliberately writes all logs to stderr
// so that stdout remains clean for MCP's stdio transport.
package client

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	waLog "go.mau.fi/whatsmeow/util/log"
)

var (
	logColors = map[string]string{
		"INFO":  "\033[36m",
		"WARN":  "\033[33m",
		"ERROR": "\033[31m",
	}
	logLevelToInt = map[string]int{
		"":      -1,
		"DEBUG": 0,
		"INFO":  1,
		"WARN":  2,
		"ERROR": 3,
	}
)

// stderrLogger is a copy of whatsmeow's stdout logger that writes to an
// io.Writer (typically os.Stderr) instead of os.Stdout. Writing to stdout
// would corrupt MCP's JSON-RPC stream.
type stderrLogger struct {
	out   io.Writer
	mod   string
	color bool
	min   int
}

// NewStderrLogger returns a waLog.Logger that writes to os.Stderr. The API
// mirrors waLog.Stdout so callers can substitute it freely.
func NewStderrLogger(module string, level string, colorful bool) waLog.Logger {
	return &stderrLogger{
		out:   os.Stderr,
		mod:   module,
		color: colorful,
		min:   logLevelToInt[strings.ToUpper(level)],
	}
}

func (s *stderrLogger) outputf(level, msg string, args ...interface{}) {
	if logLevelToInt[level] < s.min {
		return
	}
	var colorStart, colorReset string
	if s.color {
		colorStart = logColors[level]
		if colorStart != "" {
			colorReset = "\033[0m"
		}
	}
	fmt.Fprintf(s.out, "%s%s [%s %s] %s%s\n",
		time.Now().Format("15:04:05.000"),
		colorStart, s.mod, level, fmt.Sprintf(msg, args...), colorReset,
	)
}

func (s *stderrLogger) Errorf(msg string, args ...interface{}) { s.outputf("ERROR", msg, args...) }
func (s *stderrLogger) Warnf(msg string, args ...interface{})  { s.outputf("WARN", msg, args...) }
func (s *stderrLogger) Infof(msg string, args ...interface{})  { s.outputf("INFO", msg, args...) }
func (s *stderrLogger) Debugf(msg string, args ...interface{}) { s.outputf("DEBUG", msg, args...) }

func (s *stderrLogger) Sub(mod string) waLog.Logger {
	return &stderrLogger{
		out:   s.out,
		mod:   fmt.Sprintf("%s/%s", s.mod, mod),
		color: s.color,
		min:   s.min,
	}
}
