package core_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewService_NilLoggerDiscards(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NotNil(t, svc.Logger())
	assert.False(t, svc.Logger().Enabled(context.Background(), slog.LevelError), "nil Logger must mean discard")
}

func TestNewService_LoggerIsUsed(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), Logger: logger})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	assert.Contains(t, buf.String(), "migrations", "opening a fresh database logs the migration run at debug")
}
