package fileops

import (
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestOrganizationPreservesLiteralUTF8(t *testing.T) {
	for _, logical := range []bool{false, true} {
		t.Run(fmt.Sprintf("library=%t", logical), func(t *testing.T) {
			// Both adapters receive the same mixed names, including two distinct newline spellings.
			f := organizationFixture{f: setup(t), logical: logical}
			ctx := context.Background()
			names := []string{"normal", `back\slash`, `literal\n`, "literal\n", " leading", "trailing ", " \t\n", `quotes'"`, "100%?#", "照片 😀"}
			var sources []*entity.FileOperationRef
			originals := make(map[string]*library.File)
			for _, name := range names {
				sources = append(sources, f.file(t, "source/"+name))
				if !logical {
					originals[name] = f.f.original(t, "source/"+name)
				}
			}
			f.file(t, "source/back/slash")
			f.dir(t, "target")
			f.dir(t, "folders")

			// Authored mkdir names retain whitespace and backslashes as one component.
			for _, name := range names {
				stream := f.f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR,
					Destination: f.ref(t, "folders"), Name: name}, nil)
				require.Zero(t, stream.summary().FailedCount)
				require.EqualValues(t, 1, stream.summary().SucceededCount)
				f.exists(t, "folders/"+name, true)
			}

			// Implicit basenames and explicit rename inputs preserve the same exact identity.
			moved := f.f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
				Sources: sources, Destination: f.ref(t, "target")}, nil)
			require.Zero(t, moved.summary().FailedCount)
			require.EqualValues(t, len(names), moved.summary().SucceededCount)
			for _, name := range names {
				f.exists(t, "source/"+name, false)
				f.exists(t, "target/"+name, true)
				renamed := " " + name + " "
				stream := f.f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
					Sources: []*entity.FileOperationRef{f.ref(t, "target/"+name)}, Destination: f.ref(t, "target"), Name: renamed}, nil)
				require.Zero(t, stream.summary().FailedCount)
				f.exists(t, "target/"+renamed, true)
				if !logical {
					original, err := f.f.exe.Lib().GetFileLocation(ctx, originals[name].ID)
					require.NoError(t, err)
					require.Equal(t, "target/"+renamed, original.Path)
				}
			}
			f.exists(t, "source/back/slash", true)

			// Delete still uses Trash and never targets an escaped display spelling or sibling path.
			for _, name := range names {
				value := "target/ " + name + " "
				stream := f.f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
					Sources: []*entity.FileOperationRef{f.ref(t, value)}}, nil)
				require.Zero(t, stream.summary().FailedCount)
				f.exists(t, value, false)
			}
			f.exists(t, "source/back/slash", true)
		})
	}
}
