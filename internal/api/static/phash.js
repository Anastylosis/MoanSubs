// phash.js computes Stash's perceptual video hash in the browser for
// GET /upload (WP-D3), from the same video file upload.js fingerprints:
// 25 frames sampled the way stash's pkg/hash/videophash samples them,
// tiled 5×5 at 160 px wide, then goimagehash.PerceptionHash over the
// sprite. The video never leaves the browser — frames are decoded by the
// <video> element and drawn to an offscreen canvas, nothing is uploaded.
//
// Two halves, with different fidelity:
//
//   - sprite → 64 bits (phashOfPixels): a bit-exact port of goimagehash
//     v1.1.0 (nfnt/resize Bilinear to 64×64 with its int16 coefficients and
//     integer division, 0.299/0.587/0.114 grayscale, unscaled DCT-II rows
//     then columns, top-left 8×8, upper-median threshold, DC bit = MSB).
//     internal/api/phashjs_test.go proves this against hashes computed with
//     the real goimagehash on synthetic sprites.
//   - file → sprite (spriteOf): the browser's decoder, seek, and drawImage
//     scaler stand in for ffmpeg's, so a few bits can differ from the value
//     Stash stored for the same file. The server matches on Hamming
//     distance, which is why the form labels the result "approximate".
//
// Do not change the algorithm half without updating the Go test's
// expected hashes (regenerated with goimagehash, see that file).

'use strict';

const PHASH_COLUMNS = 5;
const PHASH_ROWS = 5;
const PHASH_FRAME_WIDTH = 160;

// spriteFrameHeight mirrors ffmpeg's scale=160:-2: keep the aspect ratio and
// round the height to the nearest even number (av_rescale rounds to
// nearest; the -2 factor then makes it even).
function spriteFrameHeight(videoWidth, videoHeight) {
  const h = Math.round((PHASH_FRAME_WIDTH / 2) * videoHeight / videoWidth) * 2;
  return Math.max(h, 2);
}

// frameTimes mirrors generateSprite: skip 5% at each end, then 25 evenly
// spaced samples across the middle 90%.
function frameTimes(duration) {
  const count = PHASH_COLUMNS * PHASH_ROWS;
  const offset = 0.05 * duration;
  const step = (0.9 * duration) / count;
  const out = [];
  for (let i = 0; i < count; i++) {
    out.push(offset + i * step);
  }
  return out;
}

// --- goimagehash.PerceptionHash, ported ------------------------------------

// resizeWeights mirrors nfnt/resize's createWeights8 for the Bilinear
// kernel (taps 2, blur 1): for each output index the integer coefficients
// (kernel × 256, truncated) and the first input index they apply to. When
// downscaling the triangle is stretched by the scale factor, so this is an
// area-weighted average, not two-tap interpolation.
function resizeWeights(outLen, scale) {
  const taps = 2;
  const filterLength = taps * Math.max(Math.ceil(scale), 1);
  const filterFactor = Math.min(1 / scale, 1);
  const coeffs = new Int16Array(outLen * filterLength);
  const start = new Int32Array(outLen);
  for (let y = 0; y < outLen; y++) {
    let interpX = scale * (y + 0.5) - 0.5;
    start[y] = Math.trunc(interpX) - Math.trunc(filterLength / 2) + 1;
    interpX -= start[y];
    for (let i = 0; i < filterLength; i++) {
      const x = Math.abs((interpX - i) * filterFactor);
      const k = x <= 1 ? 1 - x : 0;
      coeffs[y * filterLength + i] = Math.trunc(k * 256);
    }
  }
  return { coeffs, start, filterLength };
}

