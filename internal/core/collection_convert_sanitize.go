package core

// collection_convert_sanitize.go holds the converter to the untrusted-input
// rules L1 applies to a document from a stranger (#607 review 5): a
// collection's list and recipe are written by its curator and its source,
// and everything of them that reaches a plan - names, notes, links, feature
// names - is cleaned, capped or refused here, before the conversion reads
// it.

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// safeFeatureSuffix is s as the tail of a feature name (a mod type, an
// unknown key, a load-order id): s itself when it is a plain token - ASCII
// letters, digits and ._-:/+@ - within the line cap, else s escaped
// (strconv.QuoteToASCII), so no control, escape or bidi character ever
// reaches a feature name, and two different names never escape alike.
func safeFeatureSuffix(s string) string {
	plain := s != "" && len(s) <= 128
	for _, r := range s {
		if !plain {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-', r == ':', r == '/', r == '+', r == '@':
		default:
			plain = false
		}
	}
	if plain {
		return s
	}
	q := strconv.QuoteToASCII(s)
	if len(q) > maxCollectionLine {
		q = q[:maxCollectionLine-4] + `..."`
	}
	return q
}

// sanitizeConvertInput is in with every string a conversion can show
// cleaned (domain.SanitizeLine / SanitizeText), held to L1's caps (over one
// is ErrInvalidCollection), every link a plain web address or dropped
// (domain.SafeWebURL), and every feature-name suffix a safe token
// (safeFeatureSuffix). The list and the recipe are copied, never edited in
// place. Fields only ever matched against each other (md5s, logical file
// names, rule references) are left as written: cleaning one side of a match
// would break it.
func sanitizeConvertInput(in convertInput) (convertInput, error) {
	var tooLong error
	line := func(field string, s *string) {
		*s = domain.SanitizeLine(*s)
		if len(*s) > maxCollectionLine && tooLong == nil {
			tooLong = fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrInvalidCollection, field, len(*s), maxCollectionLine)
		}
	}
	text := func(field string, s *string) {
		*s = domain.SanitizeText(*s)
		if len(*s) > maxCollectionText && tooLong == nil {
			tooLong = fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrInvalidCollection, field, len(*s), maxCollectionText)
		}
	}
	link := func(s *string) { *s = domain.SafeWebURL(*s) }

	l := in.list
	if len(l.Items) > maxCollectionItems {
		return in, fmt.Errorf("%w: the collection lists %d items, over the %d limit", ErrInvalidCollection, len(l.Items), maxCollectionItems)
	}
	l.Items = slices.Clone(l.Items)
	line("the collection's name", &l.Name)
	line("the collection's curator", &l.Author)
	link(&l.URL)
	for i := range l.Items {
		it := &l.Items[i]
		line("an item's name", &it.Name)
		line("an item's version", &it.Version)
		line("an item's file name", &it.FileName)
		line("an item's file category", &it.FileCategory)
		line("an item's update policy", &it.UpdatePolicy)
		text("an item's instructions", &it.Instructions)
		link(&it.URL)
	}
	in.list = l

	if in.manifest != nil {
		m := *in.manifest
		if len(m.Mods) > maxCollectionItems {
			return in, fmt.Errorf("%w: the recipe lists %d mods, over the %d limit", ErrInvalidCollection, len(m.Mods), maxCollectionItems)
		}
		m.Mods = slices.Clone(m.Mods)
		m.Tools = slices.Clone(m.Tools)
		m.GameVersions = slices.Clone(m.GameVersions)
		m.Unknown = slices.Clone(m.Unknown)
		m.FileLoadOrders = slices.Clone(m.FileLoadOrders)
		m.ModRules = slices.Clone(m.ModRules)
		line("the recipe's name", &m.Name)
		line("the recipe's curator", &m.Author)
		text("the recipe's description", &m.Description)
		text("the recipe's install instructions", &m.InstallInstructions)
		for i := range m.GameVersions {
			line("a game version", &m.GameVersions[i])
		}
		for i := range m.Mods {
			mod := &m.Mods[i]
			line("a mod's name", &mod.Name)
			line("a mod's version", &mod.Version)
			line("a mod's author", &mod.Author)
			line("a mod's md5", &mod.MD5)
			text("a mod's instructions", &mod.Instructions)
			link(&mod.URL)
			if mod.ModType != "" {
				mod.ModType = safeFeatureSuffix(mod.ModType)
			}
		}
		for i := range m.Tools {
			line("a tool's name", &m.Tools[i].Name)
			line("a tool's program", &m.Tools[i].Exe)
		}
		for i := range m.Unknown {
			u := &m.Unknown[i]
			line("an item's name", &u.Item)
			u.Key = safeFeatureSuffix(u.Key)
		}
		for i := range m.FileLoadOrders {
			m.FileLoadOrders[i].ID = safeFeatureSuffix(m.FileLoadOrders[i].ID)
		}
		for i := range m.ModRules {
			m.ModRules[i].Unknown = slices.Clone(m.ModRules[i].Unknown)
		}
		in.manifest = &m
	}
	return in, tooLong
}
