package treeops

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelativeNamesPreserveLiteralUTF8(t *testing.T) {
	for _, name := range []string{`back\slash`, `literal\n`, "line\nbreak", " leading", "trailing ", " \t\n", `quotes'"`, "照片 😀"} {
		t.Run(name, func(t *testing.T) {
			names, err := relativeNames("./"+name+"//child/", false)
			require.NoError(t, err)
			require.Equal(t, []string{name, "child"}, names)
			names, err = relativeNames(name, false)
			require.NoError(t, err)
			require.Equal(t, []string{name}, names)
		})
	}
}

func TestRelativeNamesRejectInvalidIdentity(t *testing.T) {
	for _, value := range []string{"../escape", "a/../b", "/absolute", "nul\x00byte", "invalid\xff"} {
		t.Run(value, func(t *testing.T) {
			_, err := relativeNames(value, true)
			require.Error(t, err)
		})
	}
}
