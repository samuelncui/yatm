package ignore

// Benchmarks for the compiled matcher. The frozen reference implementation is timed
// alongside the compiled one so the before/after comparison stays reproducible in-repo.
//
// Measured on Apple M3 Pro with -benchtime 20000x (2026-09-18):
//   BenchmarkMatch/rules-50      depth 01:  38ns / 1 alloc  vs reference    253ns / 1 alloc
//                                depth 04: 101ns / 1 alloc  vs reference 13 424ns / 7 allocs
//                                depth 16: 388ns / 1 alloc  vs reference 73 446ns / 121 allocs
//                                depth 32: 737ns / 1 alloc  vs reference 170 908ns / 497 allocs
//   BenchmarkScopeDecision       depth 01-32: 35-40ns / 0 allocs (rules-8 and rules-50)
//                                no-rules: 4ns / 0 allocs, because an empty rule set
//                                returns before resolving anything
//   BenchmarkMatch/no-rules      depth 01-32: 4-7ns / 0 allocs, because an empty rule set
//                                returns before splitting the path (reference: 55-787ns)
//   BenchmarkScopeConstruction   per directory: 105ns at depth 4, 781ns at depth 32

import (
	"fmt"
	"strings"
	"testing"
)

func benchmarkPath(depth int) string {
	parts := make([]string, 0, depth)
	for index := 0; index+1 < depth; index++ {
		parts = append(parts, fmt.Sprintf("d%02d", index))
	}
	parts = append(parts, "file-00042.txt")
	return strings.Join(parts, "/")
}

func benchmarkRuleSets() []struct{ name, rules string } {
	return []struct{ name, rules string }{
		{"no-rules", ""},
		{"rules-8", scopeRules},
		{"rules-50", scopeFiftyRules},
		{"rules-50-anchored", scopeFiftyRules + "a/**/b\n/root\n**/cache/\nsub/**\n"},
	}
}

// BenchmarkMatch times one whole-path decision.
func BenchmarkMatch(b *testing.B) {
	for _, set := range benchmarkRuleSets() {
		matcher := Compile(set.rules)
		reference := referenceCompile(set.rules)
		for _, depth := range []int{1, 4, 16, 32} {
			value := benchmarkPath(depth)
			b.Run(fmt.Sprintf("compiled/%s/depth-%02d", set.name, depth), func(b *testing.B) {
				b.ReportAllocs()
				for index := 0; index < b.N; index++ {
					sinkDecision = matcher.Match(value, false)
				}
			})
			b.Run(fmt.Sprintf("reference/%s/depth-%02d", set.name, depth), func(b *testing.B) {
				b.ReportAllocs()
				for index := 0; index < b.N; index++ {
					sinkDecision = reference.Match(value, false)
				}
			})
		}
	}
}

// BenchmarkScopeDecision times one enumerated entry inside an already resolved directory.
func BenchmarkScopeDecision(b *testing.B) {
	for _, set := range benchmarkRuleSets() {
		matcher := Compile(set.rules)
		for _, depth := range []int{1, 4, 16, 32} {
			value := benchmarkPath(depth)
			name := "file-00042.txt"
			directory := ""
			if depth > 1 {
				directory = value[:strings.LastIndex(value, "/")]
			}
			scope := matcher.Scope(directory)
			b.Run(fmt.Sprintf("%s/depth-%02d", set.name, depth), func(b *testing.B) {
				b.ReportAllocs()
				for index := 0; index < b.N; index++ {
					sinkDecision = scope.Ignores(name, false)
				}
			})
		}
	}
}

// BenchmarkScopeConstruction times the once-per-directory resolution.
func BenchmarkScopeConstruction(b *testing.B) {
	for _, set := range benchmarkRuleSets() {
		matcher := Compile(set.rules)
		for _, depth := range []int{4, 32} {
			directory := strings.TrimSuffix(benchmarkPath(depth), "/file-00042.txt")
			b.Run(fmt.Sprintf("%s/depth-%02d", set.name, depth), func(b *testing.B) {
				b.ReportAllocs()
				for index := 0; index < b.N; index++ {
					sinkScope = matcher.Scope(directory)
				}
			})
		}
	}
}

var sinkScope *Scope
