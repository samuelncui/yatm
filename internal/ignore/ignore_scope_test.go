package ignore

import (
	"math/rand"
	"path"
	"strings"
	"testing"
)

// sinkDecision keeps benchmarked and measured decisions observable.
var sinkDecision bool

const scopeRules = "*.tmp\ncache/\n**/node_modules/\nthumbs.db\n*.bak\n.metadata/\n@eaDir/\n#recycle/\n"

var scopeFiftyRules = func() string {
	var builder strings.Builder
	builder.WriteString(scopeRules)
	for index := 0; index < 42; index++ {
		builder.WriteString("ignore-pattern-" + string(rune('a'+index%26)) + "/\n")
	}
	return builder.String()
}()

// TestScopeMatchesWholePathMatch checks the directory-scoped decision against Match.
func TestScopeMatchesWholePathMatch(t *testing.T) {
	for round := 0; round < 400; round++ {
		random := rand.New(rand.NewSource(int64(round)))
		rules := referenceRuleText(random)
		matcher := Compile(rules)
		for _, value := range referencePaths(random) {
			parent, name := path.Split(value)
			parent = strings.TrimSuffix(parent, "/")
			scope := matcher.Scope(parent)
			for _, directory := range []bool{false, true} {
				expected := matcher.Match(value, directory)
				if got := scope.Ignores(name, directory); got != expected {
					t.Fatalf("Scope(%q).Ignores(%q, isDir=%t) rules=%q: got %t, want %t", parent, name, directory, rules, got, expected)
				}
				// A scope answers many entries without changing its own state.
				if got := scope.Ignores(name, directory); got != expected {
					t.Fatalf("reused scope: rules=%q path=%q isDir=%t: got %t, want %t", rules, value, directory, got, expected)
				}
			}
		}
	}
}

// TestScopeIsStableAcrossManyNames reuses one scope for a whole directory listing.
func TestScopeIsStableAcrossManyNames(t *testing.T) {
	matcher := Compile(scopeFiftyRules + "a/**/b\n/root\n")
	scope := matcher.Scope("a/x")
	for index := 0; index < 500; index++ {
		name := string(rune('a'+index%26)) + ".tmp"
		if index%7 == 0 {
			name = "ordinary-" + name
		}
		expected := matcher.Match("a/x/"+name, false)
		if got := scope.Ignores(name, false); got != expected {
			t.Fatalf("entry %d (%q): got %t, want %t", index, name, got, expected)
		}
	}
	if scope.Ignored() {
		t.Fatal("an ordinary directory must not become ignored by answering entries")
	}
}

// TestScopeIgnoredDirectoryHidesEveryChild covers the gitignore parent rule.
func TestScopeIgnoredDirectoryHidesEveryChild(t *testing.T) {
	matcher := Compile("build/\n!build/keep\n")
	if !matcher.Scope("build").Ignored() {
		t.Fatal("an ignored directory must report itself as ignored")
	}
	scope := matcher.Scope("build")
	for _, name := range []string{"a", "keep", "nested", ".hidden"} {
		for _, directory := range []bool{false, true} {
			if !scope.Ignores(name, directory) {
				t.Fatalf("child %q (isDir=%t) of an ignored directory must stay ignored", name, directory)
			}
		}
	}
	if matcher.Scope("nested").Ignored() {
		t.Fatal("an ordinary directory must not report itself as ignored")
	}
}

// TestIgnoreIsDepthIndependent is the contract macOS-style junk rules rely on.
func TestIgnoreIsDepthIndependent(t *testing.T) {
	matcher := Compile(".DS_Store\n._*\n.Spotlight-V100/\n*.tmp\n")
	for _, depth := range []int{1, 8, 32} {
		prefix := strings.TrimSuffix(strings.Repeat("photos/", depth), "/")
		for _, name := range []string{".DS_Store", "._resource", "scratch.tmp"} {
			value := prefix + "/" + name
			if !matcher.Match(value, false) {
				t.Fatalf("depth %d: %q must stay ignored", depth, value)
			}
			parent, base := path.Split(value)
			if !matcher.Scope(strings.TrimSuffix(parent, "/")).Ignores(base, false) {
				t.Fatalf("depth %d: scope must ignore %q", depth, value)
			}
		}
		if matcher.Match(prefix+"/ordinary.txt", false) {
			t.Fatalf("depth %d: an ordinary entry must stay visible", depth)
		}
	}
}

// TestScopeDecisionIsAllocationFree guards the per-entry cost.
func TestScopeDecisionIsAllocationFree(t *testing.T) {
	for _, rules := range []string{scopeRules, scopeFiftyRules, "a/**/b\n**/cache/\n/root\n"} {
		matcher := Compile(rules)
		scope := matcher.Scope("photos/2024/09/raw")
		allocations := testing.AllocsPerRun(500, func() {
			sinkDecision = scope.Ignores("IMG_0042.JPG", false)
		})
		if allocations != 0 {
			t.Fatalf("scope decision allocated %v times per entry for rules %q", allocations, rules)
		}
	}
}

// TestMatchAllocationIsBounded covers the single-path predicate used outside listings.
func TestMatchAllocationIsBounded(t *testing.T) {
	matcher := Compile(scopeFiftyRules + "a/**/b\n/root\n")
	shallow, deep := "a/file.txt", strings.Repeat("level/", 30)+"file.txt"
	shallowAllocations := testing.AllocsPerRun(500, func() { sinkDecision = matcher.Match(shallow, false) })
	deepAllocations := testing.AllocsPerRun(500, func() { sinkDecision = matcher.Match(deep, false) })
	if deepAllocations > shallowAllocations {
		t.Fatalf("Match allocations grew with depth: depth 2 = %v, depth 31 = %v", shallowAllocations, deepAllocations)
	}

	// More rules of the same shape must not add per-call work.
	small := Compile("*.tmp\ncache/\na/**/b\n")
	large := Compile(strings.Repeat("*.pattern\ncache/\n", 40) + "a/**/b\n")
	smallAllocations := testing.AllocsPerRun(500, func() { sinkDecision = small.Match(deep, false) })
	largeAllocations := testing.AllocsPerRun(500, func() { sinkDecision = large.Match(deep, false) })
	if largeAllocations > smallAllocations {
		t.Fatalf("Match allocations grew with rule count: 3 rules = %v, 81 rules = %v", smallAllocations, largeAllocations)
	}
}
