// upload.js progressively enhances GET /upload (WP-D2): given the video
// file (never the subtitle), it fills in oshash/duration_ms/stem entirely
// client-side. The video file itself is never uploaded or read in full —
// only the first and last 64KiB pass through File.slice(...).arrayBuffer(),
// and it has no `name` attribute so a browser never includes it in the
// multipart POST. With JavaScript disabled this file never loads (CSP is
// script-src 'self' only on this page) and the form is exactly WP-D1's:
// three plain text fields the uploader copies from Stash by hand.
//
// The oshash half mirrors internal/hash/oshash.go's ComputeOSHash exactly —
// same chunking, same wraparound arithmetic — and is cross-checked against
// it by internal/api/uploadjs_test.go's TestUploadJS_OSHashMatchesGo
// (skipped when `node` is not on PATH). Do not change the algorithm here
// without updating that Go file's fixtures too.

'use strict';

// OSHASH_CHUNK_SIZE mirrors oshash.go's oshashChunkSize: the head/tail
// window read from each end of the file.
const OSHASH_CHUNK_SIZE = 64 * 1024;

// oshashOf computes Stash's oshash for file: size plus the little-endian
// uint64 sum of 8-byte words in the first and last chunk, wrapped mod 2^64,
// formatted as 16 lowercase hex chars. Returns null for files of 8 bytes or
// fewer — ComputeOSHash's own "size must be > 8 bytes" rejection, mirrored
// here rather than caught server-side, since the video file never reaches
// the server. file only needs `.size` and `.slice(start, end).arrayBuffer()`
// — the subset both the browser's File and Node's Blob (used by the Go
// cross-check test) implement.
async function oshashOf(file) {
  const size = file.size;
  if (size <= 8) {
    return null;
  }

  let chunkSize = OSHASH_CHUNK_SIZE;
  if (size < chunkSize) {
    // Round down to a multiple of 8 so sumUint64LE never sees a partial
    // word — this is what makes head and tail overlap for small files
    // instead of erroring, same as oshash.go.
    chunkSize = Math.floor(size / 8) * 8;
  }

  const head = await file.slice(0, chunkSize).arrayBuffer();
  const tail = await file.slice(size - chunkSize, size).arrayBuffer();

  // BigInt addition never overflows, so the mod-2^64 wrap oshash.go gets
  // for free from uint64 arithmetic is applied once at the end instead of
  // after every word — arithmetically identical since modular addition is
  // associative.
  const sum = sumUint64LE(head) + sumUint64LE(tail) + BigInt(size);
  const wrapped = BigInt.asUintN(64, sum);
  return wrapped.toString(16).padStart(16, '0');
}

// sumUint64LE sums buf as consecutive little-endian uint64 words, matching
// oshash.go's sumUint64LE.
function sumUint64LE(buf) {
  const view = new DataView(buf);
  let sum = 0n;
  for (let offset = 0; offset + 8 <= buf.byteLength; offset += 8) {
    sum += view.getBigUint64(offset, true);
  }
  return sum;
}

// The DOM wiring below only runs in a browser. Under Node (the Go
// cross-check test) `document` is undefined, so requiring this file just
// exposes oshashOf without touching any global.
if (typeof document !== 'undefined') {
  document.addEventListener('DOMContentLoaded', () => {
    const videoInput = document.getElementById('video');
    if (!videoInput) {
      return;
    }
    const oshashField = document.getElementById('oshash');
    const durationField = document.getElementById('duration_ms');
    const stemField = document.getElementById('stem');
    const statusEl = document.getElementById('video-status');

    const setStatus = (msg) => {
      if (statusEl) {
        statusEl.textContent = msg;
      }
    };

    const probeDuration = (file) => {
      const video = document.createElement('video');
      video.preload = 'metadata';
      const url = URL.createObjectURL(file);
      const cleanup = () => URL.revokeObjectURL(url);

      video.addEventListener('loadedmetadata', () => {
        durationField.value = String(Math.round(video.duration * 1000));
        cleanup();
      });
      video.addEventListener('error', () => {
        durationField.value = '';
        setStatus("couldn't read the duration in this browser — type it from Stash.");
        cleanup();
      });
      video.src = url;
    };

    // probePhash fills the phash field from static/phash.js when that
    // script loaded and this browser can decode the file; otherwise the
    // field is left exactly as it was (typed, or empty) — phash is optional
    // and a wrong one is worse than none, so nothing here ever guesses.
    const phashField = document.getElementById('phash');
    // lastComputedPhash lets a second video pick replace the first pick's
    // value while a value the user pasted themselves is never overwritten:
    // Stash's own phash beats the browser's approximation.
    let lastComputedPhash = '';
    const probePhash = (file) => {
      if (typeof window.moansubsPhashOf !== 'function' || !phashField) {
        return;
      }
      const before = phashField.value;
      if (before !== '' && before !== lastComputedPhash) {
        return;
      }
      phashField.placeholder = 'computing…';
      window.moansubsPhashOf(file).then((h) => {
        phashField.placeholder = '';
        if (h === null) {
          setStatus("this browser couldn't decode the video for phash — paste it from Stash if you have it.");
          return;
        }
        if (phashField.value === before) {
          phashField.value = h;
          lastComputedPhash = h;
        }
      }).catch((err) => {
        phashField.placeholder = '';
        console.error('phash:', err);
      });
    };

    videoInput.addEventListener('change', () => {
      const file = videoInput.files && videoInput.files[0];
      if (!file) {
        return;
      }
      setStatus('');

      // Filename stem: drop the extension, same shape the form's own stem
      // field expects (server-side, a bare stem with no further parsing).
      const dot = file.name.lastIndexOf('.');
      stemField.value = dot > 0 ? file.name.slice(0, dot) : file.name;

      oshashOf(file).then((h) => {
        if (h === null) {
          setStatus('file is too small to fingerprint — type oshash by hand.');
          return;
        }
        oshashField.value = h;
      }).catch((err) => {
        setStatus('could not read this file in the browser — type oshash by hand.');
        console.error('oshash:', err);
      });

      probeDuration(file);
      probePhash(file);
    });
  });
}

// Exposed for the Go cross-check test (internal/api/uploadjs_test.go),
// which runs under Node and never defines `document`.
if (typeof module !== 'undefined') {
  module.exports = { oshashOf };
}
