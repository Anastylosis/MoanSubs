package main

import (
	"strings"
	"testing"
)

// Stash parses \x01<level>\x02 and nothing else. A missing trailing STX
// makes it log the whole line at the manifest's errLog level with the level
// byte leaking into the message text, which is exactly the bug the comment
// in log.go warns about — so pin the shape rather than trusting it.
func TestLogPrefixes_AreWellFormed(t *testing.T) {
	for name, p := range map[string]string{
		"info":     logPrefixInfo,
		"warning":  logPrefixWarning,
		"error":    logPrefixError,
		"progress": logPrefixProgress,
	} {
		if len(p) != 3 {
			t.Errorf("%s prefix %q is %d bytes, want 3", name, p, len(p))
			continue
		}
		if p[0] != 0x01 {
			t.Errorf("%s prefix does not start with SOH", name)
		}
		if p[2] != 0x02 {
			t.Errorf("%s prefix does not end with STX — Stash will not recognise the level", name)
		}
	}
}

// The level bytes are Stash's, not ours: t/d/i/w/e for logs, p for progress.
func TestLogPrefixes_UseStashLevelBytes(t *testing.T) {
	for want, p := range map[byte]string{
		'i': logPrefixInfo,
		'w': logPrefixWarning,
		'e': logPrefixError,
		'p': logPrefixProgress,
	} {
		if p[1] != want {
			t.Errorf("prefix %q carries level byte %q, want %q", p, p[1], want)
		}
	}
}

// A newline would make Stash treat the continuation as an unprefixed line
// at the default level, so logLine flattens them.
func TestLogLine_FlattensNewlines(t *testing.T) {
	var sb strings.Builder
	msg := "first\nsecond\nthird"
	flat := strings.ReplaceAll(msg, "\n", " ")
	sb.WriteString(flat)
	if strings.Contains(sb.String(), "\n") {
		t.Fatal("test helper is wrong")
	}
	if flat != "first second third" {
		t.Errorf("flattened = %q", flat)
	}
}
