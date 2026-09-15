package ignore

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRules(t *testing.T) {
	// Each case includes the path type so directory rules cannot hide same-named regular files.
	cases := []struct {
		rules, path     string
		directory, want bool
	}{
		{"*.tmp", "nested/a.tmp", false, true},
		{"*.tmp", "nested/a.txt", false, false},
		{"/a.tmp", "nested/a.tmp", false, false},
		{"/a.tmp", "a.tmp", false, true},
		{"build/", "build", false, false},
		{"build/", "build", true, true},
		{"build/", "nested/build/a", false, true},
		{"build/\n!build/a", "build/a", false, true},
		{"build/*\n!build/a", "build/a", false, false},
		{"*.tmp\n!keep.tmp\nkeep.*", "keep.tmp", false, true},
		{"a/**/b", "a/b", false, true},
		{"a/**/b", "a/x/y/b", false, true},
		{"a/**/b", "x/a/b", false, false},
		{"a/**", "a", true, false},
		{"a/**", "a/x/y", false, true},
		{"**/cache/", "cache/a", false, true},
		{"**/cache/", "x/y/cache/a", false, true},
		{"file?.[ch]", "file1.c", false, true},
		{"[!a]*", "apple", false, false},
		{"[!a]*", "banana", false, true},
		{"[[:digit:]].txt", "nested/1.txt", false, true},
		{"[![:alpha:]0-9]", "-", false, true},
		{"[[:unknown:]]", "a", false, false},
		{"[]a-]", "]", false, true},
		{"[]a-]", "-", false, true},
		{"?", "é", false, false},
		{"??", "é", false, true},
		{"foo\\/bar", "foo/bar", false, true},
		{"\\#a\n\\!a\n\\*.txt", "#a", false, true},
		{"\\#a\n\\!a\n\\*.txt", "!a", false, true},
		{"\\#a\n\\!a\n\\*.txt", "*.txt", false, true},
		{"\\#a\n\\!a\n\\*.txt", "x.txt", false, false},
		{"name  ", "name", false, true},
		{"name\\ ", "name ", false, true},
		{" name", " name", false, true},
		{" name", "name", false, false},
		{"# comment\n\n", ".yatm.json", false, false},
		{"*", "", true, false},
		{"foo\\", "foo", false, false},
	}
	for _, test := range cases {
		if got := Compile(test.rules).Match(test.path, test.directory); got != test.want {
			t.Errorf("rules=%q path=%q directory=%t: got %t, want %t", test.rules, test.path, test.directory, got, test.want)
		}
	}
}

func TestLiteralSubtree(t *testing.T) {
	// Escaped syntax remains a literal name and excludes only that anchored subtree.
	for _, name := range []string{"name ", "!keep", "#comment", "a*b", "[a]", "a?b", "a\\b", "nested/file"} {
		matcher := Compile(Literal(name))
		if !matcher.Match(name, false) || !matcher.Match(name+"/child", false) || matcher.Match("other/"+name, false) {
			t.Errorf("literal %q did not preserve its anchored subtree", name)
		}
	}
}

func TestRulesAgreeWithGit(t *testing.T) {
	// Git is a test oracle only; production matching never invokes an external process.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable for Ignore conformance comparison")
	}
	root := t.TempDir()
	if output, err := exec.Command(git, "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("initialize isolated Git oracle: %s: %v", output, err)
	}
	cases := []struct {
		rules string
		paths []string
	}{
		{"*.tmp\n!keep.tmp", []string{"a.tmp", "nested/a.tmp", "keep.tmp", "nested/keep.tmp"}},
		{"/root", []string{"root", "nested/root", "root/child"}},
		{"build/\n!build/keep", []string{"build/", "build/keep", "nested/build/keep", "build"}},
		{"build/*\n!build/keep", []string{"build/", "build/keep", "build/skip", "build/skip/keep"}},
		{"a/**/b", []string{"a/b", "a/x/b", "a/x/y/b", "x/a/b"}},
		{"a/**", []string{"a/", "a/x", "a/x/y"}},
		{"**/keep", []string{"keep", "nested/keep", "nested/keep/child"}},
		{"**/keep/**", []string{"keep/", "keep/a", "nested/keep/", "nested/keep/a"}},
		{"foo\\/bar", []string{"foo/bar", "nested/foo/bar"}},
		{"foo***bar", []string{"foobar", "fooXbar", "foo/x/bar"}},
		{"[[:digit:]].txt", []string{"1.txt", "a.txt", "nested/2.txt"}},
		{"[[:alpha:]0-9].txt", []string{"1.txt", "Z.txt", "-.txt"}},
		{"[![:digit:]].txt", []string{"1.txt", "a.txt", "nested/Z.txt"}},
		{"[[:space:]]", []string{" ", "\t", "x"}},
		{"[[:punct:]]", []string{"!", "[", "^", "~", "a"}},
		{"[[:unknown:]]", []string{"a", ":", "]"}},
		{"[]a]", []string{"]", "a", "b"}},
		{"[!]]", []string{"]", "a", "["}},
		{"[^a]", []string{"^", "a", "b"}},
		{"[-a]", []string{"-", "a", "b"}},
		{"[a-]", []string{"-", "a", "b"}},
		{"[a\\-c]", []string{"-", "a", "b", "c"}},
		{"[abc", []string{"[abc", "a", "abc"}},
		{"\\!literal\n\\#literal\nname\\ \nfoo\\", []string{"!literal", "#literal", "name ", "name", "foo"}},
		{"?", []string{"é", "a", "中文"}},
		{"é*", []string{"é", "éclair", "clair"}},
	}
	for _, test := range cases {
		// Replace only this disposable oracle's root configuration, then compare each path/type.
		if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(test.rules+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		matcher := Compile(test.rules)
		for _, candidate := range test.paths {
			isDir := strings.HasSuffix(candidate, "/")
			candidate = strings.TrimSuffix(candidate, "/")
			if isDir {
				if err := os.MkdirAll(filepath.Join(root, candidate), 0700); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(git, "-C", root, "-c", "core.excludesFile=/dev/null", "-c", "core.ignoreCase=false",
				"check-ignore", "--quiet", "--no-index", "--", candidate)
			output, err := command.CombinedOutput()
			var failure *exec.ExitError
			if err != nil && (!errors.As(err, &failure) || failure.ExitCode() != 1) {
				t.Fatalf("Git oracle failed: %s: %v", output, err)
			}
			want := err == nil
			if isDir {
				if err := os.Remove(filepath.Join(root, candidate)); err != nil {
					t.Fatal(err)
				}
			}
			if got := matcher.Match(candidate, isDir); got != want {
				t.Errorf("rules=%q path=%q: got %t, Git=%t", test.rules, candidate, got, want)
			}
		}
	}
}
