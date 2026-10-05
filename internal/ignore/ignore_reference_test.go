package ignore

// Frozen reference implementation, copied verbatim from the matcher that the compiled
// design replaces. It exists only to prove that the rewrite preserves behavior, so it is
// deliberately duplicated here instead of shared with the implementation.

import (
	"math/rand"
	"path"
	"strings"
	"testing"
)

type referenceRule struct {
	parts     []string
	anchored  bool
	directory bool
	include   bool
}

type referenceMatcher struct{ rules []referenceRule }

func referenceCompile(text string) *referenceMatcher {
	value := &referenceMatcher{}
	for _, line := range strings.Split(text, "\n") {
		line = referenceTrimTrailingSpaces(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		current := referenceRule{}
		if strings.HasPrefix(line, "!") {
			current.include = true
			line = line[1:]
		}
		if line == "" {
			continue
		}
		line = referenceNormalizeSeparators(line)
		current.directory = strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		current.anchored = strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		current.parts = strings.Split(line, "/")
		if current.anchored && current.parts[len(current.parts)-1] == "**" {
			current.parts = append(current.parts[:len(current.parts)-1], "*", "**")
		}
		value.rules = append(value.rules, current)
	}
	return value
}

func (m *referenceMatcher) Match(relative string, directory bool) bool {
	if m == nil || relative == "" {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(relative, "/"), "/")
	for end := 1; end <= len(parts); end++ {
		isDir := end < len(parts) || directory
		ignored := false
		for _, rule := range m.rules {
			if rule.directory && !isDir {
				continue
			}
			if rule.match(parts[:end]) {
				ignored = !rule.include
			}
		}
		if ignored {
			return true
		}
	}
	return false
}

func (r referenceRule) match(parts []string) bool {
	if !r.anchored {
		return referenceMatchComponent(r.parts[0], parts[len(parts)-1])
	}
	states := make([]bool, len(r.parts)+1)
	states[0] = true
	for _, part := range parts {
		referenceCloseStars(states, r.parts)
		next := make([]bool, len(states))
		for index, pattern := range r.parts {
			if !states[index] {
				continue
			}
			if pattern == "**" {
				next[index] = true
				continue
			}
			if referenceMatchComponent(pattern, part) {
				next[index+1] = true
			}
		}
		states = next
	}
	referenceCloseStars(states, r.parts)
	return states[len(r.parts)]
}

func referenceCloseStars(states []bool, parts []string) {
	for index, part := range parts {
		if states[index] && part == "**" {
			states[index+1] = true
		}
	}
}

func referenceTrimTrailingSpaces(value string) string {
	for strings.HasSuffix(value, " ") {
		escapes := 0
		for i := len(value) - 2; i >= 0 && value[i] == '\\'; i-- {
			escapes++
		}
		if escapes%2 == 1 {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}

func referenceNormalizeSeparators(value string) string {
	var normalized strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
			if value[i] != '/' {
				normalized.WriteByte('\\')
			}
		}
		normalized.WriteByte(value[i])
	}
	return normalized.String()
}

func referenceMatchComponent(pattern, name string) bool {
	states, next := make([]bool, len(name)+1), make([]bool, len(name)+1)
	states[0] = true
	for index := 0; index < len(pattern); index++ {
		if pattern[index] == '*' {
			for offset := 1; offset <= len(name); offset++ {
				states[offset] = states[offset] || states[offset-1]
			}
			continue
		}
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
			accepted, index, valid = referenceBracket(pattern, index+1)
			if !valid {
				return false
			}
		}
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

var referenceClassRanges = map[string]string{
	"alnum": "09AZaz", "alpha": "AZaz", "blank": "\t\t  ", "cntrl": "\x00\x1f\x7f\x7f",
	"digit": "09", "graph": "!~", "lower": "az", "print": " ~", "punct": "!/:@[`{~",
	"space": "\t\r  ", "upper": "AZ", "xdigit": "09AFaf",
}

func referenceBracket(pattern string, start int) ([256]bool, int, bool) {
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
		if current == '[' && strings.HasPrefix(pattern[index:], "[:") {
			end := strings.Index(pattern[index+2:], ":]")
			if end < 0 {
				return accepted, 0, false
			}
			ranges, found := referenceClassRanges[pattern[index+2:index+2+end]]
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

var referencePatternPieces = []string{
	"*.tmp", "!keep.tmp", "build/", "!build/a", "**/cache/", "abc/**", "a/**/b",
	"/root", "sub/one", "anywhere", "[!a]*", "file?.[ch]", "name\\ ", "\\#h",
	"**", "d1/", "!d1/keep", "*", "*.log", "!important.log", "sub/**",
	"cache/", "!cache/keep", "?", "[[:digit:]].txt", "[a\\-c]", "[z-a]", "[abc",
	"[^b]*", "a**b", "foo***bar", "x/y", "!x/y", "deep/**/leaf", "*.TMP", "./dot",
	"trailing  ", "escaped\\ ", "\\!bang", "\\*literal",
}

var referenceNamePieces = []string{
	"a", "b", "keep.tmp", "x.tmp", "build", "cache", "abc", "root", "sub", "one",
	"anywhere", "file1.c", "name", "#h", "d1", "keep", "important.log", "a.log",
	".DS_Store", ".hidden", "deep", "leaf", "x", "y", "dot", "bang", "*literal",
	"trailing", "name ", "TMP", "éclair",
}

func referenceRuleText(random *rand.Rand) string {
	var builder strings.Builder
	for count := 1 + random.Intn(6); count > 0; count-- {
		builder.WriteString(referencePatternPieces[random.Intn(len(referencePatternPieces))])
		builder.WriteString("\n")
	}
	if random.Intn(6) == 0 {
		builder.WriteString("# comment\n\n")
	}
	return builder.String()
}

func referencePaths(random *rand.Rand) []string {
	result := make([]string, 0, 24)
	for count := 20 + random.Intn(20); count > 0; count-- {
		depth := 1 + random.Intn(4)
		if random.Intn(10) == 0 {
			depth = 12 + random.Intn(21)
		}
		parts := make([]string, 0, depth)
		for level := 0; level < depth; level++ {
			parts = append(parts, referenceNamePieces[random.Intn(len(referenceNamePieces))])
		}
		result = append(result, path.Join(parts...))
	}
	return result
}

// TestCompiledMatcherMatchesReference fuzzes the implementation against the frozen matcher.
func TestCompiledMatcherMatchesReference(t *testing.T) {
	random := rand.New(rand.NewSource(20260918))
	for round := 0; round < 1500; round++ {
		rules := referenceRuleText(random)
		reference := referenceCompile(rules)
		compiled := Compile(rules)
		for _, value := range referencePaths(random) {
			for _, directory := range []bool{false, true} {
				expected := reference.Match(value, directory)
				if got := compiled.Match(value, directory); got != expected {
					t.Fatalf("Match(%q, isDir=%t) rules=%q: got %t, reference %t", value, directory, rules, got, expected)
				}
				parent, name := path.Split(value)
				parent = strings.TrimSuffix(parent, "/")
				if got := compiled.Scope(parent).Ignores(name, directory); got != expected {
					t.Fatalf("Scope(%q).Ignores(%q, isDir=%t) rules=%q: got %t, reference %t", parent, name, directory, rules, got, expected)
				}
			}
		}
	}
}
