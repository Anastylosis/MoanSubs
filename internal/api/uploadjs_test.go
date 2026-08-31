package api

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Anastylosis/MoanSubs/hash"
)

// nodeUploadJSDriver runs static/upload.js's oshashOf over a list of files
// and prints one hash per line ("null" for a file too small to fingerprint)
// — the Node side of TestUploadJS_OSHashMatchesGo's cross-check. Blob is
// the closest thing Node has to the browser's File: both expose `.size`
// and `.slice(start, end).arrayBuffer()`, the entire surface oshashOf uses.
const nodeUploadJSDriver = `
const fs = require('fs');
const { oshashOf } = require(process.argv[2]);

(async () => {
  const lines = [];
  for (const path of process.argv.slice(3)) {
    const buf = fs.readFileSync(path);
    const blob = new Blob([buf]);
    const h = await oshashOf(blob);
    lines.push(h === null ? 'null' : h);
  }
  console.log(lines.join('\n'));
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
`

// TestUploadJS_OSHashMatchesGo cross-checks static/upload.js's oshashOf
// against hash.ComputeOSHash — the same algorithm, ported independently —
// over the same size boundaries hash/oshash_test.go's
// TestComputeOSHash_AgainstReference already exercises (just under/at/over
// one chunk, just under/at/over two chunks), plus both of that file's
// hand-computable fixtures and a too-small file. Skips when `node` is not
// on PATH: CI may not have it, and this is a cross-check, not the
// authoritative test — hash.ComputeOSHash's own tests are.
func TestUploadJS_OSHashMatchesGo(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH, skipping JS cross-check")
	}

	dir := t.TempDir()
	var fixtures []string
	var want []string

	writeFixture := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
		fixtures = append(fixtures, path)

		got, err := hash.ComputeOSHash(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			// Too small to hash — hash.ComputeOSHash's error is oshashOf's
			// null, not a hex string.
			want = append(want, "null")
			return
		}
		want = append(want, string(got))
	}

	// Too small to fingerprint at all (TestComputeOSHash_TooSmall).
	writeFixture("too-small", make([]byte, 5))
	// Hand-computable: all-zero, chunk shrinks to 96 (TestComputeOSHash_HandComputedFixture).
	writeFixture("all-zero-100", make([]byte, 100))
	// Hand-computable: all-0xff, exactly one chunk (TestComputeOSHash_AllOnesFixture).
	writeFixture("all-ff-65536", bytes.Repeat([]byte{0xff}, 65536))

	// Same size boundaries and RNG seed as
	// TestComputeOSHash_AgainstReference: just under the minimum, below/at/
	// above one chunk (64KiB), below/at/above two chunks (128KiB, where
	// head/tail stop overlapping).
	sizes := []int64{9, 65535, 65536, 65537, 131071, 131072, 131073, 200000}
	rng := rand.New(rand.NewSource(99))
	for _, size := range sizes {
		data := make([]byte, size)
		rng.Read(data)
		writeFixture(fmt.Sprintf("random-%d", size), data)
	}

	driverPath := filepath.Join(dir, "driver.js")
	if err := os.WriteFile(driverPath, []byte(nodeUploadJSDriver), 0o600); err != nil {
		t.Fatalf("writing node driver: %v", err)
	}
	uploadJSPath, err := filepath.Abs("static/upload.js")
	if err != nil {
		t.Fatalf("resolving static/upload.js: %v", err)
	}

	args := append([]string{driverPath, uploadJSPath}, fixtures...)
	cmd := exec.Command(nodePath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver: %v\n%s", err, out)
	}

	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(got) != len(want) {
		t.Fatalf("node driver printed %d lines, want %d:\n%s", len(got), len(want), out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fixture %s: JS oshashOf = %q, want %q (from hash.ComputeOSHash)", filepath.Base(fixtures[i]), got[i], want[i])
		}
	}
}
