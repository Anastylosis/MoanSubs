package api

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nodePhashJSDriver runs static/phash.js's phashOfPixels over raw RGBA
// files (width and height given alongside) and prints one hash per line —
// the Node side of TestPhashJS_MatchesGoimagehash.
const nodePhashJSDriver = `
const fs = require('fs');
const { phashOfPixels } = require(process.argv[2]);
const lines = [];
for (const spec of process.argv.slice(3)) {
  const [path, w, h] = spec.split(':');
  const buf = fs.readFileSync(path);
  const rgba = new Uint8ClampedArray(buf.buffer, buf.byteOffset, buf.byteLength);
  lines.push(phashOfPixels(rgba, Number(w), Number(h)));
}
console.log(lines.join('\n'));
`

// phashSprite regenerates the synthetic sprite the expected hashes below
// were computed from: a smooth gradient plus xorshift32 noise, fully
// opaque, w×h RGBA. The generator is duplicated byte-for-byte in the
// throwaway Go program that produced the hashes with the real goimagehash
// v1.1.0 (the library Stash uses), so the test needs no fixture files and
// this repo takes no image dependency.
func phashSprite(w, h int, seed uint32) []byte {
	x := seed
	next := func() uint32 {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		return x
	}
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for px := 0; px < w; px++ {
			r := next()
			i := (y*w + px) * 4
			pix[i+0] = uint8((px*255/w + int(r&0x3f)) & 0xff)
			pix[i+1] = uint8((y*255/h + int((r>>8)&0x3f)) & 0xff)
			pix[i+2] = uint8(((px+y)*127/(w+h) + int((r>>16)&0x7f)) & 0xff)
			pix[i+3] = 255
		}
	}
	return pix
}

// TestPhashJS_MatchesGoimagehash proves static/phash.js's sprite→hash half
// is bit-exact with goimagehash.PerceptionHash (what Stash's
// pkg/hash/videophash calls on its sprite): the expected values were
// produced by running goimagehash v1.1.0 itself over phashSprite's output
// for each size — 16:9, 4:3, ultrawide and portrait 800-wide sprites (the
// shape the browser half builds), a small one, and an upscale. Skips
// without node, like TestUploadJS_OSHashMatchesGo; the browser half
// (decode/seek/draw) is deliberately not covered here — it is approximate
// by nature and is checked by hand against Stash's stored values.
func TestPhashJS_MatchesGoimagehash(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH, skipping JS cross-check")
	}

	cases := []struct {
		w, h int
		seed uint32
		want string
	}{
		{800, 450, 1, "947a47e84bed4247"},
		{800, 600, 2, "946c13dc27cb2d17"},
		{800, 330, 3, "94550be31ce3786b"},
		{800, 1420, 4, "94572df615a64f28"},
		{160, 90, 5, "94177ad52ff00e70"},
		{40, 25, 6, "955d22dd2fc03f05"},
	}

	dir := t.TempDir()
	driver := filepath.Join(dir, "driver.js")
	if err := os.WriteFile(driver, []byte(nodePhashJSDriver), 0o600); err != nil {
		t.Fatal(err)
	}
	js, err := filepath.Abs("static/phash.js")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{driver, js}
	for i, c := range cases {
		path := filepath.Join(dir, fmt.Sprintf("sprite-%d.rgba", i))
		if err := os.WriteFile(path, phashSprite(c.w, c.h, c.seed), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, fmt.Sprintf("%s:%d:%d", path, c.w, c.h))
	}

	out, err := exec.Command(nodePath, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(got) != len(cases) {
		t.Fatalf("want %d hashes, got %d:\n%s", len(cases), len(got), out)
	}
	for i, c := range cases {
		if got[i] != c.want {
			t.Errorf("%dx%d seed %d: phash.js %s, goimagehash %s", c.w, c.h, c.seed, got[i], c.want)
		}
	}
}
