package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKnownConflicts(t *testing.T) {
	var none KnownConflicts
	assert.Nil(t, none.Clone())
	assert.Nil(t, KnownConflicts{}.Clone(), "an empty list clones to none")
	assert.True(t, none.Equal(KnownConflicts{}))

	k := KnownConflicts{
		{Winner: "nexus:1", Loser: "nexus:2", Origin: KnownConflictUser},
		{Winner: "nexus:3", Loser: "nexus:1", Origin: KnownConflictCollection},
	}
	assert.True(t, k.Has("nexus:1", "nexus:2"))
	assert.False(t, k.Has("nexus:2", "nexus:1"), "a pair is directed: the winner is recorded")
	assert.Equal(t, 1, k.Index("nexus:1", "nexus:3"), "Index finds the pair either way round")
	assert.Equal(t, -1, k.Index("nexus:2", "nexus:3"))

	c := k.Clone()
	c[0].Winner = "changed"
	assert.Equal(t, "nexus:1", k[0].Winner, "Clone is a copy")
	assert.False(t, k.Equal(c))
	assert.True(t, k.Equal(k.Clone()))

	kept := k.Keep(map[string]bool{"nexus:1": true, "nexus:2": true})
	assert.Equal(t, KnownConflicts{{Winner: "nexus:1", Loser: "nexus:2", Origin: KnownConflictUser}}, kept,
		"an entry naming a mod the profile no longer has is dropped")
	assert.Nil(t, k.Keep(nil))
}

func TestProfile_KeepKnownConflicts(t *testing.T) {
	p := &Profile{
		Mods: []ModReference{{SourceID: "nexus", ModID: "1"}, {SourceID: "nexus", ModID: "2"}},
		KnownConflicts: KnownConflicts{
			{Winner: "nexus:1", Loser: "nexus:2", Origin: KnownConflictUser},
			{Winner: "nexus:1", Loser: "nexus:9", Origin: KnownConflictUser},
		},
		CollectionBaseline: &CollectionBaseline{KnownConflicts: KnownConflicts{
			{Winner: "nexus:9", Loser: "nexus:2", Origin: KnownConflictCollection},
		}},
	}
	p.KeepKnownConflicts()
	assert.Equal(t, KnownConflicts{{Winner: "nexus:1", Loser: "nexus:2", Origin: KnownConflictUser}}, p.KnownConflicts)
	assert.Nil(t, p.CollectionBaseline.KnownConflicts, "the baseline forgets a removed mod too")
	assert.NotNil(t, p.CollectionBaseline, "an emptied baseline is still a baseline")
}

func TestKnownConflicts_RenameAndSet(t *testing.T) {
	k := KnownConflicts{
		{Winner: "nexus:1", Loser: "nexus:2", Origin: KnownConflictUser},
		{Winner: "nexus:3", Loser: "nexus:1", Origin: KnownConflictCollection},
	}
	r := k.Rename("nexus:1", "local:one")
	assert.Equal(t, KnownConflicts{
		{Winner: "local:one", Loser: "nexus:2", Origin: KnownConflictUser},
		{Winner: "nexus:3", Loser: "local:one", Origin: KnownConflictCollection},
	}, r)
	assert.Equal(t, "nexus:1", k[0].Winner, "Rename is a copy")
	assert.Nil(t, KnownConflicts(nil).Rename("a", "b"))

	set := k.Set()
	assert.True(t, set.Has("nexus:1", "nexus:2"))
	assert.False(t, set.Has("nexus:2", "nexus:1"))
	assert.True(t, set.HasPair("nexus:2", "nexus:1"))
	assert.False(t, set.HasPair("nexus:2", "nexus:3"))
	set.Add("nexus:2", "nexus:3")
	assert.True(t, set.Has("nexus:2", "nexus:3"))
}

func TestCollectionBaseline_Clone(t *testing.T) {
	var none *CollectionBaseline
	assert.Nil(t, none.Clone())
	assert.Equal(t, &CollectionBaseline{}, (&CollectionBaseline{}).Clone(), "an empty baseline stays one")

	b := &CollectionBaseline{
		Overrides:      map[string]string{"a.ini": "abc"},
		LoadOrders:     FileLoadOrders{"x": {"1.archive"}},
		Skipped:        []CollectionSkip{{SourceID: "nexus", ModID: "1", Pin: CollectionPin{Version: "1", FileIDs: []string{"f"}}}},
		KnownConflicts: KnownConflicts{{Winner: "nexus:2", Loser: "nexus:1", Origin: KnownConflictCollection}},
	}
	c := b.Clone()
	assert.Equal(t, b, c)
	c.Overrides["a.ini"] = "changed"
	c.LoadOrders["x"][0] = "changed"
	c.Skipped[0].Pin.FileIDs[0] = "changed"
	c.KnownConflicts[0].Winner = "changed"
	assert.Equal(t, "abc", b.Overrides["a.ini"])
	assert.Equal(t, "1.archive", b.LoadOrders["x"][0])
	assert.Equal(t, "f", b.Skipped[0].Pin.FileIDs[0])
	assert.Equal(t, "nexus:2", b.KnownConflicts[0].Winner, "Clone shares nothing")
}
