package scan

import (
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestScanPreservesLiteralUTF8(t *testing.T) {
	// Observe actual mixed filenames, preserving both a backslash and a newline spelling independently.
	exe, source := setupAnalyze(t)
	ctx := context.Background()
	names := []string{"normal", `back\slash`, `literal\n`, "literal\n", " leading", "trailing ", " \t\n", `quotes'"`, "100%?#", "照片 😀"}
	for index, name := range names {
		writeAnalyzeFile(t, source, name, fmt.Sprintf("payload %d", index))
	}
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	job, err := exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)

	// Stored originals and logical names retain each byte of the enumerated identity.
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 100)
	require.NoError(t, err)
	actual := make([]string, 0, len(rows))
	ids := make(map[int64]bool)
	for _, row := range rows {
		actual = append(actual, row.Path)
		require.False(t, ids[row.FileID])
		ids[row.FileID] = true
		file, err := exe.Lib().GetFile(ctx, row.FileID)
		require.NoError(t, err)
		require.Equal(t, row.Path, file.Name)
		require.NotEmpty(t, row.Signature)
	}
	require.ElementsMatch(t, names, actual)

	// Explicit selections use the same literal path as whole-directory enumeration.
	selected, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID, `back\slash`, " \t\n"),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY})
	job, err = exe.GetJob(ctx, selected.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	var paths []string
	require.NoError(t, selected.db.Model(&Entry{}).Pluck("path", &paths).Error)
	require.ElementsMatch(t, []string{`back\slash`, " \t\n"}, paths)
}
