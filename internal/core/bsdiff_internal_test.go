package core

import (
	"bytes"
	"encoding/binary"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateBSDIFFFixtures rewrites testdata/bsdiff from bsdiffFixtures, with
// the system bzip2 (the standard library only reads bzip2): go test
// ./internal/core -run TestBSDIFFFixtures -update-bsdiff-fixtures.
var updateBSDIFFFixtures = flag.Bool("update-bsdiff-fixtures", false, "rewrite internal/core/testdata/bsdiff")

// bsdiffFixture is one patch the tests apply: old, the control triples
// (add, insert, seek) a bsdiff of old into new would write, and new. A
// fixture with bad set describes a patch the applier must refuse; its new
// is what the triples' blocks are cut from.
type bsdiffFixture struct {
	name    string
	old     []byte
	new     []byte
	ctrls   [][3]int64
	newSize int64 // the header's output size; -1 is len(new)
	bad     bool
}

// manifestV40 and manifestV41 are a same-length edit, as a curator's
// version bump in a mod's manifest is.
const (
	manifestV40 = `{"Name": "Additional Farm Cave", "Version": "4.0"}`
	manifestV41 = `{"Name": "Additional Farm Cave", "Version": "4.1"}`
)

// bsdiffFixtures are the committed patches: real-shaped ones that apply,
// and hostile ones whose header is valid but whose control block lies.
var bsdiffFixtures = []bsdiffFixture{
	{name: "same-length-edit", old: []byte(manifestV40), new: []byte(manifestV41),
		ctrls: [][3]int64{{int64(len(manifestV41)), 0, 0}}, newSize: -1},
	{name: "insert-and-seek", old: []byte("0123456789abcdefghij"),
		new: []byte("0123XYZ456789---abcdefghij"), ctrls: [][3]int64{{4, 3, 0}, {6, 3, 0}, {10, 0, 0}}, newSize: -1},
	{name: "seek-back", old: []byte("abcdefgh"),
		new: []byte("abcdabcdefgh"), ctrls: [][3]int64{{4, 0, -4}, {8, 0, 0}}, newSize: -1},
	{name: "grow-past-old", old: []byte("ab"),
		new: []byte("abcdef"), ctrls: [][3]int64{{6, 0, 0}}, newSize: -1},
	{name: "empty-new", old: []byte("anything"), new: []byte{}, newSize: -1},
	{name: "bad-negative-add", old: []byte("abc"), new: []byte("abc"), ctrls: [][3]int64{{-1, 0, 0}}, newSize: 3, bad: true},
	{name: "bad-add-past-end", old: []byte("abc"), new: []byte("abcdef"), ctrls: [][3]int64{{6, 0, 0}}, newSize: 3, bad: true},
	{name: "bad-insert-past-end", old: []byte("abc"), new: []byte("abcdef"), ctrls: [][3]int64{{2, 4, 0}}, newSize: 3, bad: true},
	{name: "bad-huge-seek", old: []byte("abc"), new: []byte("abc"), ctrls: [][3]int64{{1, 0, 1 << 62}, {2, 0, 0}}, newSize: 3, bad: true},
	{name: "bad-short-ctrl", old: []byte("abc"), new: []byte("abc"), ctrls: [][3]int64{{2, 0, 0}}, newSize: 3, bad: true},
	{name: "bad-idle-spin", old: []byte("abc"), new: []byte("abc"), ctrls: idleTriples(maxBSDIFFIdleTriples + 1), newSize: 3, bad: true},
}

// idleTriples is n triples that move nothing.
func idleTriples(n int) [][3]int64 {
	out := make([][3]int64, n)
	return out
}

// bsdiffOfftout encodes v as bsdiff's sign-magnitude integer.
func bsdiffOfftout(v int64) []byte {
	b := make([]byte, 8)
	mag := v
	if v < 0 {
		mag = -v
	}
	binary.LittleEndian.PutUint64(b, uint64(mag))
	if v < 0 {
		b[7] |= 0x80
	}
	return b
}

// bzip2Compress compresses data with the system bzip2.
func bzip2Compress(t *testing.T, data []byte) []byte {
	t.Helper()
	cmd := exec.Command("bzip2", "-c", "-9")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	require.NoError(t, err)
	return out
}

// buildBSDIFF40 writes the patch f describes: each triple's add bytes are
// new's minus old's (where old has them), its insert bytes new's own.
func buildBSDIFF40(t *testing.T, f bsdiffFixture) []byte {
	t.Helper()
	var ctrl, diff, extra bytes.Buffer
	var newPos, oldPos int64
	for _, c := range f.ctrls {
		ctrl.Write(bsdiffOfftout(c[0]))
		ctrl.Write(bsdiffOfftout(c[1]))
		ctrl.Write(bsdiffOfftout(c[2]))
		for i := int64(0); i < c[0] && newPos+i < int64(len(f.new)); i++ {
			b := f.new[newPos+i]
			if at := oldPos + i; at >= 0 && at < int64(len(f.old)) {
				b -= f.old[at]
			}
			diff.WriteByte(b)
		}
		if c[0] > 0 {
			newPos += c[0]
			oldPos += c[0]
		}
		for i := int64(0); i < c[1] && newPos+i < int64(len(f.new)); i++ {
			extra.WriteByte(f.new[newPos+i])
		}
		if c[1] > 0 {
			newPos += c[1]
		}
		oldPos += c[2]
	}
	cb, db, eb := bzip2Compress(t, ctrl.Bytes()), bzip2Compress(t, diff.Bytes()), bzip2Compress(t, extra.Bytes())
	size := f.newSize
	if size < 0 {
		size = int64(len(f.new))
	}
	var p bytes.Buffer
	p.WriteString(bsdiffMagic)
	p.Write(bsdiffOfftout(int64(len(cb))))
	p.Write(bsdiffOfftout(int64(len(db))))
	p.Write(bsdiffOfftout(size))
	p.Write(cb)
	p.Write(db)
	p.Write(eb)
	return p.Bytes()
}

// readBSDIFFFixture reads the committed patch for f.
func readBSDIFFFixture(t *testing.T, f bsdiffFixture) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "bsdiff", f.name+".patch"))
	require.NoError(t, err)
	return data
}

