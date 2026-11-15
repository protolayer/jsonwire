package jsonwire

import (
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
)

// referenceParseInt and referenceParseUint are parseInt and parseUint without
// the fast paths.
func referenceParseInt(raw string, bitSize int) (int64, bool) {
	if n, err := strconv.ParseInt(raw, 10, bitSize); err == nil {
		return n, true
	}
	s, ok := intString(raw)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, bitSize)
	if err != nil {
		return 0, false
	}
	return n, true
}

func referenceParseUint(raw string, bitSize int) (uint64, bool) {
	if n, err := strconv.ParseUint(raw, 10, bitSize); err == nil {
		return n, true
	}
	s, ok := intString(raw)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, bitSize)
	if err != nil {
		return 0, false
	}
	return n, true
}

// TestParseIntFastPaths checks that the fast paths of parseInt and parseUint
// give the same result as strconv for each input.
func TestParseIntFastPaths(t *testing.T) {
	t.Parallel()
	check := func(raw string) {
		t.Helper()
		for _, bitSize := range []int{32, 64} {
			gotInt, gotOK := parseInt(raw, bitSize)
			wantInt, wantOK := referenceParseInt(raw, bitSize)
			if gotInt != wantInt || gotOK != wantOK {
				t.Fatalf("parseInt(%q, %d) = %d, %v; want %d, %v", raw, bitSize, gotInt, gotOK, wantInt, wantOK)
			}
			gotUint, gotOK := parseUint(raw, bitSize)
			wantUint, wantOK := referenceParseUint(raw, bitSize)
			if gotUint != wantUint || gotOK != wantOK {
				t.Fatalf("parseUint(%q, %d) = %d, %v; want %d, %v", raw, bitSize, gotUint, gotOK, wantUint, wantOK)
			}
		}
	}
	// All strings of up to 5 bytes from these bytes.
	const alphabet = "-+0159.eE_x "
	var gen func(prefix string, n int)
	gen = func(prefix string, n int) {
		check(prefix)
		if n == 0 {
			return
		}
		for i := range len(alphabet) {
			gen(prefix+alphabet[i:i+1], n-1)
		}
	}
	gen("", 5)
	// Limits of each type, and the values next to them.
	for _, v := range []int64{math.MinInt64, math.MinInt32, math.MaxInt32, math.MaxInt64, 0} {
		for _, d := range []int64{-1, 0, 1} {
			check(strconv.FormatInt(v+d, 10))
		}
	}
	for _, v := range []uint64{math.MaxUint32, math.MaxUint64, 999999999999999999, 1000000000000000000} {
		for _, d := range []uint64{0, 1} {
			check(strconv.FormatUint(v+d, 10))
			check(strconv.FormatUint(v-d, 10))
			check("-" + strconv.FormatUint(v-d, 10))
		}
	}
	// Random numbers with 1 to 20 digits.
	r := rand.New(rand.NewPCG(1, 2))
	for range 100000 {
		b := make([]byte, 1+r.IntN(20))
		for i := range b {
			b[i] = byte('0' + r.IntN(10))
		}
		check(string(b))
		check("-" + string(b))
	}
}
