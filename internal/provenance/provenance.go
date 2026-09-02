// Package provenance detects stash-subs machine-generated subtitles and
// extracts their structured provenance, per PLAN.md "AI-generated
// disclosure". Ported from
// ../stash-subs/stash_subs/subtitles.py — MARKER, looks_generated() and the
// Provenance dataclass/JSON shape (Provenance.as_dict/as_json), adapted to
// operate on an in-memory upload ([]byte) rather than a file path.
//
// Do not rely on self-declaration: Detect determines generated status
// itself from the marker, regardless of what an uploader's form field
// claims.
package provenance

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// Marker is the historical stash-subs sentinel (subtitles.py's MARKER),
// embedded in the visible annotation cue of every file it writes. Files
// carrying it exist in the wild forever, so it stays detected forever.
const Marker = "[stash-subs]"

// MarkerScriptorium is the sentinel emitted after the tool's rename to
// Scriptorium (2026-08-04). Detection accepts both markers — this is a
// wire contract: a moansubs node must recognize the new marker BEFORE any
// Scriptorium release starts emitting it, or new generated uploads would
// silently lose their generated flag — exactly the class of failure this
// package exists to prevent.
const MarkerScriptorium = "[scriptorium]"

// markers is every sentinel Detect accepts, oldest first.
var markers = [][]byte{[]byte(Marker), []byte(MarkerScriptorium)}

// sniffBytes mirrors subtitles.py's _SNIFF_BYTES: how much of each end of
// the file to search when identifying stash-subs's own output, so detection
// stays cheap even on a large upload.
const sniffBytes = 4096

// Provenance mirrors the JSON object stash_subs.subtitles.Provenance.as_json
// emits into a VTT NOTE block (Provenance.as_dict): tool, version,
// asr_model, mt_model, src, dst, and — notably — the generation date under
// the JSON key "generated" rather than "date", matching the Python source's
// own field naming exactly. Cues and Media are the optional extra fields
// worker.py passes to as_json/as_dict for the NOTE block and sidecar file
// respectively.
type Provenance struct {
	Tool     string `json:"tool"`
	Version  string `json:"version"`
	ASRModel string `json:"asr_model"`
	MTModel  string `json:"mt_model"`
	Src      string `json:"src"`
	Dst      string `json:"dst"`
	Date     string `json:"generated"`
	Cues     int    `json:"cues,omitempty"`
	Media    string `json:"media,omitempty"`
}

// Translated reports whether this track is a machine translation of a
// machine transcription rather than a plain transcript of what was spoken —
// PLAN.md "AI-generated disclosure": "a machine translation of a machine
// transcription is materially worse than either, and Provenance already
// distinguishes them via src/dst/mt_model." mt_model is only ever set by
// stash-subs when an actual translation pass ran (worker.py's write() only
// passes mt_model when route == "llm").
func (p *Provenance) Translated() bool {
	return p.MTModel != ""
}

// noteRe extracts a VTT NOTE block's body: "NOTE\n<body>\n\n", the exact
// shape subtitles.py:render_vtt emits (a blank line always ends the block,
// and the body itself is collapsed to never contain one — see
// internal/subtitle's mirrored blankLineRe).
var noteRe = regexp.MustCompile(`(?s)NOTE\r?\n(.*?)\r?\n\r?\n`)

// Detect sniffs data — the raw uploaded subtitle bytes, before
// internal/subtitle's parse/sanitize pass discards headers and NOTE blocks —
// for the stash-subs marker and, when present, extracts structured
// provenance from a VTT NOTE block if one exists.
//
// generated is true whenever the marker is found, independent of whether a
// NOTE block exists or its JSON parses: an uploader's own claim is never
// trusted (PLAN.md "AI-generated disclosure" — "the server can determine
// this itself on ingest"). p is nil when generated is false, when there is
// no NOTE block (plain SRT carries the marker but not the JSON — it has no
// comment syntax), or when the NOTE block's JSON is corrupt.
func Detect(data []byte) (generated bool, p *Provenance) {
	if !looksGenerated(data) {
		return false, nil
	}
	note, ok := extractNote(data)
	if !ok {
		return true, nil
	}
	var prov Provenance
	if err := json.Unmarshal([]byte(note), &prov); err != nil {
		return true, nil
	}
	prov.Tool = sanitizeField(prov.Tool)
	prov.Version = sanitizeField(prov.Version)
	prov.ASRModel = sanitizeField(prov.ASRModel)
	prov.MTModel = sanitizeField(prov.MTModel)
	prov.Src = sanitizeField(prov.Src)
	prov.Dst = sanitizeField(prov.Dst)
	prov.Date = sanitizeField(prov.Date)
	prov.Media = sanitizeField(prov.Media)
	return true, &prov
}

// maxProvenanceFieldRunes bounds every string field Detect extracts from an
// uploader-controlled NOTE block — tool/version/asr_model/mt_model/src/dst
// render on the public release page (provenanceLine) and in
// GET /api/v1/subtitles/{id}, and json.Unmarshal alone imposes no length
// limit on any of them.
const maxProvenanceFieldRunes = 200

// sanitizeField strips control characters (runes below 0x20, plus U+007F)
// and caps the result at maxProvenanceFieldRunes — truncated, never
// rejected: the marker itself is still evidence of generation even when a
// NOTE field is hostile.
func sanitizeField(s string) string {
	clean := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		clean = append(clean, r)
	}
	if len(clean) > maxProvenanceFieldRunes {
		clean = clean[:maxProvenanceFieldRunes]
	}
	return string(clean)
}

// looksGenerated ports subtitles.py's looks_generated(): search only the
// head and tail sniffBytes-sized windows, so this stays cheap on a large
// file and works whether the marker was placed at the start or end (mode
// "start" vs "end" annotation placement). The markers are plain ASCII, so
// a direct byte search is equivalent to Python's decode-with-replace-then-
// search — invalid UTF-8 elsewhere in the window cannot hide or fake them.
func looksGenerated(data []byte) bool {
	n := len(data)
	head := data
	if n > sniffBytes {
		head = data[:sniffBytes]
	}
	for _, m := range markers {
		if bytes.Contains(head, m) {
			return true
		}
	}
	if n > sniffBytes {
		tail := data[n-sniffBytes:]
		for _, m := range markers {
			if bytes.Contains(tail, m) {
				return true
			}
		}
	}
	return false
}

// extractNote returns the body of the first VTT NOTE block in data, if any.
func extractNote(data []byte) (string, bool) {
	m := noteRe.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}
