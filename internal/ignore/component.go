package ignore

import "strings"

// matchComponent uses Git's byte-oriented wildcards, not Go's rune-oriented path.Match.
// Two bounded state vectors avoid recursive wildcard backtracking.
func matchComponent(pattern, name string) bool {
	// A state records the filename prefix consumed by the pattern so far.
	states, next := make([]bool, len(name)+1), make([]bool, len(name)+1)
	states[0] = true
	for index := 0; index < len(pattern); index++ {
		// A star can consume any remaining bytes in this one component.
		if pattern[index] == '*' {
			for offset := 1; offset <= len(name); offset++ {
				states[offset] = states[offset] || states[offset-1]
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
		for offset := range next {
			next[offset] = false
		}
		for offset := 0; offset < len(name); offset++ {
			if !states[offset] {
				continue
			}
			switch kind {
			case '?':
				next[offset+1] = true
			case '[':
				next[offset+1] = accepted[name[offset]]
			default:
				next[offset+1] = name[offset] == literal
			}
		}
		states, next = next, states
	}
	return states[len(name)]
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
