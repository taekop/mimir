// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/dolthub/swiss/blob/main/bits_amd64.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: Dolthub, Inc.

//go:build amd64.v3 && !nosimd

package v2

import (
	"math/bits"

	"github.com/grafana/mimir/pkg/usagetracker/clock"
)

// The group operations in this file are implemented in map_simd_amd64.s, which asmgen generates.
// The build constraint matches GOAMD64=v3, which is what Grafana builds with. It guarantees AVX2
// and POPCNT, so there is no CPU feature check at runtime. Every other build uses map_nonsimd.go.

// groupSize is 16, so that a whole group fits in one 128-bit register.
const groupSize = 16

// bitset has bit i set when slot i of a group matches.
type bitset uint16

// match searches the given index for slots that hold the given prefix.
func (m *index) match(p prefix) bitset {
	return matchAVX2(m, p)
}

// matchEmptyOrSpillmark searches the given index for slots that hold no data, i.e. that are either
// empty or hold a spillmark.
func (m *index) matchEmptyOrSpillmark() bitset {
	return matchEmptyOrSpillmarkAVX2(m)
}

// matchOccupied matches the slots that hold data, i.e. neither empty nor spillmarks.
func (m *index) matchOccupied() bitset {
	return ^m.matchEmptyOrSpillmark()
}

// nextMatch clears and returns the index corresponding to the next set bit in
// the given bitset. It is assumed that the given bitset is nonzero.
func nextMatch(b *bitset) uint32 {
	s := uint32(bits.TrailingZeros16(uint16(*b)))
	*b &= ^(1 << s) // clear bit s.
	return s
}

// cleanupGroup removes the entries of the group that expired at watermark, and returns how many it removed.
// See cleanupGroupAVX2 for how the slots are cleared.
func cleanupGroup(idx *index, d *data, watermark clock.Minutes) int {
	lo, length := expiredRange(watermark)
	return cleanupGroupAVX2(idx, d, lo, length)
}

// expiredRange returns the range of xorData byte values that Cleanup must remove for the
// given watermark: a slot expires when uint8(x-lo) <= length and the slot holds data.
//
// # Turning the clock comparison into a range check
//
// An entry is removed when watermark.GreaterOrEqualThan(value) holds, where value is
// clock.Minutes in [0, 120) and watermark is also in [0, 120). Expanding
// clock.Minutes.GreaterOrEqualThan, that condition is (watermark-value) mod 120 < 60, so the
// values that expire are the 60 consecutive minutes ending at the watermark, wrapping
// around the 120 minute clock face.
//
// Slots hold the value xor-ed (see xorData), that is x = 255-value, so the expiring
// values map to the byte range starting at lo = 255-watermark and running upwards,
// wrapping at 256. Two cases:
//
//   - watermark >= 59: the minutes do not wrap, and x lands in [lo, lo+59], entirely
//     inside [136, 255].
//   - watermark < 59: the minutes wrap, and x lands in [lo, 255] plus [136, 194-watermark].
//     Going up from lo and wrapping at 256, those two pieces are joined by [0, 135], which
//     holds no valid value, so the whole thing is still one range: [lo, lo+195].
//
// Both cases are therefore "uint8(x-lo) <= length", differing only in length. The second
// case sweeps up the empty (0) and spillmark (1) markers as a side effect, so the result
// is always intersected with a separate test for slots that hold data.
//
// # Domain
//
// This is exact for values and watermarks in [0, 120), which is what clock.ToMinutes
// produces. A value in [120, 253] can only come from a corrupt snapshot, and for those this
// check and GreaterOrEqualThan do not agree: this check removes them at the first cleanup
// with a watermark below 59, while GreaterOrEqualThan removes them at another time, or never
// for values above 239.
func expiredRange(watermark clock.Minutes) (lo, length uint8) {
	lo = ^uint8(watermark) // 255 - watermark
	if watermark >= 59 {
		// The 60 minute window ending at the watermark does not wrap around the clock face.
		return lo, 59
	}
	return lo, 195
}

// matchAVX2 returns a bitset with bit i set when idx[i] == p.
//
//go:noescape
func matchAVX2(idx *index, p prefix) bitset

// matchEmptyOrSpillmarkAVX2 returns a bitset with bit i set when idx[i] is empty or a spillmark.
//
//go:noescape
func matchEmptyOrSpillmarkAVX2(idx *index) bitset

// cleanupGroupAVX2 removes the slots of the group that hold data in the expired range, and returns
// how many it removed. A data byte x is in the expired range when uint8(x-lo) <= length.
// Removed slots become empty in both idx and d, except for the last one, which becomes a spillmark.
// When nothing expires, neither idx nor d are written.
//
//go:noescape
func cleanupGroupAVX2(idx *index, d *data, lo, length uint8) int
