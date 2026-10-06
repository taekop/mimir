// SPDX-License-Identifier: AGPL-3.0-only

// Command asmgen generates map_simd_amd64.s, the AVX2 implementation of the group operations of
// tenantshard/v2.Map. It is run by "go generate" in the parent package.
//
// The Go declarations of the generated functions are in map_simd_amd64.go, and go vet checks that
// they match the generated frame layout. The signatures below use basic types with the same names
// and sizes, so that avo does not need to load the v2 package, which only builds for amd64.
package main

import (
	. "github.com/mmcloughlin/avo/build" //nolint:revive,staticcheck // avo programs read like assembly listings.
	"github.com/mmcloughlin/avo/gotypes"
	. "github.com/mmcloughlin/avo/operand" //nolint:revive,staticcheck
)

func main() {
	// GOAMD64=v3 guarantees AVX, AVX2 and POPCNT, so no runtime CPU feature check is needed.
	ConstraintExpr("amd64.v3,!nosimd")

	// ones holds 0x01 in every lane: a byte x is either empty (0) or a spillmark (1) when min(x, 1) == x.
	ones := GLOBL("ones", RODATA|NOPTR)
	DATA(0, U64(0x0101010101010101))
	DATA(8, U64(0x0101010101010101))

	// marks is what Cleanup writes into a removed slot: empty (0) everywhere, except for a spillmark (1)
	// in the last slot, which keeps the signal that the group may have spilled into the next one.
	marks := GLOBL("marks", RODATA|NOPTR)
	DATA(0, U64(0))
	DATA(8, U64(0x0100000000000000))

	match()
	matchEmptyOrSpillmark(ones)
	cleanupGroup(ones, marks)

	Generate()
}

func match() {
	TEXT("matchAVX2", NOSPLIT, "func(idx *[16]uint8, p uint8) uint16")
	Doc("matchAVX2 returns a bitset with bit i set when idx[i] == p.")

	idx := Mem{Base: Load(Param("idx"), GP64())}
	x := XMM()
	VPBROADCASTB(addr(Param("p")), x)
	VPCMPEQB(idx, x, x)

	mask := GP32()
	VPMOVMSKB(x, mask)
	Store(mask.As16(), ReturnIndex(0))
	RET()
}

func matchEmptyOrSpillmark(ones Mem) {
	TEXT("matchEmptyOrSpillmarkAVX2", NOSPLIT, "func(idx *[16]uint8) uint16")
	Doc("matchEmptyOrSpillmarkAVX2 returns a bitset with bit i set when idx[i] is empty or a spillmark.")

	x := XMM()
	VMOVDQU(Mem{Base: Load(Param("idx"), GP64())}, x)
	free := XMM()
	VPMINUB(ones, x, free)
	VPCMPEQB(x, free, free)

	mask := GP32()
	VPMOVMSKB(free, mask)
	Store(mask.As16(), ReturnIndex(0))
	RET()
}

func cleanupGroup(ones, marks Mem) {
	TEXT("cleanupGroupAVX2", NOSPLIT, "func(idx *[16]uint8, d *[16]uint8, lo uint8, length uint8) int")
	Doc(
		"cleanupGroupAVX2 removes the slots of the group that hold data in the expired range, and returns how many it removed.",
		"A data byte x is in the expired range when uint8(x-lo) <= length, see expiredRange.",
		"Removed slots become empty in both idx and d, except for the last one, which becomes a spillmark.",
	)

	d := Mem{Base: Load(Param("d"), GP64())}
	x := XMM()
	VMOVDQU(d, x)

	Comment("Lanes in the expired range: min(x-lo, length) == x-lo.")
	lo, length := XMM(), XMM()
	VPBROADCASTB(addr(Param("lo")), lo)
	VPBROADCASTB(addr(Param("length")), length)
	y, expired := XMM(), XMM()
	VPSUBB(lo, x, y)
	VPMINUB(length, y, expired)
	VPCMPEQB(y, expired, expired)

	Comment("Lanes that hold no data: min(x, 1) == x. Data and index always agree on which slots these are.")
	free := XMM()
	VPMINUB(ones, x, free)
	VPCMPEQB(x, free, free)

	Comment("The lanes to remove are the expired ones that hold data.")
	remove := XMM()
	VPANDN(expired, free, remove)

	Comment("POPCNT sets ZF when nothing is removed, and then neither the index nor the data are written.")
	mask := GP32()
	VPMOVMSKB(remove, mask)
	POPCNTL(mask, mask)
	Store(mask.As64(), ReturnIndex(0))
	JZ(LabelRef("done"))

	Comment("Copy the marks into the removed lanes, first for the data, then for the index.")
	VPBLENDVB(remove, marks, x, x)
	VMOVDQU(x, d)

	idx := Mem{Base: Load(Param("idx"), GP64())}
	i := XMM()
	VMOVDQU(idx, i)
	VPBLENDVB(remove, marks, i, i)
	VMOVDQU(i, idx)

	Label("done")
	RET()
}

// addr returns the stack address of a parameter, so that an instruction can read it from there.
func addr(c gotypes.Component) Mem {
	b, err := c.Resolve()
	if err != nil {
		panic(err)
	}
	return b.Addr
}
