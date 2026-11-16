package larian

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian/lariantest"
)

// The tests build every pak they read, with lariantest: no real game file
// is in the repository.

// testMember is one file of a synthetic pak.
type testMember struct {
	name   string
	data   []byte
	method uint32
	part   uint32
}

// pakOptions shape a synthetic pak beyond its members.
type pakOptions struct {
	version uint32
	flags   byte
}

// buildPak is lariantest.Pak with this package's names.
func buildPak(_ testing.TB, opts pakOptions, members ...testMember) []byte {
	ms := make([]lariantest.Member, len(members))
	for i, m := range members {
		ms[i] = lariantest.Member{Name: m.name, Data: m.data, Method: m.method, Part: m.part}
	}
	return lariantest.Pak(opts.version, opts.flags, ms...)
}

func lz4(src []byte) []byte { return lariantest.LZ4(src) }

func metaLSX(folder, name, uuid, version64 string) []byte {
	return lariantest.MetaLSX(folder, name, uuid, version64)
}
