// Package safeyaml decodes YAML that a person wrote by hand, or a remote
// source served, without letting a decoder panic take lmm down (#452).
//
// Every YAML document lmm reads goes through Unmarshal: games.yaml,
// config.yaml, profiles and source definitions are read at the start of
// nearly every command - `lmm serve` included - and docs/configuration.md
// invites editing all of them, so one malformed file has to read as an
// error naming that file, never as a crash that stops every command until
// the user finds it.
package safeyaml

import (
	"fmt"

	"go.yaml.in/yaml/v3"
)

// Unmarshal is yaml.Unmarshal with the decoder's own panics returned as
// errors. The decoder has panicked on malformed input rather than failing:
// gopkg.in/yaml.v3 v3.0.1 did on a merge key over a mapping keyed by a
// mapping ("hash of unhashable type"), which fuzzing #431's profile editor
// found. go.yaml.in/yaml/v3 returns that one as an error; the recovery is
// for the next one nobody has found yet.
func Unmarshal(data []byte, v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("yaml: the decoder failed on this document: %v", r)
		}
	}()
	return yaml.Unmarshal(data, v)
}
