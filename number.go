// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE-protobuf-go file.
//
// This file is a port of google.golang.org/protobuf/internal/encoding/json.

package jsonwire

import (
	"math"
	"strconv"
	"strings"
)

// parseNumber reads a JSON number at the start of s, based on
// https://tools.ietf.org/html/rfc7159#section-6. It returns the number of
// bytes that are part of the number.
func parseNumber(s string) (int, bool) {
	var n int
	if len(s) == 0 {
		return 0, false
	}

	// Optional -
	if s[0] == '-' {
		s = s[1:]
		n++
		if len(s) == 0 {
			return 0, false
		}
	}

	// Digits
	switch {
	case s[0] == '0':
		s = s[1:]
		n++

	case '1' <= s[0] && s[0] <= '9':
		s = s[1:]
		n++
		for len(s) > 0 && isDigit(s[0]) {
			s = s[1:]
			n++
		}

	default:
		return 0, false
	}

	// . followed by 1 or more digits.
	if len(s) >= 2 && s[0] == '.' && isDigit(s[1]) {
		s = s[2:]
		n += 2
		for len(s) > 0 && isDigit(s[0]) {
			s = s[1:]
			n++
		}
	}

	// e or E followed by an optional - or + and 1 or more digits.
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		n++
		if s[0] == '+' || s[0] == '-' {
			s = s[1:]
			n++
			if len(s) == 0 {
				return 0, false
			}
		}
		for len(s) > 0 && isDigit(s[0]) {
			s = s[1:]
			n++
		}
	}
	return n, true
}

func isDigit(c byte) bool {
	return '0' <= c && c <= '9'
}

// numberParts is the result of parsing out a valid JSON number. It contains
// the parts of a number. The parts are used for integer conversion.
type numberParts struct {
	neg  bool
	intp string
	frac string
	exp  string
}

// parseNumberParts constructs numberParts from the given valid JSON number.
// The fields in numberParts are substrings of the input.
func parseNumberParts(s string) (numberParts, bool) {
	var neg bool
	var intp, frac, exp string

	if len(s) == 0 {
		return numberParts{}, false
	}

	// Optional -
	if s[0] == '-' {
		neg = true
		s = s[1:]
		if len(s) == 0 {
			return numberParts{}, false
		}
	}

	// Digits
	switch {
	case s[0] == '0':
		// Skip first 0 and no need to store.
		s = s[1:]

	case '1' <= s[0] && s[0] <= '9':
		n := 1
		for n < len(s) && isDigit(s[n]) {
			n++
		}
		intp, s = s[:n], s[n:]

	default:
		return numberParts{}, false
	}

	// . followed by 1 or more digits.
	if len(s) >= 2 && s[0] == '.' && isDigit(s[1]) {
		n := 2
		for n < len(s) && isDigit(s[n]) {
			n++
		}
		frac, s = s[1:n], s[n:]
	}

	// e or E followed by an optional - or + and 1 or more digits.
	if len(s) >= 2 && (s[0] == 'e' || s[0] == 'E') {
		s = s[1:]
		n := 0
		if s[0] == '+' || s[0] == '-' {
			n++
			if len(s) == 1 {
				return numberParts{}, false
			}
		}
		for n < len(s) && isDigit(s[n]) {
			n++
		}
		exp = s[:n]
	}

	return numberParts{
		neg:  neg,
		intp: intp,
		frac: strings.TrimRight(frac, "0"), // Remove unnecessary 0s to the right.
		exp:  exp,
	}, true
}

