package subs

import (
	"strings"
	"testing"
	"time"
)

// Extracted from StashJanitor/internal/subs/match_test.go — only the cases
// that exercise Runtime/RuntimeFit, since match.go/normalize.go/vocab.go
// (and their SceneRef/Index/Vocab-dependent tests) are v2 scope.

func mins(m, s int) time.Duration {
	return time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}

func TestRuntime(t *testing.T) {
	srt := `1
00:00:01,000 --> 00:00:02,500
Hello.

2
01:41:47,100 --> 01:41:48,980
Goodbye.
`
	got, ok := Runtime(strings.NewReader(srt))
	if !ok {
		t.Fatal("Runtime returned ok=false")
	}
	if want := time.Hour + mins(41, 48) + 980*time.Millisecond; got != want {
		t.Errorf("Runtime = %v, want %v", got, want)
	}
}

// Cues are not always in order; the maximum must win, not the last line.
func TestRuntime_UnorderedCues(t *testing.T) {
	srt := `1
00:20:00,000 --> 00:20:01,000
later

2
00:00:06,000 --> 00:00:07,000
credits
`
	got, ok := Runtime(strings.NewReader(srt))
	if !ok || got != mins(20, 1) {
		t.Errorf("Runtime = (%v, %v), want (20m1s, true)", got, ok)
	}
}

func TestRuntime_NoTimestamps(t *testing.T) {
	if _, ok := Runtime(strings.NewReader("not a subtitle")); ok {
		t.Error("expected ok=false for a file with no cues")
	}
}

func TestRuntimeFit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sub, scn time.Duration
		wantPos  bool
	}{
		{"just before the end", mins(10, 52), mins(10, 54), true},
		{"way short", mins(10, 0), mins(40, 0), false},
		{"small overrun tolerated", mins(25, 6), mins(24, 59), true},
		{"large overrun rejected", mins(30, 0), mins(20, 0), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := RuntimeFit(tc.sub, tc.scn)
			if (got > 0) != tc.wantPos {
				t.Errorf("RuntimeFit = %v, wantPositive=%v", got, tc.wantPos)
			}
		})
	}
}
