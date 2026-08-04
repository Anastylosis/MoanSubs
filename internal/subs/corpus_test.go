package subs

import (
	"encoding/json"
	"flag"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// updateGolden regenerates the golden verdict file from today's replay
// instead of checking against it: go test ./internal/subs/ -run
// TestCorpusReplay -update
var updateGolden = flag.Bool("update", false, "regenerate the corpus replay golden file")

// This file replays ../../../subtitle-match-report.md — a report generated
// against a live 61k-scene library by an older tool version, kept outside the
// repo (PLAN.md step 7) — against the ported v2 token scorer. It exists only
// in moansubs; StashJanitor has no equivalent.
//
// The report's "why=" strings do not match current Reasons text (the scorer
// has moved on since the report was generated) and are ignored; only the
// structure — subtitle, candidates, verdict section — is parsed.

// corpusPath is relative to this package's directory (internal/subs), so
// three levels up reaches the repo root's parent, where the corpus lives
// alongside the moansubs checkout.
const corpusPath = "../../../subtitle-match-report.md"

// corpusGoldenPath sits next to the corpus, outside the repo — never inside
// it, since the subtitle stems are the user's live library filenames and
// must not enter tracked files.
const corpusGoldenPath = "../../../subtitle-match-report.golden.json"

// corpusRoot is the scan root the report's "in `<dir>`" lines are relative
// to; everything below it becomes Subtitle.Dirs.
const corpusRoot = "/share/main/Videos"

var (
	corpusHeaderRe    = regexp.MustCompile(`^\d+ orphaned subtitle files\.\s+CONFIRMED=(\d+)\s+LIKELY=(\d+)\s+AMBIGUOUS=(\d+)\s*$`)
	corpusSectionRe   = regexp.MustCompile(`^## (CONFIRMED|LIKELY|AMBIGUOUS)\b`)
	corpusSubtitleRe  = regexp.MustCompile("^\\*\\*`(.+)`\\*\\*\\s+_\\(runtime (.+)\\)_\\s*$")
	corpusDirRe       = regexp.MustCompile("^  in `(.+)`$")
	corpusCandidateRe = regexp.MustCompile(`^\s*(?:->\s*)?#(\d+)\s+score=([-0-9.]+)\s+\[([^\]]+)\](\s*\[already has captions\])?\s*$`)
	corpusPathLineRe  = regexp.MustCompile("^\\s+`(.+)`$")
	corpusHMSRe       = regexp.MustCompile(`^(\d+):(\d{2}):(\d{2})$`)
)

// corpusEntry is one subtitle from the report, paired with the verdict
// recorded for it (the section it was filed under) and the candidate scenes
// the original run listed under it. scenes is per-entry, not pooled: the
// original tool scored this subtitle against its full library and the
// recorded candidates are the top rivals that competed, including whichever
// runner-up decide()'s score-gap thresholds need. Pooling every scene from
// every entry into one shared index (an earlier version of this harness did
// that) introduces rivals that never competed for this subtitle in the
// original run and is a harness artifact, not a scorer behavior.
type corpusEntry struct {
	sub     Subtitle
	verdict Verdict
	scenes  []SceneRef
}

// corpusCounts tallies subtitles by verdict.
type corpusCounts struct {
	Confirmed, Likely, Ambiguous, Unmatched int
}

func (c *corpusCounts) add(v Verdict) {
	switch v {
	case Confirmed:
		c.Confirmed++
	case Likely:
		c.Likely++
	case Ambiguous:
		c.Ambiguous++
	default:
		c.Unmatched++
	}
}

// parseCorpusHMS parses "H:MM:SS", or "?" as unknown (zero).
func parseCorpusHMS(s string) time.Duration {
	if s == "?" {
		return 0
	}
	m := corpusHMSRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(sec)*time.Second
}

// stemOf strips a filename's extension.
func stemOf(filename string) string {
	if i := strings.LastIndex(filename, "."); i > 0 {
		return filename[:i]
	}
	return filename
}

// parseCorpus reads the report and reconstructs the ordered list of
// subtitles, each with its own recorded verdict and its own candidate
// scenes (only the scenes the report listed under that specific subtitle —
// see corpusEntry).
func parseCorpus(data string) (entries []corpusEntry, header corpusCounts) {
	var section Verdict
	var cur *corpusEntry
	var curFilename string

	var pendingDur string
	var pendingHasCaption, havePending bool
	var pendingIdx int // index into cur.scenes of the candidate awaiting its path line

	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimRight(raw, "\r")

		// The copy plan section reuses "#<id>" and backtick-path shapes for a
		// different purpose (from/to renames); stop before it.
		if strings.HasPrefix(line, "## Copy plan") {
			break
		}
		if m := corpusHeaderRe.FindStringSubmatch(line); m != nil {
			header.Confirmed, _ = strconv.Atoi(m[1])
			header.Likely, _ = strconv.Atoi(m[2])
			header.Ambiguous, _ = strconv.Atoi(m[3])
			continue
		}
		if m := corpusSectionRe.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "CONFIRMED":
				section = Confirmed
			case "LIKELY":
				section = Likely
			case "AMBIGUOUS":
				section = Ambiguous
			}
			cur = nil
			continue
		}
		if m := corpusSubtitleRe.FindStringSubmatch(line); m != nil {
			curFilename = m[1]
			entries = append(entries, corpusEntry{
				sub: Subtitle{
					Stem:    stemOf(curFilename),
					Runtime: parseCorpusHMS(m[2]),
				},
				verdict: section,
			})
			cur = &entries[len(entries)-1]
			havePending = false
			continue
		}
		if cur == nil {
			continue
		}
		if m := corpusDirRe.FindStringSubmatch(line); m != nil {
			dir := m[1]
			rel := strings.TrimPrefix(dir, corpusRoot)
			rel = strings.Trim(rel, "/")
			if rel != "" {
				cur.sub.Dirs = strings.Split(rel, "/")
			}
			cur.sub.Path = dir + "/" + curFilename
			continue
		}
		if m := corpusCandidateRe.FindStringSubmatch(line); m != nil {
			cur.scenes = append(cur.scenes, SceneRef{
				ID:         m[1],
				Duration:   parseCorpusHMS(m[3]),
				HasCaption: m[4] != "",
			})
			pendingDur = m[3]
			pendingHasCaption = m[4] != ""
			pendingIdx = len(cur.scenes) - 1
			havePending = true
			continue
		}
		if havePending {
			if m := corpusPathLineRe.FindStringSubmatch(line); m != nil {
				p := m[1]
				sc := &cur.scenes[pendingIdx]
				sc.Path = p
				sc.Stem = stemOf(path.Base(p))
				sc.Duration = parseCorpusHMS(pendingDur)
				sc.HasCaption = pendingHasCaption
				havePending = false
				continue
			}
		}
		// "why=" lines and blank lines carry no structure we need.
	}

	return entries, header
}