// normalizeToIntString returns an integer string in normal form without the
// E-notation for the given numberParts. It returns false if the number is not
// an integer or if the exponent is too large.
func normalizeToIntString(n numberParts) (string, bool) {
	intpSize := len(n.intp)
	fracSize := len(n.frac)

	if intpSize == 0 && fracSize == 0 {
		return "0", true
	}

	var exp int
	if len(n.exp) > 0 {
		i, err := strconv.ParseInt(n.exp, 10, 32)
		if err != nil {
			return "", false
		}
		exp = int(i)
	}

	var num string
	if exp >= 0 {
		// For positive E, shift fraction digits into integer part and also pad
		// with zeroes as needed.

		// If there are more digits in fraction than the E value, then the
		// number is not an integer.
		if fracSize > exp {
			return "", false
		}

		// Make sure resulting digits are within max value limit to avoid
		// unnecessarily constructing a large string that may simply fail later
		// on.
		const maxDigits = 20 // Max uint64 value has 20 decimal digits.
		// Compare without an addition, which can overflow on 32-bit platforms.
		if exp > maxDigits-intpSize {
			return "", false
		}

		num = n.intp + n.frac + strings.Repeat("0", exp-fracSize)
	} else {
		// For negative E, shift digits in integer part out.

		// If there are fractions, then the number is not an integer.
		if fracSize > 0 {
			return "", false
		}

		// index is where the decimal point will be after adjusting for
		// negative exponent.
		index := intpSize + exp
		if index < 0 {
			return "", false
		}

		// If any of the digits being shifted to the right of the decimal point
		// is non-zero, then the number is not an integer.
		for i := index; i < intpSize; i++ {
			if n.intp[i] != '0' {
				return "", false
			}
		}
		num = n.intp[:index]
	}

	if n.neg {
		return "-" + num, true
	}
	return num, true
}

// intString returns the given JSON number as an integer string, or false if
// the number is not an integer.
func intString(raw string) (string, bool) {
	parts, ok := parseNumberParts(raw)
	if !ok {
		return "", false
	}
	return normalizeToIntString(parts)
}

// parseInt returns the signed integer value of the given JSON number. It
// returns false if the number is not an integer or does not fit in bitSize.
func parseInt(raw string, bitSize int) (int64, bool) {
	// Fast path for short plain integers, which are the common case.
	if n, ok := parseDigits(raw, bitSize, true); ok {
		return int64(n), true
	}
	// strconv never accepts a fraction or an exponent, so a number with one
	// goes straight to intString. Then strconv does not allocate an error
	// that is not used.
	if isPlainInt(raw) {
		if n, err := strconv.ParseInt(raw, 10, bitSize); err == nil {
			return n, true
		}
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

// parseUint returns the unsigned integer value of the given JSON number. It
// returns false if the number is not an unsigned integer or does not fit in
// bitSize.
func parseUint(raw string, bitSize int) (uint64, bool) {
	if n, ok := parseDigits(raw, bitSize, false); ok {
		return n, true
	}
	if isPlainInt(raw) {
		if n, err := strconv.ParseUint(raw, 10, bitSize); err == nil {
			return n, true
		}
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

// isPlainInt reports whether the JSON number raw has no fraction and no
// exponent.
func isPlainInt(raw string) bool {
	// A loop is faster than strings.ContainsAny for short numbers.
	for i := range len(raw) {
		switch raw[i] {
		case '.', 'e', 'E':
			return false
		}
	}
	return true
}

// parseDigits parses an optional minus sign, if signed is true, and 1 to 18
// digits in one pass. 18 digits always fit in 64 bits. It returns false for
// all other input and for a value that does not fit in bitSize. Then the
// caller uses strconv, which gives the same result for the input that
// parseDigits accepts. For a signed value, the result is the two's complement.
func parseDigits(raw string, bitSize int, signed bool) (uint64, bool) {
	s := raw
	neg := false
	if signed && len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	if len(s) == 0 || len(s) > 18 {
		return 0, false
	}
	var n uint64
	for i := range len(s) {
		c := s[i] - '0'
		if c > 9 {
			return 0, false
		}
		n = n*10 + uint64(c)
	}
	if bitSize == 32 {
		switch {
		case !signed && n > math.MaxUint32:
			return 0, false
		case signed && !neg && n > math.MaxInt32:
			return 0, false
		case signed && neg && n > -math.MinInt32:
			return 0, false
		}
	}
	if neg {
		return -n, true
	}
	return n, true
}
