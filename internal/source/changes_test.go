package source_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

func TestReportModChecked(t *testing.T) {
	source.ReportModChecked(context.Background(), "1") // no observer: a no-op

	var got []string
	ctx := source.WithModCheckedObserver(context.Background(), func(id string) { got = append(got, id) })
	source.ReportModChecked(ctx, "1")
	source.ReportModChecked(ctx, "2")
	assert.Equal(t, []string{"1", "2"}, got)

	assert.Equal(t, context.Background(), source.WithModCheckedObserver(context.Background(), nil))
}
