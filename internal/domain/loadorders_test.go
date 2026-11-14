package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFileLoadOrders(t *testing.T) {
	var none FileLoadOrders
	assert.True(t, none.IsZero())
	assert.True(t, FileLoadOrders{"x": nil}.IsZero(), "an id with no names orders nothing")
	assert.Nil(t, FileLoadOrders{"x": {}}.Clone())
	assert.True(t, none.Equal(FileLoadOrders{"x": {}}))

	l := FileLoadOrders{"b": {"2.archive"}, "a": {"1.archive", "0.archive"}, "empty": nil}
	assert.False(t, l.IsZero())
	assert.Equal(t, []string{"a", "b"}, l.IDs())
	c := l.Clone()
	assert.Equal(t, FileLoadOrders{"b": {"2.archive"}, "a": {"1.archive", "0.archive"}}, c)
	c["a"][0] = "changed"
	assert.Equal(t, "1.archive", l["a"][0], "Clone is deep")
	assert.True(t, l.Equal(FileLoadOrders{"a": {"1.archive", "0.archive"}, "b": {"2.archive"}}))
	assert.False(t, l.Equal(FileLoadOrders{"a": {"0.archive", "1.archive"}, "b": {"2.archive"}}), "order matters")
}
