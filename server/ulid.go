package main

// ulid.go — standard ULID generator (pure stdlib implementation, zero external dependencies).
//
// Structure: 128 bit = 48-bit Unix millisecond timestamp + 80-bit crypto/rand entropy.
// Encoding: Crockford Base32 (26 chars, lowercase); string lexicographic order == time order.
// Globally unique regardless of node count: no coordinator needed, adding a server requires
// no configuration.

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"
)

// ulidCrockford Base32 alphabet (lowercase; i/l/o/u removed to avoid confusion)
const ulidAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

var (
	ulidMu     sync.Mutex
	ulidLastTs int64  // last millisecond timestamp, for same-millisecond monotonic increase
	ulidLast   uint64 // random-bit base within the same millisecond (guarantees in-process monotonicity)
	ulidRand   = make([]byte, 16)
)

// NewULID returns a new standard ULID string (26 chars, time-ordered).
func NewULID() string {
	ts := time.Now().UnixMilli()

	ulidMu.Lock()
	if ts == ulidLastTs {
		// Same millisecond: increment the random part from the previous value, guaranteeing
		// strict in-process monotonicity
		ulidLast++
	} else {
		if _, err := rand.Read(ulidRand); err != nil {
			ulidMu.Unlock()
			panic("ulid: crypto/rand unavailable: " + err.Error())
		}
		ulidLast = binary.BigEndian.Uint64(ulidRand[:8])
		ulidLastTs = ts
	}
	lo := binary.BigEndian.Uint64(ulidRand[8:])
	hi := ulidLast
	ulidMu.Unlock()

	// 48-bit timestamp occupies the high 48 bits; followed by 80-bit random/monotonic bits
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[:8], uint64(ts)<<16|(hi>>48))
	binary.BigEndian.PutUint64(buf[8:], (hi&0xffffffffffff)<<16|(lo>>48))

	// 128 bit -> 26 base32 chars (5 bits per char)
	out := make([]byte, 0, 26)
	acc := uint32(0)
	bits := 0
	for _, b := range buf {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, ulidAlphabet[(acc>>bits)&31])
		}
	}
	if bits > 0 {
		out = append(out, ulidAlphabet[(acc<<(5-bits))&31])
	}
	return string(out)
}

// ulidDecodeAlphabet is the reverse lookup table: char -> value (-1 invalid)
var ulidDecodeAlphabet = func() [256]int8 {
	var m [256]int8
	for i := range m {
		m[i] = -1
	}
	for i, c := range ulidAlphabet {
		m[c] = int8(i)
	}
	return m
}()

// ParseULIDTime parses the millisecond timestamp embedded in a ULID (returns 0 on invalid input).
// The timestamp is the high 48 bits of the 128 bits, i.e. the first 48 bits of the first
// 10 base32 chars.
func ParseULIDTime(s string) int64 {
	if len(s) != 26 {
		return 0
	}
	var hi uint64 // first 10 chars = 50 bits (48 timestamp bits + 2 low random bits)
	for i := 0; i < 10; i++ {
		d := ulidDecodeAlphabet[s[i]]
		if d < 0 {
			return 0
		}
		hi = hi<<5 | uint64(d)
	}
	return int64(hi >> 2)
}

// validULID reports whether s is a valid ULID.
func validULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, c := range []byte(s) {
		if ulidDecodeAlphabet[c] < 0 {
			return false
		}
	}
	return true
}

// ErrInvalidULID is returned where an explicit error is needed.
var ErrInvalidULID = errors.New("invalid ulid")

// normalizeULID trims spaces / lowercases (optional leniency); returns "" if invalid.
func normalizeULID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if validULID(s) {
		return s
	}
	return ""
}
