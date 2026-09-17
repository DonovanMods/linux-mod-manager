package core

// #466 review D1: the size/mtime/ctime pre-check that spares a hash is
// only as good as the filesystem's change time. vfat and exFAT have none -
// their "ctime" is the mtime, which a user can set, kept in coarse ticks -
// so a same-size edit in the same tick, or one with the mtime copied back,
// passed for lmm's file and a purge deleted it. The pre-check is now
// trusted only on a filesystem that keeps a real change time, and never
// for a fingerprint taken within one tick of the ctime it recorded (git's
// "racy clean" rule).

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coarseInfo is a FileInfo as vfat reports one: ctime equal to mtime, in
// whole seconds.
type coarseInfo struct {
	size int64
	at   time.Time
}

func (c coarseInfo) Name() string       { return "k.esp" }
func (c coarseInfo) Size() int64        { return c.size }
func (c coarseInfo) Mode() fs.FileMode  { return 0o644 }
func (c coarseInfo) ModTime() time.Time { return c.at }
func (c coarseInfo) IsDir() bool        { return false }
func (c coarseInfo) Sys() any {
	ts := syscall.NsecToTimespec(c.at.UnixNano())
	return &syscall.Stat_t{Size: c.size, Mtim: ts, Ctim: ts}
}

func TestUnchangedSince_ACoarseFileInfoMatchesAnyEditInItsTick(t *testing.T) {
	at := time.Unix(1789633028, 0)
	fp := &db.FileFingerprint{Checksum: "lmm's", Size: 15, MTime: at.UnixNano(), CTime: at.UnixNano()}
	// The user's same-size edit, in the same two-second tick: every stat
	// field the pre-check reads is what the record says.
	assert.True(t, unchangedSince(coarseInfo{size: 15, at: at}, fp),
		"on its own the pre-check cannot tell - which is why its filesystem has to be trusted first")
}

func TestFsKeepsChangeTime_OnlyTheAllowList(t *testing.T) {
	for _, tc := range []struct {
		name  string
		magic int64
		want  bool
	}{
		{"ext4", 0xEF53, true},
		{"btrfs", 0x9123683E, true},
		{"xfs", 0x58465342, true},
		{"tmpfs", 0x01021994, true},
		{"f2fs", 0xF2F52010, true},
		{"bcachefs", 0xCA451A4E, true},
		{"zfs", 0x2FC12FC1, true},
		{"vfat", 0x4d44, false},
		{"exfat", 0x2011BAB0, false},
		{"ntfs3", 0x7366746e, false},
		{"ntfs", 0x5346544e, false},
		{"fuse", 0x65735546, false},
		{"unknown", 0x12345678, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withStatfs(t, tc.magic, nil)
			assert.Equal(t, tc.want, fsKeepsChangeTime("/anything"))
		})
	}
	t.Run("statfs fails", func(t *testing.T) {
		withStatfs(t, 0xEF53, os.ErrPermission)
		assert.False(t, fsKeepsChangeTime("/anything"))
	})
}

// withStatfs makes statfsType answer magic (or err) for the test.
func withStatfs(t *testing.T, magic int64, err error) {
	t.Helper()
	orig := statfsType
	statfsType = func(string) (int64, error) { return magic, err }
	t.Cleanup(func() { statfsType = orig })
}

// TestJudge_OnAFilesystemWithoutAChangeTimeEveryFileIsHashed: the record
// says what a same-size edit's stat says, as it would on vfat; the file is
// hashed there, and found changed. On a trusted filesystem the same stat
// passes - the allow-list is what makes the difference.
func TestJudge_OnAFilesystemWithoutAChangeTimeEveryFileIsHashed(t *testing.T) {
	ctx := context.Background()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	root := t.TempDir()
	game := &domain.Game{ID: "g", ModPath: root}
	dst := filepath.Join(root, "k.esp")
	require.NoError(t, os.WriteFile(dst, []byte("USER'S EDIT, 15"), 0o644)) // lmm wrote "tex AAAA 1.0.0\n"
	info, err := os.Lstat(dst)
	require.NoError(t, err)
	// A record whose size, mtime and ctime are exactly the edited file's.
	require.NoError(t, database.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "p", RelativePath: "k.esp", SourceID: "s", ModID: "m", ModPath: root,
		Fingerprint: &db.FileFingerprint{Checksum: "0000lmm", Size: info.Size(), MTime: info.ModTime().UnixNano(), CTime: inodeChangeTime(info)},
	}))
	jd := deployedJudge{db: database, game: game, profile: "p"}

	withStatfs(t, 0x4d44, nil) // vfat
	j := jd.judge(ctx, "k.esp", dst)
	assert.Equal(t, deployedUsers, j.verdict, "hashed, and found to be the user's")
	assert.Equal(t, "its content changed after lmm deployed it", j.reason)

	withStatfs(t, 0xEF53, nil) // ext4: the pre-check is trusted
	assert.Equal(t, deployedOurs, jd.judge(ctx, "k.esp", dst).verdict)
}

