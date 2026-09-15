// Package ignore matches explicitly configured, root-relative gitignore rules.
// It never discovers .gitignore files or consults Git's tracked-file state.
package ignore

import "strings"

type rule struct {
	parts     []string
	anchored  bool
	directory bool
	include   bool
}

// Matcher is immutable and can be reused for traversal and content admission.
type Matcher struct{ rules []rule }

func Compile(text string) *Matcher {
	// Preserve the caller's raw text; normalization applies only to compiled patterns.
	value := &Matcher{}
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
		value.rules = append(value.rules, current)
	}
	return value
}

// Match enforces parent pruning even when a caller requests a leaf directly.
func (m *Matcher) Match(relative string, directory bool) bool {
	// The root is always traversed/validated, even when every child is ignored.
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

func (r rule) match(parts []string) bool {
	// A basename pattern is valid at any depth; slashed patterns start at the root.
	if !r.anchored {
		return matchComponent(r.parts[0], parts[len(parts)-1])
	}

	// A bounded state vector implements ** without exponential recursive backtracking.
	states := make([]bool, len(r.parts)+1)
	states[0] = true
	for _, part := range parts {
		closeStars(states, r.parts)
		next := make([]bool, len(states))
		for index, pattern := range r.parts {
			if !states[index] {
				continue
			}
			if pattern == "**" {
				next[index] = true
				continue
			}
			if matchComponent(pattern, part) {
				next[index+1] = true
			}
		}
		states = next
	}
	closeStars(states, r.parts)
	return states[len(r.parts)]
}

func closeStars(states []bool, parts []string) {
	for index, part := range parts {
		if states[index] && part == "**" {
			states[index+1] = true
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
