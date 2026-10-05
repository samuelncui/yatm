package ignore

import (
	"math/bits"
	"strings"
)

// componentWords bounds the inline state vector to names shorter than 256 bytes, which
// covers every filesystem name this matcher sees; longer names take the heap fallback.
const componentWords = 4

// bitVector keeps component matching allocation-free for ordinary names.
type bitVector struct {
	inline [componentWords]uint64
	heap   []uint64
}

func newBitVector(length int) bitVector {
	if length <= componentWords*64 {
		return bitVector{}
	}
	return bitVector{heap: make([]uint64, (length+63)/64)}
}

func (v *bitVector) words() []uint64 {
	if v.heap != nil {
		return v.heap
	}
	return v.inline[:]
}

// matchComponent uses Git's byte-oriented wildcards, not Go's rune-oriented path.Match.
// Two bounded state vectors avoid recursive wildcard backtracking.
func matchComponent(pattern, name string) bool {
	// A state records the filename prefix consumed by the pattern so far.
	states, next := newBitVector(len(name)+1), newBitVector(len(name)+1)
	current, pending := states.words(), next.words()
	setBit(current, 0)
	for index := 0; index < len(pattern); index++ {
		// A star can consume any remaining bytes in this one component, so the lowest
		// reachable prefix fills every later prefix.
		if pattern[index] == '*' {
			if lowest := lowestBit(current); lowest >= 0 {
				fillBits(current, lowest, len(name))
			}
			continue
		}

		// Parse one literal, single-byte wildcard or bracket expression.
		kind, literal := pattern[index], pattern[index]
		var accepted [256]bool
		switch kind {
		case '\\':
			index++
			if index == len(pattern) {
				return false
			}
			literal = pattern[index]
		case '[':
			var valid bool
			accepted, index, valid = bracket(pattern, index+1)
			if !valid {
				return false
			}
		}

		// Advance only prefixes whose next byte satisfies this token.
		clearBits(pending)
		for offset := 0; offset < len(name); offset++ {
			if !bitSet(current, offset) {
				continue
			}
			switch kind {
			case '?':
				setBit(pending, offset+1)
			case '[':
				if accepted[name[offset]] {
					setBit(pending, offset+1)
				}
			default:
				if name[offset] == literal {
					setBit(pending, offset+1)
				}
			}
		}
		current, pending = pending, current
	}
	return bitSet(current, len(name))
}

func bitSet(words []uint64, position int) bool {
	return words[position/64]&(uint64(1)<<uint(position%64)) != 0
}

func setBit(words []uint64, position int) {
	words[position/64] |= uint64(1) << uint(position%64)
}

func clearBits(words []uint64) {
	for index := range words {
		words[index] = 0
	}
}

func anyBit(words []uint64) bool {
	for _, word := range words {
		if word != 0 {
			return true
		}
	}
	return false
}

func lowestBit(words []uint64) int {
	for index, word := range words {
		if word == 0 {
			continue
		}
		return index*64 + bits.TrailingZeros64(word)
	}
	return -1
}

// fillBits sets every bit from start through end inclusive.
func fillBits(words []uint64, start, end int) {
	first, last := start/64, end/64
	if first == last {
		words[first] |= bitRange(start%64, end%64)
		return
	}
	words[first] |= ^uint64(0) << uint(start%64)
	for index := first + 1; index < last; index++ {
		words[index] = ^uint64(0)
	}
	words[last] |= ^uint64(0) >> uint(63-end%64)
}

func bitRange(low, high int) uint64 {
	return (^uint64(0) >> uint(63-high)) & (^uint64(0) << uint(low))
}

// POSIX classes use the same ASCII ranges as Git wildmatch, independent of process locale.
// Each pair of bytes denotes an inclusive range.
var classRanges = map[string]string{
	"alnum": "09AZaz", "alpha": "AZaz", "blank": "\t\t  ", "cntrl": "\x00\x1f\x7f\x7f",
	"digit": "09", "graph": "!~", "lower": "az", "print": " ~", "punct": "!/:@[`{~",
	"space": "\t\r  ", "upper": "AZ", "xdigit": "09AFaf",
}

func bracket(pattern string, start int) ([256]bool, int, bool) {
	// A leading closing bracket or hyphen is a member, not a terminator/range operator.
	var accepted [256]bool
	negate := start < len(pattern) && (pattern[start] == '!' || pattern[start] == '^')
	if negate {
		start++
	}
	var previous byte
	hasPrevious := false
	for index := start; index < len(pattern); index++ {
		current := pattern[index]
		if current == ']' && index > start {
			if negate {
				for value := range accepted {
					accepted[value] = !accepted[value]
				}
			}
			return accepted, index, true
		}

		// Expand named classes without treating their inner closing bracket as the outer end.
		if current == '[' && strings.HasPrefix(pattern[index:], "[:") {
			end := strings.Index(pattern[index+2:], ":]")
			if end < 0 {
				return accepted, 0, false
			}
			ranges, found := classRanges[pattern[index+2:index+2+end]]
			if !found {
				return accepted, 0, false
			}
			for pair := 0; pair < len(ranges); pair += 2 {
				for value := int(ranges[pair]); value <= int(ranges[pair+1]); value++ {
					accepted[value] = true
				}
			}
			index += end + 3
			hasPrevious = false
			continue
		}

		// An unescaped middle hyphen extends the preceding member into a range.
		if current == '-' && hasPrevious && index+1 < len(pattern) && pattern[index+1] != ']' {
			index++
			if pattern[index] == '\\' {
				index++
				if index == len(pattern) {
					return accepted, 0, false
				}
			}
			for value := int(previous); value <= int(pattern[index]); value++ {
				accepted[value] = true
			}
			hasPrevious = false
			continue
		}

		// Escaped punctuation is a literal member, including a hyphen or closing bracket.
		if current == '\\' {
			index++
			if index == len(pattern) {
				return accepted, 0, false
			}
			current = pattern[index]
		}
		accepted[current] = true
		previous, hasPrevious = current, true
	}
	return accepted, 0, false
}
