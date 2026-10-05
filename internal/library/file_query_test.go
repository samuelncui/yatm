package library

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFileQueryNumericNegation(t *testing.T) {
	// Cover both sides and endpoints of numeric ranges, including a saved-only Catalog File.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	rows := []LiveQueryRow{
		{Name: "zero", Size: 0},
		{Name: "below", Size: 50},
		{Name: "equal", Size: 100},
		{Name: "saved", Size: 150},
		{Name: "above", Size: 200},
	}
	for index := range rows {
		row := &rows[index]
		row.MtimeNS = time.Date(2026, 1, index+1, 0, 0, 0, 0, time.UTC).UnixNano()
		file := &File{Name: row.Name}
		createFileRows(t, db, file)
		row.FileID, row.LocationID = file.ID, location.ID
		if row.Name == "saved" {
			require.NoError(t, db.Create(&FileVersion{
				FileID: file.ID, Signature: []byte("saved"), Size: row.Size, MtimeNS: row.MtimeNS,
			}).Error)
			continue
		}
		require.NoError(t, db.Create(&FileLocation{
			FileID: file.ID, LocationID: location.ID, Path: row.Name, Size: row.Size, MtimeNS: row.MtimeNS,
		}).Error)
	}

	// Each NOT complements its whole operand, including nested Boolean and range expressions.
	for _, test := range []struct {
		query string
		want  []string
	}{
		{"NOT size:100", []string{"zero", "below", "saved", "above"}},
		{`NOT size:"100"`, []string{"zero", "below", "saved", "above"}},
		{"NOT size:0", []string{"below", "equal", "saved", "above"}},
		{"NOT size:[50 TO 150]", []string{"zero", "above"}},
		{"NOT (size: > 50 AND size: < 150)", []string{"zero", "below", "saved", "above"}},
		{"NOT size: > 100", []string{"zero", "below", "equal"}},
		{"NOT size: >= 100", []string{"zero", "below"}},
		{"NOT size: < 100", []string{"equal", "saved", "above"}},
		{"NOT size: <= 100", []string{"saved", "above"}},
		{"NOT (NOT size:100)", []string{"equal"}},
		{`NOT (size:"100" AND name:below)`, []string{"zero", "below", "equal", "saved", "above"}},
		{"NOT (size:100 OR name:below)", []string{"zero", "saved", "above"}},
		{"NOT (size:100 AND (name:equal OR name:above))", []string{"zero", "below", "saved", "above"}},
		{"size: >= 50 AND NOT (size:100 OR name:above)", []string{"below", "saved"}},
		{"NOT name:equal", []string{"zero", "below", "saved", "above"}},
		{`NOT mtime:["2026-01-02T00:00:00Z" TO "2026-01-04T00:00:00Z"]`, []string{"zero", "above"}},
	} {
		t.Run(test.query, func(t *testing.T) {
			// Both adapters compile the same syntax tree without changing their fact sources.
			query, err := lib.CompileFilesQuery(test.query)
			require.NoError(t, err)
			t.Run("Library", func(t *testing.T) {
				page, err := lib.ListFileQueryRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, false, query, "", 100)
				require.NoError(t, err)
				var names []string
				for _, file := range page.Files {
					names = append(names, file.Name)
				}
				require.ElementsMatch(t, test.want, names)
			})
			t.Run("Location", func(t *testing.T) {
				indexes, err := lib.MatchLiveQuery(ctx, query, rows)
				require.NoError(t, err)
				var names []string
				for _, index := range indexes {
					names = append(names, rows[index].Name)
				}
				require.ElementsMatch(t, test.want, names)
			})
		})
	}
}

func TestFileQueryNumericNegationFactsAndUnknowns(t *testing.T) {
	// The recorded 100 MB original and observed 2 GB entry intentionally disagree.
	db, lib := newTestLibrary(t)
	ctx := context.Background()
	location := locationTestSource(t, lib)
	edited, unknown := &File{Name: "edited"}, &File{Name: "unknown"}
	createFileRows(t, db, edited, unknown)
	require.NoError(t, db.Create(&FileLocation{
		FileID: edited.ID, LocationID: location.ID, Path: edited.Name, Size: 100_000_000,
	}).Error)
	rows := []LiveQueryRow{
		{FileID: edited.ID, LocationID: location.ID, Name: edited.Name, Size: 2_000_000_000},
		{LocationID: location.ID, Name: unknown.Name, Kind: entity.EntryKind_ENTRY_KIND_LINK},
	}

	// Unknown size stays NULL: negation must preserve SQL's three-valued Boolean semantics.
	for _, test := range []struct {
		query   string
		catalog []string
		live    []string
	}{
		{"NOT size:100000000", nil, []string{"edited"}},
		{"NOT size:2000000000", []string{"edited"}, nil},
		{"NOT (NOT size:100000000)", []string{"edited"}, nil},
		{"NOT (size:100000000 OR name:missing)", nil, []string{"edited"}},
		{"NOT (size:100000000 AND name:missing)", []string{"edited", "unknown"}, []string{"edited", "unknown"}},
	} {
		t.Run(test.query, func(t *testing.T) {
			// Legacy Catalog search uses the same compiler directly on recorded rows.
			t.Run("Library", func(t *testing.T) {
				page, err := lib.SearchFiles(ctx, test.query, "", 100)
				require.NoError(t, err)
				var names []string
				for _, result := range page.Results {
					names = append(names, result.File.Name)
				}
				require.ElementsMatch(t, test.catalog, names)
			})

			// An observed linked entry has no regular-file size, independently of Catalog membership.
			t.Run("Location", func(t *testing.T) {
				indexes, err := lib.MatchLiveFiles(ctx, test.query, rows)
				require.NoError(t, err)
				var names []string
				for _, index := range indexes {
					names = append(names, rows[index].Name)
				}
				require.ElementsMatch(t, test.live, names)
			})
		})
	}
}
