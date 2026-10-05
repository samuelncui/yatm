package ignore

// Git conformance corpus. Git is a test oracle only; production matching never
// invokes an external process and no Git source is copied into this package.

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type conformanceCase struct {
	path  string
	isDir bool
}

type conformanceSet struct {
	name  string
	rules string
	cases []conformanceCase
}

func files(paths ...string) []conformanceCase {
	cases := make([]conformanceCase, 0, len(paths))
	for _, value := range paths {
		cases = append(cases, conformanceCase{path: value})
	}
	return cases
}

func directories(paths ...string) []conformanceCase {
	cases := make([]conformanceCase, 0, len(paths))
	for _, value := range paths {
		cases = append(cases, conformanceCase{path: value, isDir: true})
	}
	return cases
}

func merge(groups ...[]conformanceCase) []conformanceCase {
	var cases []conformanceCase
	for _, group := range groups {
		cases = append(cases, group...)
	}
	return cases
}

// TestIgnoreConformanceAgainstGit compares every corpus case with git check-ignore.
func TestIgnoreConformanceAgainstGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable for Ignore conformance comparison")
	}
	ignoredTotal, keptTotal := 0, 0
	for _, set := range conformanceSets() {
		set := set
		t.Run(set.name, func(t *testing.T) {
			root := t.TempDir()
			initializeRepository(t, git, root, set.rules)
			cases := materializeCases(t, root, set.cases)
			if len(cases) == 0 {
				t.Fatal("corpus set has no usable cases")
			}
			want := gitDecisions(t, git, root, cases)
			matcher := Compile(set.rules)
			for _, item := range cases {
				expected := want[item.path]
				if expected {
					ignoredTotal++
				} else {
					keptTotal++
				}
				if got := matcher.Match(item.path, item.isDir); got != expected {
					t.Errorf("Match(%q, isDir=%t) = %t, git = %t\n%s", item.path, item.isDir, got, expected, gitDiagnostic(t, git, root, item.path))
					continue
				}
				directory, name := path.Split(item.path)
				directory = strings.TrimSuffix(directory, "/")
				if got := matcher.Scope(directory).Ignores(name, item.isDir); got != expected {
					t.Errorf("Scope(%q).Ignores(%q, isDir=%t) = %t, git = %t\n%s",
						directory, name, item.isDir, got, expected, gitDiagnostic(t, git, root, item.path))
				}
			}
		})
	}
	if ignoredTotal < 100 || keptTotal < 100 {
		t.Fatalf("corpus is not exercising both decisions: ignored=%d kept=%d", ignoredTotal, keptTotal)
	}
	t.Logf("conformance corpus decisions: ignored=%d kept=%d", ignoredTotal, keptTotal)
}

// TestGitOracleReportsBothDecisions checks the oracle plumbing itself.
func TestGitOracleReportsBothDecisions(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable for Ignore conformance comparison")
	}
	root := t.TempDir()
	initializeRepository(t, git, root, "*.tmp\n")
	cases := materializeCases(t, root, files("a.tmp", "a.txt"))
	decisions := gitDecisions(t, git, root, cases)
	if !decisions["a.tmp"] || decisions["a.txt"] {
		t.Fatalf("git oracle returned %v", decisions)
	}
}

