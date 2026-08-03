package main

import (
	"fmt"
	"os"
	"strings"
)

// Stash reads plugin logs from stderr, with a control-character prefix
// selecting the level (pkg/plugin/common/log upstream): \x01 followed by a
// level byte. Progress is a float in [0,1] on the "p" level. Stdout is the
// RPC channel and must never be written to directly.
const (
	logPrefixInfo     = "\x01i"
	logPrefixError    = "\x01e"
	logPrefixProgress = "\x01p"
)

// logLine emits one prefixed line. Newlines inside the message would make
// Stash treat the continuation as an unprefixed (default-level) log line, so
// they are flattened.
func logLine(prefix, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	msg = strings.ReplaceAll(msg, "\n", " ")
	fmt.Fprintf(os.Stderr, "%s%s\n", prefix, msg)
}

func logInfo(format string, args ...any)  { logLine(logPrefixInfo, format, args...) }
func logError(format string, args ...any) { logLine(logPrefixError, format, args...) }

// logProgress reports task progress to Stash's job bar; v is clamped to [0,1].
func logProgress(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1 {
		v = 1
	}
	fmt.Fprintf(os.Stderr, "%s%f\n", logPrefixProgress, v)
}