func TestBSDIFFFixtures(t *testing.T) {
	if !*updateBSDIFFFixtures {
		t.Skip("run with -update-bsdiff-fixtures to rewrite testdata/bsdiff")
	}
	if _, err := exec.LookPath("bzip2"); err != nil {
		t.Skip("bzip2 is not installed")
	}
	dir := filepath.Join("testdata", "bsdiff")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, f := range bsdiffFixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f.name+".patch"), buildBSDIFF40(t, f), 0o644))
	}
}

func TestApplyBSDIFF40_AppliesRealShapedPatches(t *testing.T) {
	for _, f := range bsdiffFixtures {
		if f.bad {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			old := bytes.Clone(f.old)
			got, err := applyBSDIFF40(old, readBSDIFFFixture(t, f), 1<<20)
			require.NoError(t, err)
			assert.Equal(t, f.new, got)
			assert.Equal(t, f.old, old, "the old file is never written")
		})
	}
}

func TestApplyBSDIFF40_RefusesHostileControlBlocks(t *testing.T) {
	for _, f := range bsdiffFixtures {
		if !f.bad {
			continue
		}
		t.Run(f.name, func(t *testing.T) {
			_, err := applyBSDIFF40(f.old, readBSDIFFFixture(t, f), 1<<20)
			require.ErrorIs(t, err, ErrBadPatch)
		})
	}
}

func TestApplyBSDIFF40_RefusesBadHeaders(t *testing.T) {
	good := readBSDIFFFixture(t, bsdiffFixtures[0])
	header := func(ctrl, diff, size int64) []byte {
		p := bytes.Clone(good)
		copy(p[8:16], bsdiffOfftout(ctrl))
		copy(p[16:24], bsdiffOfftout(diff))
		copy(p[24:32], bsdiffOfftout(size))
		return p
	}
	ctrlLen, diffLen, size := bsdiffOfftin(good[8:16]), bsdiffOfftin(good[16:24]), bsdiffOfftin(good[24:32])
	cases := map[string][]byte{
		"empty":              nil,
		"short":              []byte("BSDIFF40"),
		"other format":       append([]byte("BSDIFN40"), good[8:]...),
		"hdiffpatch":         append([]byte("HDIFF13&"), good[8:]...),
		"negative ctrl":      header(-1, diffLen, size),
		"negative diff":      header(ctrlLen, -1, size),
		"negative size":      header(ctrlLen, diffLen, -1),
		"ctrl past end":      header(int64(len(good)), diffLen, size),
		"diff past end":      header(ctrlLen, int64(len(good)), size),
		"overflowing blocks": header(1<<62, 1<<62, size),
		"over the cap":       header(ctrlLen, diffLen, 1<<40),
		"size over content":  header(ctrlLen, diffLen, size+9),
	}
	for name, patch := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := applyBSDIFF40(bsdiffFixtures[0].old, patch, 1<<20)
			require.ErrorIs(t, err, ErrBadPatch)
		})
	}
}

func TestApplyBSDIFF40_OutputCapIsChecked(t *testing.T) {
	f := bsdiffFixtures[0]
	_, err := applyBSDIFF40(f.old, readBSDIFFFixture(t, f), int64(len(f.new))-1)
	require.ErrorIs(t, err, ErrBadPatch)
	got, err := applyBSDIFF40(f.old, readBSDIFFFixture(t, f), int64(len(f.new)))
	require.NoError(t, err)
	assert.Equal(t, f.new, got)
}

// FuzzApplyBSDIFF40 holds the applier to its contract on any input: it
// never panics, never returns more than the cap, and returns exactly the
// header's size when it succeeds.
func FuzzApplyBSDIFF40(f *testing.F) {
	for _, fx := range bsdiffFixtures {
		data, err := os.ReadFile(filepath.Join("testdata", "bsdiff", fx.name+".patch"))
		if err == nil {
			f.Add(fx.old, data)
		}
	}
	f.Add([]byte("abc"), []byte("BSDIFF40"))
	const maxOut = 1 << 16
	f.Fuzz(func(t *testing.T, old, patch []byte) {
		got, err := applyBSDIFF40(old, patch, maxOut)
		if err != nil {
			if got != nil {
				t.Fatalf("an error with output")
			}
			return
		}
		if int64(len(got)) > maxOut {
			t.Fatalf("output of %d bytes over the %d cap", len(got), maxOut)
		}
		if want := bsdiffOfftin(patch[24:32]); int64(len(got)) != want {
			t.Fatalf("output of %d bytes, header says %d", len(got), want)
		}
	})
}