// TestCorpusReplay replays the real-library regression corpus
// (../../../subtitle-match-report.md, gitignored, not present in CI) through
// the ported matcher, one subtitle at a time.
//
// Each subtitle is matched against a mini-Index built ONLY from that
// subtitle's own recorded candidates (see corpusEntry) — the actual rivals
// that competed for it in the original full-library run — rather than a
// library pooled from every entry in the report, which would let scenes that
// never competed for this subtitle collide with it by coincidence of shared
// tokens (a harness artifact an earlier version of this test had).
//
// Even so, this replay cannot reproduce the original run bit-for-bit: the
// report records no scene titles/studios/performers (Title is always empty
// here) and no performer/studio vocabulary (Vocab is nil), and it was
// generated by an older, different version of the scorer than what's ported
// here (its "why=" strings don't match current Reasons text, which is why
// they're ignored rather than parsed). So the corpus's job is to anchor a
// realistic INPUT — real filenames, real runtimes, real candidate sets from a
// live library — while a golden file pins what THIS ported scorer verifiably
// does with that input today, so a future change to match.go/normalize.go/
// vocab.go that silently shifts verdicts on real data gets caught.
//
// If the fix ever legitimately changes verdicts here (an intentional scoring
// change, not a regression), regenerate the golden file with -update; do not
// hand-edit it.
func TestCorpusReplay(t *testing.T) {
	data, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Skipf("regression corpus not present at %s (expected outside CI): %v", corpusPath, err)
	}

	entries, header := parseCorpus(string(data))
	if got, want := len(entries), header.Confirmed+header.Likely+header.Ambiguous; got != want {
		t.Fatalf("parsed %d subtitle entries, but the report header claims CONFIRMED=%d LIKELY=%d AMBIGUOUS=%d (%d total)",
			got, header.Confirmed, header.Likely, header.Ambiguous, want)
	}

	replayed := make(map[string]Verdict, len(entries))
	var recordedTally, replayedTally corpusCounts
	var diffs []string
	for _, e := range entries {
		ix := NewIndex(e.scenes, nil)
		m := ix.Match(e.sub, 5)
		replayed[e.sub.Stem] = m.Verdict
		recordedTally.add(e.verdict)
		replayedTally.add(m.Verdict)
		if m.Verdict != e.verdict {
			best := "no candidate"
			if b := m.Best(); b != nil {
				best = b.Scene.ID + " score=" + strconv.FormatFloat(b.Score, 'f', 1, 64)
			}
			diffs = append(diffs, "  "+e.sub.Stem+": recorded="+string(e.verdict)+" replayed="+string(m.Verdict)+" ("+best+", "+strconv.Itoa(len(e.scenes))+" candidates)")
		}
		t.Logf("%-90s recorded=%-10s replayed=%-10s", e.sub.Stem, e.verdict, m.Verdict)
	}

	t.Logf("recorded distribution:  CONFIRMED=%d LIKELY=%d AMBIGUOUS=%d",
		recordedTally.Confirmed, recordedTally.Likely, recordedTally.Ambiguous)
	t.Logf("replayed distribution:  CONFIRMED=%d LIKELY=%d AMBIGUOUS=%d UNMATCHED=%d",
		replayedTally.Confirmed, replayedTally.Likely, replayedTally.Ambiguous, replayedTally.Unmatched)
	if len(diffs) > 0 {
		t.Logf("%d/%d entries differ from the recorded verdict:\n%s", len(diffs), len(entries), strings.Join(diffs, "\n"))
	} else {
		t.Logf("all %d entries match the recorded verdict", len(entries))
	}

	if *updateGolden {
		golden, err := json.MarshalIndent(replayed, "", "  ")
		if err != nil {
			t.Fatalf("marshal golden: %v", err)
		}
		if err := os.WriteFile(corpusGoldenPath, golden, 0o644); err != nil {
			t.Fatalf("write golden file %s: %v", corpusGoldenPath, err)
		}
		t.Logf("wrote golden file %s (%d entries)", corpusGoldenPath, len(replayed))
		return
	}

	goldenData, err := os.ReadFile(corpusGoldenPath)
	if err != nil {
		t.Skipf("golden file not present at %s; regenerate with: go test ./internal/subs/ -run TestCorpusReplay -update", corpusGoldenPath)
	}
	var golden map[string]Verdict
	if err := json.Unmarshal(goldenData, &golden); err != nil {
		t.Fatalf("parse golden file %s: %v", corpusGoldenPath, err)
	}

	if len(golden) != len(replayed) {
		t.Errorf("golden file has %d entries, replay produced %d — regenerate with -update if this is an intentional corpus/scorer change",
			len(golden), len(replayed))
	}
	for stem, want := range golden {
		got, ok := replayed[stem]
		if !ok {
			t.Errorf("golden entry %q not present in this replay (corpus changed?)", stem)
			continue
		}
		if got != want {
			t.Errorf("regression: %q replayed %s, golden pinned %s — this is either a scorer behavior change "+
				"(regenerate the golden with -update if intentional) or an actual regression", stem, got, want)
		}
	}
}
