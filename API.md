# moansubs HTTP API

Base path: `/api/v1`. All bodies are JSON. Errors are
`{"error": "message"}` with a 4xx/5xx status.

Downloads and lookups are anonymous (rate-limited per IP, 300/min).
Uploads require `Authorization: Bearer <account token>` (rate-limited per
token, default 30/hour — see MANUAL.md).

## The bucket contract

Client and server MUST agree bit-for-bit on these definitions; they are
frozen API surface, not implementation detail.

- **oshash** — Stash's oshash *is* the OpenSubtitles moviehash: file size
  plus the little-endian uint64 sum of the first and last 64 KiB, formatted
  `%016x` (always zero-padded, lowercase). The **bucket prefix** is its
  first 5 hex characters.
- **phash** — Stash's 64-bit perceptual hash. On the wire from Stash it is
  `strconv.FormatUint(v, 16)` — **unpadded**, so parse it as a number,
  never compare as strings. Canonical form is zero-padded 16-char lowercase
  hex. Stored server-side as a signed bigint reinterpreting the uint64 bit
  pattern.
- **MIH blocks** — with the phash as a uint64: `b0` = bits 63–51, `b1` =
  50–38, `b2` = 37–25, `b3` = 24–12 (13 bits each), `b4` = bits 11–0
  (12 bits). By pigeonhole, two hashes within Hamming distance 4 share at
  least one block exactly, so querying all five block buckets and filtering
  by true Hamming distance client-side is exact for d≤4.

**Privacy, honestly stated:** the oshash prefix leaks 20 bits. Full phash
block recall requires querying all 5 blocks, and a server that correlates
those requests can reconstruct the full hash — bucketing is the lookup
*mechanism*, not a guarantee against the node operator. Full-hash mode
leaks the same, explicitly.

## Endpoints

### `GET /healthz`

`ok` (200) when the server and database are reachable.

### `GET /api/v1/lookup/oshash/{prefix}`

`prefix` is exactly 5 lowercase hex chars (400 otherwise). Returns every
release in the bucket. An empty bucket is `200` with an empty list — never
a 404.

### `GET /api/v1/lookup/phash/{block}/{val}`

`block` is 0–4; `val` is the block value in lowercase hex (max `1fff` for
blocks 0–3, `fff` for block 4; 400 outside the range). Returns releases in
that block bucket.

### `POST /api/v1/lookup/batch`

```json
{"oshash_prefixes": ["7a604"], "phash_blocks": [{"block": 0, "val": "1fe0"}]}
```

At most 100 entries combined. Response keys mirror the request entries:

```json
{"results": {"oshash:7a604": [<release>...], "phash:0:1fe0": [<release>...]}}
```

Exists so a wall of scene cards costs one request, not forty.

### `POST /api/v1/lookup/exact`

```json
{"oshash": "7a604bd1a3800e67", "phash": "ff00454c6e3f1333", "max_distance": 4}
```

Full-hash mode, opt-in. At least one of `oshash`/`phash` required;
`max_distance` defaults to 4, hard cap 8 (false positives climb sharply
past that). POST deliberately, so hashes stay out of access logs. Response:
`{"releases": [<release>...]}`.

### `POST /api/v1/match`

The v2 no-phash fallback (PLAN.md "Matching" level 5): when a scene has no
phash — or hash lookup simply found nothing — this scores stored releases'
name metadata (title, filename stem, studio, performers) against the query
scene, via the shared `subtitlematch` token/runtime scorer. POST
deliberately, like exact mode: titles and filenames are the user's library
content and must stay out of access logs. This is documented the same way
exact mode is — trusting the node — because there is no bucketed variant of
a name.

```json
{"stem": "some-scene-2023-1080p", "title": "Some Scene",
 "studio": "Some Studio", "performers": ["A Performer"],
 "duration_ms": 1857470}
```

`stem` or `title` is required (at least one non-empty); `duration_ms` (>0)
is required. `studio`/`performers` are optional evidence for the scorer's
vocabulary split. Response:

```json
{
  "verdict": "CONFIRMED",
  "candidates": [
    {"release": {<release shape below>}, "title": "Some Scene",
     "stem": "some-scene-2023-1080p", "score": 130.0, "name_sim": 0.95,
     "delta_ms": -500, "reasons": ["filename match", "runtime +0.5s"]}
  ]
}
```

`verdict` is one of `CONFIRMED`/`LIKELY`/`AMBIGUOUS`/`UNMATCHED`.
**Every verdict here is offer-only** — clients must never auto-apply a
name-match result. A server `CONFIRMED` means the name evidence strongly
agrees, which is a different and weaker claim than "this is the same
file" (levels 1-3's fingerprint identity); it is not grounds to skip user
confirmation. `candidates` is always present (possibly empty) and ordered
best-first; `title`/`stem` echo the stored release's own name metadata so
a client can show what the score was computed against.

### Release shape (shared by all lookups)

```json
{
  "id": 1, "oshash": "7a604bd1a3800e67",
  "phash": "ff00454c6e3f1333",          // padded 16-hex or null
  "duration_ms": 1857470,
  "width": null, "height": null, "video_codec": null,
  "tracks": [{"id": 1, "lang": "pt-BR", "generated": false,
              "license": "CC0", "has_provenance": false,
              "created_at": "..."}]
}
```

The server returns everything in the queried buckets; the **client** is
expected to filter by true oshash equality / Hamming distance and gate
phash matches on `|Δduration| ≤ ~1s`.

### `GET /api/v1/subtitles/{id}`

Full track: body (canonical SRT), `lang` (full BCP-47 as uploaded, e.g.
`pt-BR` — normalizing to a bare subtag for Stash filenames is the
client's job at write time), `generated`, `provenance` (structured JSON
when the track was machine-generated by a tool that records it),
`license`, `source`.

### `POST /api/v1/subtitles` *(auth required)*

```json
{"oshash": "7a604bd1a3800e67", "phash": "ff00454c6e3f1333",
 "md5": "…", "duration_ms": 1857470, "lang": "en", "body": "1\n00:00:01,000 --> …",
 "title": "Some Scene", "stem": "some-scene-2023-1080p", "date": "2023-05-23",
 "studio": "Some Studio", "performers": ["A Performer"]}
```

`oshash`, `duration_ms` (>0), `lang` (parseable BCP-47) and `body` are
required. `title`/`stem`/`date`/`studio`/`performers` are optional scene
name metadata, stored on the release for `POST /api/v1/match` to use
later. They are **backfill-only**: recorded when the release has no name
metadata at all yet, never overwritten or merged column-by-column on a
release that already has some, so two uploaders' descriptions of the same
file can't blend into an inconsistent record. Omit a field entirely if
Stash didn't report it — an empty string is a value, not "absent".
See MANUAL.md "Upload semantics" for the sanitization pipeline. Responses:

- `201` `{"track_id": n, "release_id": n, "generated": bool}` — stored.
- `200` `{…, "duplicate": true}` — byte-identical track already existed;
  its id is returned. Re-running a bulk push is safe.
- `400` — unparseable subtitle, bad language tag, over caps, or runtime
  incompatible with `duration_ms`.
- `401`/`429` — bad token / over the upload budget.
