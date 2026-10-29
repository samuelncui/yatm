package media

import (
	"context"
	"io/fs"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/stretchr/testify/require"
)

type testContentSource struct{}

func (testContentSource) Open(context.Context) (fs.File, error) {
	return nil, nil
}

type testContentTarget struct{}

func (testContentTarget) Open(context.Context, acp.ContentMetadata) (acp.ContentWriter, error) {
	return nil, nil
}

func TestReferencesRequireExactlyOneVariant(t *testing.T) {
	// The native and endpoint variants are mutually exclusive at resolution.
	for _, test := range []struct {
		name   string
		source SourceRef
		target TargetRef
		valid  bool
	}{
		{name: "absent"},
		{name: "native", source: SourceRef{Path: "/source"}, target: TargetRef{Path: "/target"}, valid: true},
		{name: "endpoint", source: SourceRef{Content: testContentSource{}}, target: TargetRef{Content: testContentTarget{}}, valid: true},
		{name: "ambiguous", source: SourceRef{Path: "/source", Content: testContentSource{}}, target: TargetRef{Path: "/target", Content: testContentTarget{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.valid, test.source.Validate() == nil)
			require.Equal(t, test.valid, test.target.Validate() == nil)
		})
	}
}
