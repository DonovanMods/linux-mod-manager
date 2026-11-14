package core

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zipOf builds a zip from name/content pairs, in order.
func zipOf(t *testing.T, members ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for i := 0; i+1 < len(members); i += 2 {
		f, err := w.CreateHeader(&zip.FileHeader{Name: members[i], Method: zip.Deflate})
		require.NoError(t, err)
		_, err = f.Write([]byte(members[i+1]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestCollectionContainer_RoundTrip(t *testing.T) {
	doc := []byte("lmm_collection: 1\nname: p\n")
	data, err := writeCollectionContainer(doc)
	require.NoError(t, err)
	require.True(t, IsCollectionContainer(data), "a container starts with the zip magic")

	again, err := writeCollectionContainer(doc)
	require.NoError(t, err)
	require.Equal(t, data, again, "the container is deterministic: no timestamps")

	got, bundle, warnings, err := readCollectionContainer(data)
	require.NoError(t, err)
	require.Equal(t, doc, got)
	require.Nil(t, bundle, "no files/, no bundle")
	require.Empty(t, warnings)
}

// TestCollectionContainer_ReservedAndUnknownMembersWarn: the signature and
// a stray member are named in warnings; files/ is read as the bundle since
// #623, no longer ignored.
func TestCollectionContainer_ReservedAndUnknownMembersWarn(t *testing.T) {
	data := zipOf(t,
		"collection.yaml", "lmm_collection: 1\n",
		"collection.yaml.sig", "sig",
		"files/1/readme.txt", "bundled",
		"notes.txt", "hello",
	)
	doc, bundle, warnings, err := readCollectionContainer(data)
	require.NoError(t, err)
	require.Equal(t, "lmm_collection: 1\n", string(doc))
	joined := strings.Join(warnings, "\n")
	require.Contains(t, joined, "signature")
	require.NotContains(t, joined, "files/")
	require.Contains(t, joined, "notes.txt")
	require.Equal(t, []string{"1"}, bundle.names())
	require.Equal(t, int64(7), bundle.items["1"].bytes)
}

func TestCollectionContainer_Refusals(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"no document", zipOf(t, "other.yaml", "x"), "no collection.yaml"},
		{"two documents", zipOf(t, "collection.yaml", "a", "collection.yaml", "b"), "more than one"},
		{"zip slip", zipOf(t, "collection.yaml", "a", "../evil", "x"), "path traversal"},
		{"reserved name", zipOf(t, "collection.yaml", "a", "files/.lmm-file-1", "x"), "reserved name"},
		{"not a zip", []byte("PK\x03\x04garbage"), "not a valid"},
		{"oversized document", zipOf(t, "collection.yaml", strings.Repeat("a", maxCollectionDocument+1)), "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := readCollectionContainer(tc.data)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCollectionContainer_MemberCountCap(t *testing.T) {
	members := []string{"collection.yaml", "a"}
	for i := 0; i < maxCollectionMembers; i++ {
		members = append(members, fmt.Sprintf("files/x/%d", i), "")
	}
	_, _, _, err := readCollectionContainer(zipOf(t, members...))
	require.Error(t, err)
	require.Contains(t, err.Error(), "members")
}

func TestCollectionContainer_RatioCap(t *testing.T) {
	// Highly compressible content under the size cap, over the ratio cap:
	// a zip bomb's signature, refused before it is inflated.
	_, _, _, err := readCollectionContainer(zipOf(t, "collection.yaml", strings.Repeat("a", maxCollectionDocument)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "ratio")
}

// TestCollectionContainer_StrayMemberWarningsAreCapped is #621 review N4:
// the first stray members are named, the rest counted.
func TestCollectionContainer_StrayMemberWarningsAreCapped(t *testing.T) {
	members := []string{"collection.yaml", "lmm_collection: 1\n"}
	for i := 0; i < 30; i++ {
		members = append(members, fmt.Sprintf("stray-%02d.txt", i), "")
	}
	_, _, warnings, err := readCollectionContainer(zipOf(t, members...))
	require.NoError(t, err)
	require.Len(t, warnings, maxIgnoredWarnings+1)
	require.Contains(t, warnings[maxIgnoredWarnings], "10 more")
}

// TestCollectionContainer_BundledMembersKeepAnOlderReadersWarning is #623's
// compatibility promise: an lmm that predates bundled files (#621's reader)
// files every member under files/ as "bundled" and warns, refusing nothing,
// as long as each member's name passes the extractor's guards and the
// container keeps to its size cap - which an export's always do.
func TestCollectionContainer_BundledMembersKeepAnOlderReadersWarning(t *testing.T) {
	item := &bundledItem{name: "my-patch"}
	for _, rel := range []string{"patch.esp", "cfg/deep/a.ini"} {
		content := []byte(rel)
		item.members = append(item.members, bundleMember{rel: rel, size: int64(len(content)),
			open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(content)), nil }})
	}
	require.NoError(t, item.read())
	data, err := writeCollectionContainer([]byte("lmm_collection: 1\n"), containerFiles([]exportBundle{{item: item}})...)
	require.NoError(t, err)
	require.LessOrEqual(t, len(data), maxCollectionContainer)

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	guard := NewExtractor()
	for _, f := range zr.File {
		_, err := guard.sanitizePath("/lmmc", f.Name)
		require.NoError(t, err, f.Name)
		if f.Name != collectionDocMember {
			require.True(t, strings.HasPrefix(path.Clean(f.Name)+"/", collectionFilesDir), "%s is read as bundled by an older reader", f.Name)
			require.True(t, f.Mode().IsRegular(), "%s is a plain file", f.Name)
		}
	}
}

// zipWith builds a container of collection.yaml and the given members.
func zipWith(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create(collectionDocMember)
	require.NoError(t, err)
	_, err = f.Write([]byte("lmm_collection: 1\nname: p\n"))
	require.NoError(t, err)
	for name, data := range members {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// TestCollectionContainer_PatchMembers (#610): a container's patches/ holds
// each patch as patches/<sha256>.diff, checked against its name and its
// format; anything else under patches/ refuses the container.
func TestCollectionContainer_PatchMembers(t *testing.T) {
	patch, err := os.ReadFile(filepath.Join("testdata", "bsdiff", "same-length-edit.patch"))
	require.NoError(t, err)
	sum := sha256.Sum256(patch)
	sha := hex.EncodeToString(sum[:])
	_, bundle, _, err := readCollectionContainer(zipWith(t, map[string][]byte{"patches/" + sha + ".diff": patch}))
	require.NoError(t, err)
	require.NotNil(t, bundle)
	assert.Equal(t, patch, bundle.patches[sha])
	assert.Empty(t, bundle.items)

	other := []byte("HDIFF13&not bsdiff")
	otherSum := sha256.Sum256(other)
	for name, members := range map[string]map[string][]byte{
		"named for another patch": {"patches/" + strings.Repeat("ab", 32) + ".diff": patch},
		"not a sha256 name":       {"patches/readme.txt": patch},
		"upper-case name":         {"patches/" + strings.ToUpper(sha) + ".diff": patch},
		"nested":                  {"patches/x/" + sha + ".diff": patch},
		"another format":          {"patches/" + hex.EncodeToString(otherSum[:]) + ".diff": other},
		"spelled another way":     {"./patches/" + sha + ".diff": patch},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := readCollectionContainer(zipWith(t, members))
			require.Error(t, err)
			assert.True(t, IsInvalidCollection(err), "%v", err)
		})
	}
}
