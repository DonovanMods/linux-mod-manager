package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

func kc(winner, loser string, origin domain.KnownConflictOrigin) domain.KnownConflict {
	return domain.KnownConflict{Winner: winner, Loser: loser, Origin: origin}
}

const (
	byUser       = domain.KnownConflictUser
	byCollection = domain.KnownConflictCollection
)

func TestAddCollectionKnown(t *testing.T) {
	t.Run("an import marks every pair and records it", func(t *testing.T) {
		p := &domain.Profile{CollectionBaseline: &domain.CollectionBaseline{}}
		assert.True(t, addCollectionKnown(p, domain.KnownConflicts{kc("b", "a", byCollection)}))
		assert.Equal(t, domain.KnownConflicts{kc("b", "a", byCollection)}, p.KnownConflicts)
		assert.Equal(t, domain.KnownConflicts{kc("b", "a", byCollection)}, p.CollectionBaseline.KnownConflicts)
	})
	t.Run("a pair the collection marked before is left as the user has it", func(t *testing.T) {
		// The user unmarked b over a, then flipped the order so a wins:
		// a later install computes a over b, which must not be marked.
		p := &domain.Profile{CollectionBaseline: &domain.CollectionBaseline{KnownConflicts: domain.KnownConflicts{kc("b", "a", byCollection)}}}
		assert.False(t, addCollectionKnown(p, domain.KnownConflicts{kc("a", "b", byCollection)}))
		assert.Nil(t, p.KnownConflicts)
	})
	t.Run("a pair the user already marked either way is theirs", func(t *testing.T) {
		p := &domain.Profile{KnownConflicts: domain.KnownConflicts{kc("a", "c", byUser)}, CollectionBaseline: &domain.CollectionBaseline{}}
		assert.True(t, addCollectionKnown(p, domain.KnownConflicts{kc("c", "a", byCollection)}))
		assert.Equal(t, domain.KnownConflicts{kc("a", "c", byUser)}, p.KnownConflicts)
		assert.Equal(t, domain.KnownConflicts{kc("c", "a", byCollection)}, p.CollectionBaseline.KnownConflicts)
	})
	t.Run("a profile without a baseline still marks", func(t *testing.T) {
		p := &domain.Profile{}
		assert.True(t, addCollectionKnown(p, domain.KnownConflicts{kc("b", "a", byCollection)}))
		assert.Equal(t, domain.KnownConflicts{kc("b", "a", byCollection)}, p.KnownConflicts)
		assert.Nil(t, p.CollectionBaseline, "no baseline is made up")
	})
}

func TestUpdateCollectionKnown(t *testing.T) {
	base := domain.KnownConflicts{kc("b", "a", byCollection), kc("c", "a", byCollection), kc("d", "a", byCollection)}
	p := &domain.Profile{
		KnownConflicts: domain.KnownConflicts{
			kc("x", "a", byUser),
			kc("b", "a", byCollection), // still made: kept
			// c over a: the user unmarked it
			kc("d", "a", byCollection), // no longer made: goes
			kc("e", "x", byUser),
		},
		CollectionBaseline: &domain.CollectionBaseline{KnownConflicts: base},
	}
	next := domain.KnownConflicts{
		kc("b", "a", byCollection),
		kc("c", "a", byCollection),
		kc("f", "a", byCollection), // new: marked
		kc("x", "e", byCollection), // the user holds e over x: theirs wins
	}
	assert.True(t, updateCollectionKnown(p, next))
	assert.Equal(t, domain.KnownConflicts{
		kc("x", "a", byUser), kc("e", "x", byUser),
		kc("b", "a", byCollection), kc("f", "a", byCollection),
	}, p.KnownConflicts)
	assert.Equal(t, next, p.CollectionBaseline.KnownConflicts)

	assert.False(t, updateCollectionKnown(p, next), "the same version again changes nothing")
}
