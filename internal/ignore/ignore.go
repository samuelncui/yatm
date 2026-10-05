// Package ignore matches explicitly configured, root-relative gitignore rules.
// It never discovers .gitignore files or consults Git's tracked-file state.
//
// One rule set is compiled once into per-name lookups, so a decision costs the same at
// any path depth: no-slash rules match the entry name alone, and slashed rules advance a
// component state that each directory resolves once.
package ignore

import "strings"

type rule struct {
	parts     []string
	anchored  bool
	directory bool
	include   bool
}

// ruleRef identifies one rule in its source order; the last matching rule decides.
type ruleRef struct {
	index     int
	include   bool
	directory bool
}

type literalKind uint8

const (
	literalPrefix literalKind = iota + 1
	literalSuffix
)

// literalRef is a name pattern reducible to a prefix or a suffix test.
type literalRef struct {
	ruleRef
	kind literalKind
	text string
}

func (r literalRef) matches(value string) bool {
	if r.kind == literalPrefix {
		return strings.HasPrefix(value, r.text)
	}
	return strings.HasSuffix(value, r.text)
}

// wildcardRef is a name pattern that needs the component matcher.
type wildcardRef struct {
	ruleRef
	pattern string
}

// anchoredRef is a pattern containing a separator, matched against the rule base.
type anchoredRef struct {
	ruleRef
	parts []string
	kinds []componentKind
	words int
}

// componentKind selects the cheapest exact test for one pattern component.
type componentKind uint8

const (
	componentLiteral componentKind = iota
	componentAnyDepth
	componentPattern
)

func componentKinds(parts []string) []componentKind {
	kinds := make([]componentKind, len(parts))
	for index, part := range parts {
		switch {
		case part == "**":
			kinds[index] = componentAnyDepth
		case !strings.ContainsAny(part, "*?[\\"):
			kinds[index] = componentLiteral
		default:
			kinds[index] = componentPattern
		}
	}
	return kinds
}

// Matcher is immutable after Compile and safe for concurrent use.
type Matcher struct {
	exact      map[string][]ruleRef
	literals   []literalRef
	wildcards  []wildcardRef
	anchored   []anchoredRef
	offsets    []int
	stateWords int
}