// resizePass mirrors resizeRGBA/resizeNRGBA: one separable pass along the
// rows of `src` (width srcW, height srcH, RGBA), producing the transposed
// image (width srcH, height outLen) exactly as nfnt does, so calling it
// twice resizes both axes. Edge taps clamp to the row's last pixel. The
// sprite is fully opaque, so nfnt's alpha premultiplication on the first
// pass is the identity and is omitted.
function resizePass(src, srcW, srcH, outLen) {
  const scale = srcW / outLen;
  const { coeffs, start, filterLength } = resizeWeights(outLen, scale);
  const dst = new Uint8ClampedArray(srcH * outLen * 4);
  const maxX = srcW - 1;
  for (let x = 0; x < srcH; x++) {
    const rowOff = x * srcW * 4;
    for (let y = 0; y < outLen; y++) {
      let r = 0, g = 0, b = 0, a = 0, sum = 0;
      const s = start[y];
      const ci = y * filterLength;
      for (let i = 0; i < filterLength; i++) {
        const coeff = coeffs[ci + i];
        if (coeff !== 0) {
          let xi = s + i;
          if (xi < 0) {
            xi = 0;
          } else if (xi >= maxX) {
            xi = maxX;
          }
          const p = rowOff + xi * 4;
          r += coeff * src[p];
          g += coeff * src[p + 1];
          b += coeff * src[p + 2];
          a += coeff * src[p + 3];
          sum += coeff;
        }
      }
      // Go's int32 division truncates toward zero; coefficients are
      // non-negative here so Math.trunc is exact. Uint8ClampedArray clamps
      // like clampUint8.
      const o = (y * srcH + x) * 4;
      dst[o] = Math.trunc(r / sum);
      dst[o + 1] = Math.trunc(g / sum);
      dst[o + 2] = Math.trunc(b / sum);
      dst[o + 3] = Math.trunc(a / sum);
    }
  }
  return dst;
}

// dct1D is the unscaled DCT-II goimagehash's Lee-algorithm implementation
// computes; written out directly, since 64 points × 128 rows is cheap.
function dct1D(input, out, len) {
  for (let k = 0; k < len; k++) {
    let sum = 0;
    for (let n = 0; n < len; n++) {
      sum += input[n] * Math.cos((Math.PI / len) * (n + 0.5) * k);
    }
    out[k] = sum;
  }
}

// medianOf64 is goimagehash's MedianOfPixelsFast64/quickSelectMedian,
// ported line for line rather than replaced by a sort: for an even count it
// returns sequence[k-1]/2 + sequence[k]/2 *after its own partitioning*, and
// sequence[k-1] there is whatever the partition left in that slot, which is
// not always the true 32nd-smallest value. Matching the stored Stash hashes
// means matching that quirk, not the textbook median.
function medianOf64(pixels) {
  const seq = Array.from(pixels);
  let low = 0;
  let hi = 63;
  const k = 32;
  while (low < hi) {
    const pivot = Math.trunc(low / 2) + Math.trunc(hi / 2);
    const pivotValue = seq[pivot];
    let storeIdx = low;
    [seq[pivot], seq[hi]] = [seq[hi], seq[pivot]];
    for (let i = low; i < hi; i++) {
      if (seq[i] < pivotValue) {
        [seq[storeIdx], seq[i]] = [seq[i], seq[storeIdx]];
        storeIdx++;
      }
    }
    [seq[hi], seq[storeIdx]] = [seq[storeIdx], seq[hi]];
    if (k <= storeIdx) {
      hi = storeIdx;
    } else {
      low = storeIdx + 1;
    }
  }
  return seq[k - 1] / 2 + seq[k] / 2;
}

// phashOfPixels hashes an RGBA pixel buffer (width w, height h — the sprite)
// exactly as goimagehash.PerceptionHash would, returning 16 lowercase hex
// chars.
function phashOfPixels(rgba, w, h) {
  // nfnt resizes horizontally into a transposed temp image, then
  // horizontally again (i.e. vertically) back into the final orientation.
  const temp = resizePass(rgba, w, h, 64); // transposed: h wide, 64 tall
  const small = resizePass(temp, h, 64, 64); // 64 × 64, upright again

  // Grayscale, row-major (y*64 + x), as rgb2GrayRGBA fills it.
  const pixels = new Float64Array(4096);
  for (let i = 0; i < 4096; i++) {
    pixels[i] = 0.299 * small[i * 4] + 0.587 * small[i * 4 + 1] + 0.114 * small[i * 4 + 2];
  }

  // DCT rows, then columns (DCT2DFast64's order).
  const row = new Float64Array(64);
  const out = new Float64Array(64);
  for (let y = 0; y < 64; y++) {
    for (let x = 0; x < 64; x++) row[x] = pixels[y * 64 + x];
    dct1D(row, out, 64);
    for (let x = 0; x < 64; x++) pixels[y * 64 + x] = out[x];
  }
  for (let x = 0; x < 64; x++) {
    for (let y = 0; y < 64; y++) row[y] = pixels[y * 64 + x];
    dct1D(row, out, 64);
    for (let y = 0; y < 64; y++) pixels[y * 64 + x] = out[y];
  }

  // Top-left 8×8, flattened row-major, thresholded at goimagehash's
  // median — its own quickselect, ported verbatim (see medianOf64).
  const flat = new Float64Array(64);
  for (let i = 0; i < 8; i++) {
    for (let j = 0; j < 8; j++) flat[i * 8 + j] = pixels[i * 64 + j];
  }
  const median = medianOf64(flat);

  let hash = 0n;
  for (let idx = 0; idx < 64; idx++) {
    if (flat[idx] > median) {
      hash |= 1n << BigInt(64 - idx - 1);
    }
  }
  return hash.toString(16).padStart(16, '0');
}

