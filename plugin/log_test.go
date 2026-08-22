package main

import (
	"bytes"
	"io"
	"os"
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

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// what it wrote. The log helpers write to os.Stderr directly — that is the
// channel Stash reads — so asserting on their real output is the only way
// to test them; reimplementing the formatting in the test would pin nothing.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stderr = saved
	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe: %v", err)
	}
	out := <-done
	if err := r.Close(); err != nil {
		t.Fatalf("closing pipe reader: %v", err)
	}
	return out
}

// A newline would make Stash treat the continuation as an unprefixed line
// at the default level, so logLine flattens them.
func TestLogLine_FlattensNewlines(t *testing.T) {
	got := captureStderr(t, func() {
		logLine(logPrefixInfo, "first\nsecond\nthird")
	})
	if want := logPrefixInfo + "first second third\n"; got != want {
		t.Errorf("logLine wrote %q, want %q", got, want)
	}
}

// Exactly one trailing newline: the line terminator Stash splits on. An
// embedded newline is flattened, but the terminator must survive.
func TestLogLine_EmitsOneTerminatingNewline(t *testing.T) {
	got := captureStderr(t, func() { logLine(logPrefixInfo, "plain") })
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
		t.Errorf("logLine wrote %q, want exactly one trailing newline", got)
	}
}

func TestLogLine_FormatsArguments(t *testing.T) {
	got := captureStderr(t, func() { logLine(logPrefixWarning, "scene %s: %d matches", "42", 3) })
	if want := logPrefixWarning + "scene 42: 3 matches\n"; got != want {
		t.Errorf("logLine wrote %q, want %q", got, want)
	}
}

// Each helper must use its own level byte. Getting this wrong is invisible
// locally and silently hides messages on an instance set to Warning.
func TestLogHelpers_UseTheirOwnLevel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fn     func(string, ...any)
		prefix string
	}{
		{"info", logInfo, logPrefixInfo},
		{"warning", logWarning, logPrefixWarning},
		{"error", logError, logPrefixError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureStderr(t, func() { tc.fn("hello") })
			if want := tc.prefix + "hello\n"; got != want {
				t.Errorf("log%s wrote %q, want %q", tc.name, got, want)
			}
		})
	}
}

// Stash reads progress as a float in [0,1]; anything outside that range is
// a job bar that renders wrong, so logProgress clamps rather than passing
// a caller's arithmetic slip through.
func TestLogProgress_ClampsToUnitRange(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{-1, "0.000000"},
		{0, "0.000000"},
		{0.5, "0.500000"},
		{1, "1.000000"},
		{1.5, "1.000000"},
		{42, "1.000000"},
	} {
		got := captureStderr(t, func() { logProgress(tc.in) })
		if want := logPrefixProgress + tc.want + "\n"; got != want {
			t.Errorf("logProgress(%v) wrote %q, want %q", tc.in, got, want)
		}
	}
}

// Progress goes out on its own level, not as an info line — Stash routes it
// to the job bar rather than the log.
func TestLogProgress_UsesProgressLevel(t *testing.T) {
	got := captureStderr(t, func() { logProgress(0.25) })
	if !strings.HasPrefix(got, logPrefixProgress) {
		t.Errorf("logProgress wrote %q, want the progress prefix", got)
	}
	if strings.HasPrefix(got, logPrefixInfo) {
		t.Error("progress went out at info level; Stash would log it instead of moving the job bar")
	}
}
