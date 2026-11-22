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
