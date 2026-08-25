# Subtitle kinds

Every track has a **kind**, one of five: `default`, `cc`, `sdh`,
`forced`, or `other`. Kind names what a track *is*, not how good it is —
a badly-timed `default` track and a perfect one are still both
`default`.

A release holds at most one caption per language, so if you're choosing
between two tracks in the same language, the kind is usually what tells
you which one you actually want.

| Kind | What it is | Example |
|---|---|---|
| `default` | Plain dialogue, nothing added. The unmarked common case — it gets no badge at all in the panel or on a release page. | A straight transcript/translation of the spoken lines. |
| `cc` | Closed captions: the broadcast-style transcription of the *spoken* language, including non-dialogue sound cues and speaker labels for deaf and hard-of-hearing viewers. | `(door slams)`, `JOHN: Wait—` |
| `sdh` | Subtitles for the Deaf and Hard of hearing — the same idea as CC, but delivered as a subtitle track, and often a translation into another language rather than a same-language transcript. | `♪ music playing ♪`, `[muffled shouting]` |
| `forced` | Only the foreign-language lines, not a full transcript of the scene. | Subtitling just the one line of dialogue spoken in a different language partway through an otherwise-unsubtitled scene. |
| `other` | Anything that doesn't fit the above, with a short custom label saying what it is. | A countdown-timer edit, a commentary track. |

CC and SDH are routinely confused because both add sound cues and
speaker names for deaf viewers — the difference is *which* transcription
convention was used and whether it's the spoken language or a
translation, not how much detail either carries.

When you upload from the browser, the form suggests `sdh` automatically
if it spots bracketed cues, musical-note glyphs, or all-caps speaker
labels in the text — a suggestion, not a rule, and you can always pick
something else. The Stash plugin instead reads kind from a filename
suffix when pushing sidecars (`.en.sdh.srt`, `.en.cc.srt`,
`.en.forced.srt`), never from the subtitle's content.
