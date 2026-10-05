package library

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func filenameImportJSON(legacy bool, name string) string {
	if legacy {
		return `{"files":[{"id":0,"name":"/","mode":2147484141},{"id":1,"name":` + name + `,"mode":420}]}`
	}
	return `{"type":"header","format":"yatm-library-backup","version":1,"entities":["file"]}` + "\n" +
		`{"type":"file","data":{"id":1,"name":` + name + `,"kind":1,"created_at_ns":"0","updated_at_ns":"0"}}` + "\n" + `{"type":"end"}` + "\n"
}

func TestLibraryImportRejectsLossyFilenameDecoding(t *testing.T) {
	// Go's JSON decoder would replace these inputs with U+FFFD before name validation.
	for _, legacy := range []bool{false, true} {
		for _, name := range []string{
			"\"raw\xff\"", `"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\u0041"`, `"\ud800\ud800"`,
			`"\u0000"`, `"a/b"`, `".."`, `""`,
		} {
			t.Run(fmt.Sprintf("legacy=%t/name=%q", legacy, name), func(t *testing.T) {
				// Rejection must precede any replacement of the existing catalog.
				ctx := context.Background()
				lib := newJSONLTestLibrary(t)
				require.NoError(t, lib.SaveFile(ctx, &File{ID: 99, Name: "kept"}))
				err := lib.Import(ctx, strings.NewReader(filenameImportJSON(legacy, name)), false)
				require.Error(t, err)
				var rows []fileRow
				require.NoError(t, lib.db.Find(&rows).Error)
				require.Len(t, rows, 1)
				require.Equal(t, "kept", rows[0].Name)
			})
		}
	}
}

func TestLibraryImportPreservesLiteralUTF8Names(t *testing.T) {
	// Valid escaped text, surrogate pairs and literal U+FFFD are distinct supported names.
	for _, legacy := range []bool{false, true} {
		for _, test := range []struct{ raw, want string }{
			{`"back\\slash"`, `back\slash`}, {`" leading "`, " leading "}, {`" \t\n"`, " \t\n"},
			{`"quote'\"\n照片"`, "quote'\"\n照片"}, {`"\ud83d\ude00"`, "😀"}, {`"\uD83D\uDE00"`, "😀"},
			{`"�"`, "�"}, {`"\ufffd"`, "�"}, {`"\\ud800"`, `\ud800`}, {`"\u005cud800"`, `\ud800`},
		} {
			t.Run(fmt.Sprintf("legacy=%t/name=%q", legacy, test.want), func(t *testing.T) {
				// Import through the public format detection path and retrieve the exact identity.
				ctx := context.Background()
				lib := newJSONLTestLibrary(t)
				require.NoError(t, lib.Import(ctx, strings.NewReader(filenameImportJSON(legacy, test.raw)), false))
				file, err := lib.GetFile(ctx, 1)
				require.NoError(t, err)
				require.Equal(t, test.want, file.Name)
				var count int64
				require.NoError(t, lib.db.Model(ModelFile).Count(&count).Error)
				require.EqualValues(t, 1, count, "the legacy root is not a persisted filename")
			})
		}
	}
}