// --- Browser half: file → sprite → hash ------------------------------------

// spriteOf decodes `file` in a detached <video>, seeks to each sample time,
// and draws the frame into a 5×5 sprite canvas at 160 px wide. Resolves to
// {rgba, w, h} or null when the browser cannot decode the container/codec
// (HEVC in Firefox, say) — phash is then simply left for the user.
function spriteOf(file) {
  return new Promise((resolve) => {
    const video = document.createElement('video');
    video.preload = 'auto';
    video.muted = true;
    video.playsInline = true;
    const url = URL.createObjectURL(file);
    let done = false;
    const finish = (result) => {
      if (done) return;
      done = true;
      video.removeAttribute('src');
      video.load();
      URL.revokeObjectURL(url);
      resolve(result);
    };
    video.addEventListener('error', () => finish(null));
    video.addEventListener('loadedmetadata', async () => {
      try {
        if (!video.videoWidth || !video.videoHeight || !isFinite(video.duration) || video.duration <= 0) {
          finish(null);
          return;
        }
        const fw = PHASH_FRAME_WIDTH;
        const fh = spriteFrameHeight(video.videoWidth, video.videoHeight);
        const canvas = document.createElement('canvas');
        canvas.width = fw * PHASH_COLUMNS;
        canvas.height = fh * PHASH_ROWS;
        const ctx = canvas.getContext('2d', { willReadFrequently: true });
        ctx.imageSmoothingEnabled = true;
        ctx.imageSmoothingQuality = 'high';
        const times = frameTimes(video.duration);
        for (let i = 0; i < times.length; i++) {
          await seekTo(video, times[i]);
          const x = fw * (i % PHASH_COLUMNS);
          const y = fh * Math.floor(i / PHASH_COLUMNS);
          ctx.drawImage(video, x, y, fw, fh);
        }
        const img = ctx.getImageData(0, 0, canvas.width, canvas.height);
        finish({ rgba: img.data, w: canvas.width, h: canvas.height });
      } catch (err) {
        console.error('phash:', err);
        finish(null);
      }
    });
    video.src = url;
  });
}

// seekTo resolves after the frame at t is ready to draw; rejects on a
// decode error or if the browser never fires seeked (10 s guard, so a
// stuck decoder can't hang the form forever).
function seekTo(video, t) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      cleanup();
      reject(new Error('seek timed out at ' + t));
    }, 10000);
    const onSeeked = () => {
      cleanup();
      resolve();
    };
    const onError = () => {
      cleanup();
      reject(new Error('decode error while seeking'));
    };
    const cleanup = () => {
      clearTimeout(timer);
      video.removeEventListener('seeked', onSeeked);
      video.removeEventListener('error', onError);
    };
    video.addEventListener('seeked', onSeeked);
    video.addEventListener('error', onError);
    video.currentTime = t;
  });
}

// phashOf is upload.js's entry point: Stash-style phash for a video file,
// or null when this browser can't decode it.
async function phashOf(file) {
  const sprite = await spriteOf(file);
  if (!sprite) {
    return null;
  }
  return phashOfPixels(sprite.rgba, sprite.w, sprite.h);
}

if (typeof window !== 'undefined') {
  window.moansubsPhashOf = phashOf;
}

// Exposed for the Go cross-check test (internal/api/phashjs_test.go), which
// runs the pixel half under Node.
if (typeof module !== 'undefined') {
  module.exports = { phashOfPixels, spriteFrameHeight, frameTimes };
}
