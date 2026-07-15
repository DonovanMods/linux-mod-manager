package custom

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// lookupPath traverses a decoded JSON document (map[string]any tree) by a
// dot-separated path. Returns (value, true) when every segment resolves;
// (nil, false) when any segment is missing or a non-object is traversed
// into. Array indexing is not supported in v1 (design §4).
func lookupPath(doc any, path string) (any, bool) {
	cur := doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// coerceString renders a JSON scalar as a string. JSON numbers decode as
// float64, so integer ids must format without a trailing ".0".
func coerceString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// coerceInt64 renders a JSON scalar as an int64, best effort (0 on failure).
func coerceInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// pathString resolves a mapping path to a string; "" when the mapping key or
// the path is absent.
func pathString(doc any, mapping map[string]string, key string) string {
	path, ok := mapping[key]
	if !ok {
		return ""
	}
	v, ok := lookupPath(doc, path)
	if !ok {
		return ""
	}
	return coerceString(v)
}

// mapMod builds a domain.Mod from a decoded JSON object using the definition's
// mod mappings. id and name are required (design §4); everything else is a
// zero value when missing or unmapped.
func mapMod(doc any, mapping map[string]string, sourceID string) (domain.Mod, error) {
	mod := domain.Mod{SourceID: sourceID}

	mod.ID = pathString(doc, mapping, "id")
	if mod.ID == "" {
		return domain.Mod{}, fmt.Errorf(`response is missing required field "id" (mapped from %q)`, mapping["id"])
	}
	mod.Name = pathString(doc, mapping, "name")
	if mod.Name == "" {
		return domain.Mod{}, fmt.Errorf(`response is missing required field "name" (mapped from %q)`, mapping["name"])
	}

	mod.Version = pathString(doc, mapping, "version")
	mod.Author = pathString(doc, mapping, "author")
	mod.Summary = pathString(doc, mapping, "summary")
	mod.Description = pathString(doc, mapping, "description")
	if mod.Description == "" {
		mod.Description = mod.Summary
	}
	mod.SourceURL = pathString(doc, mapping, "url")
	mod.PictureURL = pathString(doc, mapping, "picture_url")

	if path, ok := mapping["downloads"]; ok {
		if v, found := lookupPath(doc, path); found {
			mod.Downloads = coerceInt64(v)
		}
	}
	if ts := pathString(doc, mapping, "updated_at"); ts != "" {
		if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
			mod.UpdatedAt = parsed // unparseable -> zero value, by design
		}
	}

	return mod, nil
}

// mapFile builds a domain.DownloadableFile from a decoded JSON object using
// the definition's file mappings. id is required (design §4).
func mapFile(doc any, mapping map[string]string) (domain.DownloadableFile, error) {
	f := domain.DownloadableFile{}

	f.ID = pathString(doc, mapping, "id")
	if f.ID == "" {
		return domain.DownloadableFile{}, fmt.Errorf(`response is missing required field "id" (mapped from %q)`, mapping["id"])
	}
	f.Name = pathString(doc, mapping, "name")
	f.FileName = pathString(doc, mapping, "filename")
	f.Version = pathString(doc, mapping, "version")
	if path, ok := mapping["size"]; ok {
		if v, found := lookupPath(doc, path); found {
			f.Size = coerceInt64(v)
		}
	}
	return f, nil
}