func initializeRepository(t *testing.T, git, root, rules string) {
	t.Helper()
	if output, err := exec.Command(git, "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("initialize isolated Git oracle: %s: %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
}

// materializeCases creates one consistent tree and returns the cases it can represent.
// A path cannot be both a regular file and a directory, and a regular file cannot be an ancestor.
func materializeCases(t *testing.T, root string, cases []conformanceCase) []conformanceCase {
	t.Helper()
	kinds := map[string]bool{}
	for _, item := range cases {
		if existing, ok := kinds[item.path]; ok && existing != item.isDir {
			t.Fatalf("corpus lists %q as both a file and a directory", item.path)
		}
		kinds[item.path] = item.isDir
	}
	ordered := append([]conformanceCase{}, cases...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].isDir != ordered[j].isDir {
			return ordered[i].isDir
		}
		return strings.Count(ordered[i].path, "/") < strings.Count(ordered[j].path, "/")
	})
	// Reject a regular file that another case needs as a directory, instead of failing at mkdir.
	for _, item := range ordered {
		if item.isDir {
			continue
		}
		for _, other := range ordered {
			if strings.HasPrefix(other.path, item.path+"/") {
				t.Fatalf("corpus lists %q as a regular file but %q needs it as a directory", item.path, other.path)
			}
		}
	}
	effective := make([]conformanceCase, 0, len(ordered))
	seen := map[string]bool{}
	for _, item := range ordered {
		if item.path == "" || seen[item.path] {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(item.path))
		if item.isDir {
			if err := os.MkdirAll(full, 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("content"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		seen[item.path] = true
		effective = append(effective, item)
	}
	return effective
}

// gitDecisions returns one decision per case, batched through one Git process.
// Git reports matching paths only and exits 1 when none match.
func gitDecisions(t *testing.T, git, root string, cases []conformanceCase) map[string]bool {
	t.Helper()
	var input bytes.Buffer
	for _, item := range cases {
		input.WriteString(item.path)
		input.WriteByte(0)
	}
	command := exec.Command(git, "-C", root,
		"-c", "core.excludesFile=/dev/null", "-c", "core.ignoreCase=false",
		"check-ignore", "--stdin", "-z", "--no-index")
	command.Stdin = bytes.NewReader(input.Bytes())
	output, err := command.Output()
	if err != nil {
		var failure *exec.ExitError
		if !errors.As(err, &failure) || failure.ExitCode() != 1 {
			t.Fatalf("Git oracle failed: %v", err)
		}
	}
	ignored := map[string]bool{}
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		ignored[string(record)] = true
	}
	decisions := make(map[string]bool, len(cases))
	for _, item := range cases {
		decisions[item.path] = ignored[item.path]
	}
	return decisions
}

// gitDiagnostic reports which pattern Git applied, for divergence reports.
func gitDiagnostic(t *testing.T, git, root, value string) string {
	t.Helper()
	command := exec.Command(git, "-C", root,
		"-c", "core.excludesFile=/dev/null", "-c", "core.ignoreCase=false",
		"check-ignore", "--stdin", "-z", "-v", "-n", "--no-index")
	command.Stdin = strings.NewReader(value + "\x00")
	output, err := command.Output()
	if err != nil {
		return fmt.Sprintf("git diagnostic unavailable: %v", err)
	}
	fields := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	return fmt.Sprintf("git -v -n: %q", fields)
}

func conformanceSets() []conformanceSet {
	sets := curatedConformanceSets()
	sets = append(sets, generatedConformanceSets()...)
	return sets
}

func curatedConformanceSets() []conformanceSet {
	return []conformanceSet{
		{
			name:  "comments-blanks-escaped-markers",
			rules: "# comment\n\n\\#hash\n\\!bang\nname  \nname\\ \nplain\n",
			cases: merge(files("#hash", "!bang", "name", "name ", "plain", "other", "nested/#hash", "nested/name"),
				directories("dir")),
		},
		{
			name:  "negation-last-match-wins",
			rules: "*.tmp\n!keep.tmp\nkeep.*\n",
			cases: merge(files("a.tmp", "keep.tmp", "keep.txt", "nested/keep.tmp", "nested/a.tmp"), directories("dir")),
		},
		{
			name:  "negation-before-ignore",
			rules: "!*.log\n*.log\n",
			cases: files("a.log", "nested/a.log", "a.txt"),
		},
		{
			name:  "negation-after-ignore",
			rules: "*.log\n!*.log\n",
			cases: files("a.log", "nested/a.log"),
		},
		{
			name:  "anchored-and-anywhere",
			rules: "/root.txt\nsub/anchored.txt\nanywhere.txt\n",
			cases: merge(files("root.txt", "anywhere.txt", "sub/anchored.txt", "sub/root.txt", "deep/sub/anchored.txt", "deep/anywhere.txt", "deep/root.txt"),
				directories("sub", "deep/sub")),
		},
		{
			name:  "trailing-slash-directory-only",
			rules: "build/\n",
			cases: merge(directories("build", "nested/build"), files("nested/build/output.txt", "build.txt")),
		},
		{
			name:  "trailing-slash-does-not-match-file",
			rules: "build/\n",
			cases: files("build", "nested/build"),
		},
		{
			name:  "excluded-parent-cannot-be-reopened",
			rules: "build/\n!build/a\n",
			cases: merge(directories("build"), files("build/a", "build/b", "nested/build/a")),
		},
		{
			name:  "content-rule-can-reopen-child",
			rules: "cache/**\n!cache/keep\n",
			cases: merge(directories("cache"), files("cache/keep", "cache/drop", "nested/cache/keep")),
		},
		{
			name:  "wildcard-and-class-shapes",
			rules: "file?.[ch]\n[!a]*\n[[:digit:]].txt\n[]a-]\n[a\\-c]\n[abc\n[[:unknown:]]\n",
			cases: files("file1.c", "fileX.h", "file12.c", "apple", "banana", "1.txt", "a.txt", "]", "-", "a", "b", "c", "[abc", "abc"),
		},
		{
			name:  "reversed-range",
			rules: "[z-a]\n",
			cases: files("a", "m", "z", "az"),
		},
		{
			name:  "double-star-anywhere",
			rules: "**/cache/\n",
			cases: merge(directories("cache", "x/cache", "x/y/cache"), files("cache/file", "x/cache/file", "cached")),
		},
		{
			name:  "double-star-trailing",
			rules: "abc/**\n",
			cases: merge(directories("abc", "abc/x"), files("abc/x/file", "abc/x/y/file", "abcd/file")),
		},
		{
			name:  "double-star-middle",
			rules: "a/**/b\n",
			cases: merge(files("a/b", "a/x/b", "a/x/y/b", "x/a/b", "ab"), directories("a", "a/x")),
		},
		{
			name:  "double-star-inside-component",
			rules: "foo***bar\na**b\n",
			cases: files("foobar", "fooXbar", "a/b", "ab", "axb"),
		},
		{
			name:  "double-star-alone",
			rules: "**\n",
			cases: merge(files("anything", "x/y"), directories("dir", "dir/sub")),
		},
		{
			name:  "double-star-directory-only",
			rules: "**/\n",
			cases: merge(directories("dir", "dir/sub"), files("file", "dir/file")),
		},
		{
			name:  "literal-escaped-metacharacters",
			rules: "\\*.txt\n\\[a\\]\nfoo\\\n",
			cases: files("*.txt", "x.txt", "[a]", "a", "foo"),
		},
		{
			name:  "case-sensitivity",
			rules: "*.TMP\n",
			cases: files("a.tmp", "a.TMP", "nested/a.TMP"),
		},
		{
			name:  "deep-paths-anywhere-rule",
			rules: ".DS_Store\n._*\n.Spotlight-V100/\n*.tmp\n",
			cases: merge(
				files("one/.DS_Store", "one/two/three/.DS_Store", "one/two/three/four/five/._resource", "one/two/three/four/five/six/x.tmp", "one/keep.txt"),
				directories("one/two/three/four/five/six/.Spotlight-V100"),
			),
		},
		{
			name:  "blank-only-rule-text",
			rules: "# nothing here\n\n",
			cases: files(".yatm.json", "file", "nested/file"),
		},
		{
			name: "posix-classes",
			rules: "[[:alpha:]]\n[[:digit:]]\n[[:alnum:]]\n[[:upper:]]\n[[:lower:]]\n" +
				"[[:space:]]\n[[:blank:]]\n[[:punct:]]\n[[:cntrl:]]\n[[:graph:]]\n[[:print:]]\n[[:xdigit:]]\n",
			cases: files("a", "Z", "5", "f", "G", "-", "_", " ", "\t", "\x01", "~", "é"),
		},
		{
			name:  "negated-class-forms",
			rules: "[!a]*\n[^b]*\n",
			cases: files("apple", "banana", "cherry", "ab"),
		},
		{
			name:  "dot-prefixed-pattern",
			rules: "./foo\n",
			cases: files("foo", "sub/foo"),
		},
		{
			name:  "leading-slash-double-star",
			rules: "/**/foo\n",
			cases: files("foo", "sub/foo", "sub/deep/foo"),
		},
		{
			name:  "consecutive-separators-in-pattern",
			rules: "a//b\n",
			cases: merge(directories("a/b"), files("a/b/c", "a/bc")),
		},
		{
			name:  "unicode-pattern",
			rules: "é*\n",
			cases: files("é", "éclair", "eclair"),
		},
		{
			name:  "pattern-with-non-utf8-bytes",
			rules: "bad\xff*\n",
			cases: files("badA", "bad", "other"),
		},
		{
			name:  "very-long-anchored-pattern",
			rules: "/" + strings.Repeat("x/", 300) + "file\n*.tmp\n",
			cases: files(strings.Repeat("x/", 300)+"file", strings.Repeat("x/", 300)+"other", strings.Repeat("x/", 300)+"deep.tmp"),
		},
	}
}

var conformancePatterns = []string{
	"*.tmp", "!keep.tmp", "build/", "!build/a", "**/cache/", "abc/**", "a/**/b",
	"/root", "sub/one", "anywhere", "[!a]*", "file?.[ch]", "name\\ ", "\\#h",
	"**", "d1/", "!d1/keep", "*", "*.log", "!important.log", "sub/**",
}

var conformanceNames = []string{
	"a", "b", "keep.tmp", "x.tmp", "build", "cache", "abc", "root", "sub", "one",
	"anywhere", "file1.c", "name", "#h", "d1", "keep", "important.log", "a.log",
}

// generatedConformanceSets combines pattern and path shapes deterministically.
func generatedConformanceSets() []conformanceSet {
	random := rand.New(rand.NewSource(20260918))
	sets := make([]conformanceSet, 0, 48)
	for index := 0; index < 48; index++ {
		lines := make([]string, 0, 6)
		for count := 1 + random.Intn(4); count > 0; count-- {
			lines = append(lines, conformancePatterns[random.Intn(len(conformancePatterns))])
		}
		sets = append(sets, conformanceSet{
			name:  fmt.Sprintf("generated-%02d", index),
			rules: strings.Join(lines, "\n") + "\n",
			cases: generatedCases(random),
		})
	}
	return sets
}

// generatedCases builds one consistent tree plus a deep chain.
func generatedCases(random *rand.Rand) []conformanceCase {
	cases := make([]conformanceCase, 0, 32)
	seen := map[string]bool{}
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		if depth > 3 {
			return
		}
		// One kind per name and level, so a path can never be both a file and a directory.
		kinds := map[string]bool{}
		for count := 1 + random.Intn(3); count > 0; count-- {
			name := conformanceNames[random.Intn(len(conformanceNames))]
			isDir, chosen := kinds[name]
			if !chosen {
				isDir = random.Intn(2) == 0
				kinds[name] = isDir
			}
			current := path.Join(prefix, name)
			if seen[current] {
				continue
			}
			seen[current] = true
			cases = append(cases, conformanceCase{path: current, isDir: isDir})
			if isDir {
				walk(current, depth+1)
			}
		}
	}
	walk("", 1)

	// Deep chains prove that matching does not depend on path depth.
	depth := 8 + random.Intn(24)
	deep := ""
	for level := 0; level < depth; level++ {
		deep = path.Join(deep, fmt.Sprintf("level%02d", level))
		cases = append(cases, conformanceCase{path: deep, isDir: true})
	}
	cases = append(cases, conformanceCase{path: path.Join(deep, conformanceNames[random.Intn(len(conformanceNames))])})
	cases = append(cases, conformanceCase{path: path.Join(deep, ".DS_Store")})
	return cases
}
