package store

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// crockford is the Crockford base32 alphabet ULIDs are written in. Its
// characters sort in the same order as the values they stand for, so ULIDs
// compare correctly as plain strings.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ulidSource keeps the last ULID it made, so the next one in the same
// millisecond can increment it rather than draw fresh randomness.
type ulidSource struct {
	mu     sync.Mutex
	ms     uint64
	hi     uint16 // top 16 bits of the 80-bit random part
	lo     uint64 // bottom 64 bits
	issued bool
}

// ulids is the process-wide source behind NewULID.
var ulids ulidSource

// NewULID returns a 26-character ULID for now: 48 bits of Unix milliseconds
// and 80 random bits from crypto/rand, in Crockford base32.
//
// ULIDs from one process sort in the order they were made. A second ULID in
// the same millisecond, or after the clock has gone back, reuses the last
// timestamp and increments the random part instead of drawing a new one.
func NewULID(now time.Time) string {
	return ulids.next(now)
}

func (g *ulidSource) next(now time.Time) string {
	ms := uint64(now.UnixMilli()) & (1<<48 - 1)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.issued && ms <= g.ms {
		ms = g.ms
		g.lo++
		if g.lo == 0 {
			g.hi++
			if g.hi == 0 {
				// The random part ran out within one millisecond; move to the next.
				ms++
				g.hi, g.lo = randomPart()
			}
		}
	} else {
		g.hi, g.lo = randomPart()
	}
	g.ms, g.issued = ms, true

	var b [16]byte
	binary.BigEndian.PutUint64(b[0:8], ms<<16|uint64(g.hi))
	binary.BigEndian.PutUint64(b[8:16], g.lo)
	return encodeULID(b)
}

// randomPart draws a fresh 80-bit random value. The top bit of the low half
// is cleared so that incrementing it cannot carry for a very long time.
func randomPart() (uint16, uint64) {
	var r [10]byte
	_, _ = rand.Read(r[:]) // crypto/rand.Read never fails as of Go 1.24
	return binary.BigEndian.Uint16(r[0:2]), binary.BigEndian.Uint64(r[2:10]) >> 1
}

// encodeULID writes 128 bits as 26 base32 characters. 26 characters hold 130
// bits, so the first character carries two leading zero bits.
func encodeULID(b [16]byte) string {
	bit := func(k int) byte {
		if k < 0 {
			return 0
		}
		return b[k/8] >> (7 - k%8) & 1
	}
	out := make([]byte, 26)
	for i := range out {
		var v byte
		for j := 0; j < 5; j++ {
			v = v<<1 | bit(i*5+j-2)
		}
		out[i] = crockford[v]
	}
	return string(out)
}