func TestFingerprintOf_ARacyCTimeIsNotRecorded(t *testing.T) {
	fine := time.Unix(1789633028, 123456789)
	whole := time.Unix(1789633028, 0)
	for _, tc := range []struct {
		name   string
		ctime  time.Time
		done   time.Time
		record bool
	}{
		{"fine ctime, fingerprint in the same tick", fine, fine.Add(5 * time.Millisecond), false},
		{"fine ctime, fingerprint well after", fine, fine.Add(time.Second), true},
		{"whole-second ctime, fingerprint a second later", whole, whole.Add(time.Second), false},
		{"whole-second ctime, fingerprint three seconds later", whole, whole.Add(3 * time.Second), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := fingerprintOf(coarseInfo{size: 15, at: tc.ctime}, "sum", 15, tc.done)
			assert.Equal(t, "sum", fp.Checksum)
			if tc.record {
				assert.Equal(t, tc.ctime.UnixNano(), fp.CTime)
			} else {
				assert.Zero(t, fp.CTime, "a racy ctime is left out, so the pre-check never passes for it")
				assert.False(t, unchangedSince(coarseInfo{size: 15, at: tc.ctime}, fp))
			}
		})
	}
}

// TestJudge_OnARealVfatMount runs the vfat case on a real mount when one is
// named by LMM_TEST_VFAT_DIR (a writable directory on vfat or exFAT).
func TestJudge_OnARealVfatMount(t *testing.T) {
	dir := os.Getenv("LMM_TEST_VFAT_DIR")
	if dir == "" {
		t.Skip("LMM_TEST_VFAT_DIR names no vfat/exFAT directory")
	}
	root, err := os.MkdirTemp(dir, "lmm-judge-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	require.False(t, fsKeepsChangeTime(root), "LMM_TEST_VFAT_DIR is not on a filesystem without a change time")

	ctx := context.Background()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	dst := filepath.Join(root, "k.esp")
	require.NoError(t, os.WriteFile(dst, []byte("tex AAAA 1.0.0\n"), 0o644))
	fp, err := fingerprintFile(dst)
	require.NoError(t, err)
	require.NoError(t, database.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "p", RelativePath: "k.esp", SourceID: "s", ModID: "m", ModPath: root, Fingerprint: fp,
	}))
	// A same-size edit with the mtime put back.
	info, err := os.Stat(dst)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, []byte("tex MINE 1.0.0\n"), 0o644))
	require.NoError(t, os.Chtimes(dst, info.ModTime(), info.ModTime()))

	j := deployedJudge{db: database, game: &domain.Game{ID: "g", ModPath: root}, profile: "p"}.judge(ctx, "k.esp", dst)
	assert.Equal(t, deployedUsers, j.verdict)
}

// TestBeginOp_AFlowsKeptFilesEndWithIt (#466 review D3): what one flow's
// removals kept is not handed to the next flow, which would write it back.
func TestBeginOp_AFlowsKeptFilesEndWithIt(t *testing.T) {
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	store := svc.originalsStoreFor("g")
	require.NotNil(t, store)

	release, err := svc.beginOp(context.Background())
	require.NoError(t, err)
	store.rememberKept("Data/k.esp", keptFile{reason: "it could not be checked"})
	_, ok := store.keptBefore("Data/k.esp")
	require.True(t, ok, "the flow itself still knows")
	release()

	_, ok = store.keptBefore("Data/k.esp")
	assert.False(t, ok, "the next flow judges the file again")
}

// TestJudgeLink_OnlyTheActingProfilesRecordsMakeALinkLmms (#466 re-review
// R1): a link outside the cache that only another profile records, with no
// fingerprint, is the user's for this profile - kept with nothing to
// restore - while a judge acting for no profile still counts every record.
func TestJudgeLink_OnlyTheActingProfilesRecordsMakeALinkLmms(t *testing.T) {
	ctx := context.Background()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	root := t.TempDir()
	game := &domain.Game{ID: "g", ModPath: root}
	precious := filepath.Join(t.TempDir(), "precious.txt")
	require.NoError(t, os.WriteFile(precious, []byte("PRECIOUS"), 0o644))
	dst := filepath.Join(root, "k.esp")
	require.NoError(t, os.Symlink(precious, dst))
	require.NoError(t, database.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: "g", Profile: "sym", RelativePath: "k.esp", SourceID: "s", ModID: "m", ModPath: root,
	}))

	j := deployedJudge{db: database, game: game, profile: "default"}.judge(ctx, "k.esp", dst)
	assert.Equal(t, deployedUsers, j.verdict)
	assert.True(t, j.recorded, "a deploy writes no record of its own over it")
	assert.Nil(t, j.kept, "and restores none")
	assert.Contains(t, j.reason, "it is a link")

	assert.Equal(t, deployedOurs, deployedJudge{db: database, game: game, profile: "sym"}.judge(ctx, "k.esp", dst).verdict,
		"sym's own link deployment")
	assert.Equal(t, deployedOurs, deployedJudge{db: database, game: game}.judge(ctx, "k.esp", dst).verdict,
		"a judge acting for no profile counts every record")
}
