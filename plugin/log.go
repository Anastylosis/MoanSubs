package main

import (
	"fmt"
	"os"
	"strings"
)

// Stash reads plugin logs from stderr, with a control-character envelope
// selecting the level (pkg/plugin/common/log upstream): \x01 (SOH), the
// level byte, then \x02 (STX), then the message. The trailing STX is
// load-bearing — without it Stash does not recognize the level, logs the
// whole line at the manifest's errLog level, and the level byte leaks into
// the message text ("iwrote ..."). Progress is a float in [0,1] on the "p"
// level. Stdout is the RPC channel and must never be written to directly.
const (
	logPrefixInfo     = "\x01i\x02"
	logPrefixWarning  = "\x01w\x02"
	logPrefixError    = "\x01e\x02"
	logPrefixProgress = "\x01p\x02"
)

// logLine emits one prefixed line. Newlines inside the message would make
// Stash treat the continuation as an unprefixed (default-level) log line, so
// they are flattened.
func logLine(prefix, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	msg = strings.ReplaceAll(msg, "\n", " ")
	fmt.Fprintf(os.Stderr, "%s%s\n", prefix, msg)
}

// Level matters more than it looks: Stash drops anything below its
// configured log level before it is ever stored, and an instance left at
// Warning — a common setting — shows nothing this plugin logs at Info. So
// anything a user needs in order to understand why a task did not do what
// they expected belongs at Warning or above, and Info is reserved for the
// running commentary of a task that is working.
func logInfo(format string, args ...any)    { logLine(logPrefixInfo, format, args...) }
func logWarning(format string, args ...any) { logLine(logPrefixWarning, format, args...) }
func logError(format string, args ...any)   { logLine(logPrefixError, format, args...) }

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
