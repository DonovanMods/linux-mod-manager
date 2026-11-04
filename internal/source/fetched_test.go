package source_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
)

func TestReportFetchedMod(t *testing.T) {
	source.ReportFetchedMod(context.Background(), domain.Mod{ID: "1"}) // no observer: a no-op

	var got []domain.Mod
	ctx := source.WithFetchedModObserver(context.Background(), func(m domain.Mod) { got = append(got, m) })
	source.ReportFetchedMod(ctx, domain.Mod{ID: "1", CategoryName: "UI"})
	assert.Equal(t, []domain.Mod{{ID: "1", CategoryName: "UI"}}, got)

	assert.Equal(t, context.Background(), source.WithFetchedModObserver(context.Background(), nil))
}
