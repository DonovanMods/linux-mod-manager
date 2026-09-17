package core

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// AuthorText is the author a surface shows for m (#420): the resolved
// display name where the source's author field is an opaque id
// (domain.Mod.AuthorName), the author field otherwise, and "" when the
// source reports no author at all - in which case a surface omits its
// author label rather than printing it with nothing after it (#398, #459).
func AuthorText(m *domain.Mod) string {
	if m == nil {
		return ""
	}
	if m.AuthorName != "" {
		return m.AuthorName
	}
	return m.Author
}

// stampCachedAuthorNames sets AuthorName on each installed row whose
// source keeps display names for its opaque author ids
// (source.AuthorNameCache) and already has one for the row's author. It
// never makes a request: an installed-mod listing is read far too often to
// pay a network round trip per author, and a name resolved by any search,
// detail view or earlier lookup is what it shows.
func (s *Service) stampCachedAuthorNames(rows []domain.InstalledMod) {
	bySource := make(map[string][]int)
	for i := range rows {
		if rows[i].Author != "" && rows[i].AuthorName == "" {
			bySource[rows[i].SourceID] = append(bySource[rows[i].SourceID], i)
		}
	}
	for sourceID, idx := range bySource {
		src, err := s.registry.Get(sourceID)
		if err != nil {
			continue
		}
		cache, ok := src.(source.AuthorNameCache)
		if !ok {
			continue
		}
		authors := make([]string, 0, len(idx))
		for _, i := range idx {
			authors = append(authors, rows[i].Author)
		}
		names := cache.CachedAuthorNames(authors)
		for _, i := range idx {
			rows[i].AuthorName = names[rows[i].Author]
		}
	}
}