func Compile(text string) *Matcher {
	// Preserve the caller's raw text; normalization applies only to compiled patterns.
	value := &Matcher{exact: map[string][]ruleRef{}}
	index := 0
	for _, line := range strings.Split(text, "\n") {
		line = trimTrailingSpaces(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		current := rule{}
		if strings.HasPrefix(line, "!") {
			current.include = true
			line = line[1:]
		}
		if line == "" {
			continue
		}
		line = normalizeSeparators(line)
		current.directory = strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		current.anchored = strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		current.parts = strings.Split(line, "/")
		if current.anchored && current.parts[len(current.parts)-1] == "**" {
			// A trailing /** means descendants, not the containing directory itself.
			current.parts = append(current.parts[:len(current.parts)-1], "*", "**")
		}
		reference := ruleRef{index: index, include: current.include, directory: current.directory}
		index++
		value.add(reference, current)
	}
	// Anchored rule states share one flat bitset per decision context.
	value.offsets = make([]int, len(value.anchored))
	for index, ref := range value.anchored {
		value.offsets[index] = value.stateWords
		value.stateWords += ref.words
	}
	return value
}

// add routes one parsed rule to the lookup that decides it without scanning other rules.
func (m *Matcher) add(reference ruleRef, current rule) {
	parts := current.parts
	if !current.anchored {
		m.addBasename(reference, parts[0])
		return
	}
	// A leading "**/" with one remaining component matches that name at any depth, the
	// same as the plain name pattern (gitignore: "**/foo" matches "foo" anywhere).
	if len(parts) == 2 && parts[0] == "**" {
		m.addBasename(reference, parts[1])
		return
	}
	m.anchored = append(m.anchored, anchoredRef{ruleRef: reference, parts: parts, kinds: componentKinds(parts), words: (len(parts) + 64) / 64})
}

func (m *Matcher) addBasename(reference ruleRef, pattern string) {
	switch {
	case !strings.ContainsAny(pattern, "*?[\\"):
		m.exact[pattern] = append(m.exact[pattern], reference)
	case strings.HasPrefix(pattern, "*") && !strings.ContainsAny(pattern[1:], "*?[\\"):
		m.literals = append(m.literals, literalRef{ruleRef: reference, kind: literalSuffix, text: pattern[1:]})
	case strings.HasSuffix(pattern, "*") && !strings.ContainsAny(pattern[:len(pattern)-1], "*?[\\"):
		m.literals = append(m.literals, literalRef{ruleRef: reference, kind: literalPrefix, text: pattern[:len(pattern)-1]})
	default:
		m.wildcards = append(m.wildcards, wildcardRef{ruleRef: reference, pattern: pattern})
	}
}

// Match enforces parent pruning even when a caller requests a leaf directly.
func (m *Matcher) Match(relative string, directory bool) bool {
	// The root is always traversed/validated, even when every child is ignored.
	if m == nil || relative == "" || m.empty() {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(relative, "/"), "/")
	states, next := m.newState(), m.newState()
	for index, part := range parts {
		isDir := index < len(parts)-1 || directory
		if m.level(part, isDir, states, next) {
			return true
		}
		states, next = next, states
	}
	return false
}

// Scope is the Ignore decision context of one directory: the gitignore exclude-stack
// state for that level. Create one per listing or traversal directory; it is not safe
// for concurrent use.
type Scope struct {
	matcher *Matcher
	ignored bool
	states  []uint64
	next    []uint64
	scratch []uint64
}

// Scope resolves the decision context for one directory path relative to the rule base.
func (m *Matcher) Scope(relative string) *Scope {
	directory := strings.TrimSuffix(relative, "/")
	scope := &Scope{matcher: m}
	if m == nil || m.empty() {
		// Without rules no directory or entry can be ignored; nothing needs resolving.
		return scope
	}
	scope.states, scope.next, scope.scratch = m.newState(), m.newState(), make([]uint64, m.stateWords)
	if directory == "" {
		return scope
	}
	for _, part := range strings.Split(directory, "/") {
		if m.level(part, true, scope.states, scope.next) {
			scope.ignored = true
			return scope
		}
		scope.states, scope.next = scope.next, scope.states
	}
	return scope
}

// Ignored reports whether this directory or any of its ancestors is ignored.
func (s *Scope) Ignored() bool { return s.ignored }

// Ignores reports the decision for one child name of this directory.
func (s *Scope) Ignores(name string, directory bool) bool {
	if s.ignored {
		return true
	}
	if s.matcher == nil || s.matcher.empty() {
		return false
	}
	// Evaluate the child level on a copy, so the scope stays valid for every entry.
	copy(s.scratch, s.states)
	return s.matcher.level(name, directory, s.scratch, s.next)
}

// empty reports a rule set that can never ignore anything.
func (m *Matcher) empty() bool {
	return len(m.exact) == 0 && len(m.literals) == 0 && len(m.wildcards) == 0 && len(m.anchored) == 0
}

// newState starts every anchored rule at the rule base.
func (m *Matcher) newState() []uint64 {
	state := make([]uint64, m.stateWords)
	for index := range m.anchored {
		setBit(state[m.offsets[index]:], 0)
	}
	return state
}

// level evaluates one path component and advances the anchored rule states from states
// into next. It reports whether this level is ignored by its last matching rule.
func (m *Matcher) level(name string, directory bool, states, next []uint64) bool {
	best, include := -1, false
	consider := func(reference ruleRef) {
		if reference.directory && !directory {
			return
		}
		if reference.index > best {
			best, include = reference.index, reference.include
		}
	}
	for _, reference := range m.exact[name] {
		consider(reference)
	}
	for _, reference := range m.literals {
		if reference.matches(name) {
			consider(reference.ruleRef)
		}
	}
	for _, reference := range m.wildcards {
		if matchComponent(reference.pattern, name) {
			consider(reference.ruleRef)
		}
	}
	for index := range m.anchored {
		reference := &m.anchored[index]
		offset, words := m.offsets[index], reference.words
		current, target := states[offset:offset+words], next[offset:offset+words]
		clearBits(target)
		if !anyBit(current) {
			// A rule that consumed every live path can never match again.
			continue
		}
		closeStars(current, reference.parts)
		for position, kind := range reference.kinds {
			if !bitSet(current, position) {
				continue
			}
			switch kind {
			case componentAnyDepth:
				setBit(target, position)
			case componentLiteral:
				if name == reference.parts[position] {
					setBit(target, position+1)
				}
			default:
				if matchComponent(reference.parts[position], name) {
					setBit(target, position+1)
				}
			}
		}
		// A level is accepted only after its own component is consumed.
		closeStars(target, reference.parts)
		if bitSet(target, len(reference.parts)) {
			consider(reference.ruleRef)
		}
	}
	return best >= 0 && !include
}

// Literal converts an exact relative subtree into an anchored rule without pattern reinterpretation.
func Literal(value string) string {
	var result strings.Builder
	result.WriteByte('/')
	for _, ch := range value {
		if strings.ContainsRune("\\*?[]#! ", ch) {
			result.WriteByte('\\')
		}
		result.WriteRune(ch)
	}
	return result.String()
}

func closeStars(state []uint64, parts []string) {
	for position, part := range parts {
		if part == "**" && bitSet(state, position) {
			setBit(state, position+1)
		}
	}
}

func trimTrailingSpaces(value string) string {
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

func normalizeSeparators(value string) string {
	// An escaped slash is still a separator, while escaped backslashes remain literal.
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
