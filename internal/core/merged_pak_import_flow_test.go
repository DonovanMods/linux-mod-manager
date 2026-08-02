package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/stretchr/testify/require"
)

// importFlowCompilerSource is a full ModSource + source.MergeCompiler fake
// serving one real HTTP download (an ".exmodz" file) - unlike
// fakeCompilerSource (service_icarus_compile_test.go), which stubs
// GetMod/GetModFiles as source.ErrNotSupported, ApplyImport's download
// loop needs a working GetMod->GetModFiles->DownloadMod chain end to end.
type importFlowCompilerSource struct {
	mod      *domain.Mod
	fileName string
	server   *httptest.Server
	content  []byte
}

func newImportFlowCompilerSource(mod *domain.Mod, fileName string, content []byte) *importFlowCompilerSource {
	s := &importFlowCompilerSource{mod: mod, fileName: fileName, content: content}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(s.content)
	}))
	return s
}

func (s *importFlowCompilerSource) Close() { s.server.Close() }

func (s *importFlowCompilerSource) ID() string      { return s.mod.SourceID }
func (s *importFlowCompilerSource) Name() string    { return "Import Flow Compiler Source" }
func (s *importFlowCompilerSource) AuthURL() string { return "" }
func (s *importFlowCompilerSource) ExchangeToken(ctx context.Context, code string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (s *importFlowCompilerSource) Search(ctx context.Context, query source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}
func (s *importFlowCompilerSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	if modID == s.mod.ID {
		return s.mod, nil
	}
	return nil, domain.ErrModNotFound
}
func (s *importFlowCompilerSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (s *importFlowCompilerSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "exmodz-1", Name: "Main", FileName: s.fileName, IsPrimary: true}}, nil
}
func (s *importFlowCompilerSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.server.URL, nil
}
func (s *importFlowCompilerSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}
func (s *importFlowCompilerSource) ValidateSource(sourceFilePath string) error {
	_, err := os.Stat(sourceFilePath)
	return err
}
func (s *importFlowCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, error) {
	var out []byte
	for _, src := range sources {
		data, err := os.ReadFile(src.ExmodzPath)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return nil, os.WriteFile(outputPath, out, 0o644)
}

var (
	_ source.ModSource     = (*importFlowCompilerSource)(nil)
	_ source.MergeCompiler = (*importFlowCompilerSource)(nil)
)

// TestApplyImport_DeployCompile_SyncsMergedPak is the #197 I3 regression
// test: importing a profile that references an ".exmodz" mod downloads
// and validates+retains it (Task 2/3 - zero per-mod deployment members),
// but nothing deployed it into the game directory until this fix, since
// ApplyImport never called syncMergedPak.
func TestApplyImport_DeployCompile_SyncsMergedPak(t *testing.T) {
	svc := newFlowsTestService(t)
	installDir := t.TempDir()
	basePak := filepath.Join(installDir, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	game := &domain.Game{
		ID: "icarus", Name: "Icarus", InstallPath: installDir, ModPath: t.TempDir(),
		DeployMode: domain.DeployCompile, LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"fake-compiler": "external-icarus-id"},
	}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "bear-mount", SourceID: "fake-compiler", Name: "Bear Mount", Version: "1.0", GameID: "icarus"}
	src := newImportFlowCompilerSource(mod, "Bear_Mount.exmodz", []byte("bear-exmodz-bytes"))
	defer src.Close()
	svc.RegisterSource(src)

	profile := &domain.Profile{Name: "target", GameID: game.ID, Mods: []domain.ModReference{{SourceID: "fake-compiler", ModID: "bear-mount", Version: "1.0"}}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)

	plan, err := svc.PlanImport(context.Background(), game, data)
	require.NoError(t, err)
	require.Len(t, plan.Missing, 1)

	result, err := svc.ApplyImport(context.Background(), game, plan, core.ProfileImportOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, result.Installed)

	deployedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	deployedData, err := os.ReadFile(deployedPath)
	require.NoError(t, err, "ApplyImport must sync the merged pak - the imported mod deploys zero files of its own")
	require.Equal(t, "bear-exmodz-bytes", string(deployedData))
}
