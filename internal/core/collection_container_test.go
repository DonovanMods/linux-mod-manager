package core

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

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

	got, warnings, err := readCollectionContainer(data)
	require.NoError(t, err)
	require.Equal(t, doc, got)
	require.Empty(t, warnings)
}

func TestCollectionContainer_ReservedAndUnknownMembersWarn(t *testing.T) {
	data := zipOf(t,
		"collection.yaml", "lmm_collection: 1\n",
		"collection.yaml.sig", "sig",
		"files/1/readme.txt", "bundled",
		"notes.txt", "hello",
	)
	doc, warnings, err := readCollectionContainer(data)
	require.NoError(t, err)
	require.Equal(t, "lmm_collection: 1\n", string(doc))
	joined := strings.Join(warnings, "\n")
	require.Contains(t, joined, "signature")
	require.Contains(t, joined, "files/")
	require.Contains(t, joined, "notes.txt")
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
			_, _, err := readCollectionContainer(tc.data)
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
	_, _, err := readCollectionContainer(zipOf(t, members...))
	require.Error(t, err)
	require.Contains(t, err.Error(), "members")
}

func TestCollectionContainer_RatioCap(t *testing.T) {
	// Highly compressible content under the size cap, over the ratio cap:
	// a zip bomb's signature, refused before it is inflated.
	_, _, err := readCollectionContainer(zipOf(t, "collection.yaml", strings.Repeat("a", maxCollectionDocument)))
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
	_, warnings, err := readCollectionContainer(zipOf(t, members...))
	require.NoError(t, err)
	require.Len(t, warnings, maxIgnoredWarnings+1)
	require.Contains(t, warnings[maxIgnoredWarnings], "10 more")
}
