package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-multierror"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestScanReadFailureRetainsResultWriteFailure(t *testing.T) {
	for _, test := range []struct {
		name          string
		source, write bool
	}{{"source only", true, false}, {"write only", false, true}, {"source and write", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			// A real ACP endpoint either fails Open or supplies one byte to the asynchronous result writer.
			r := newProgressRunner(t)
			entry := &Entry{Path: "member", NeedsHash: true, Finding: entity.ScanFinding_SCAN_FINDING_NOT_CHECKED}
			require.NoError(t, r.db.Create(entry).Error)
			path := filepath.Join(t.TempDir(), "metadata")
			require.NoError(t, os.WriteFile(path, []byte("x"), 0600))
			info, err := os.Stat(path)
			require.NoError(t, err)
			source := &scanEndpointSource{content: []byte("x"), info: info}
			if test.source {
				source.openErr = os.ErrPermission
			}
			session := &endpointReadSession{source: source}
			config := &Config{Spec: &entity.ScanJobSpec{MediaId: 1,
				SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
				ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}}

			// Refuse the actual result/finding write after ACP has settled its source outcome.
			writeErr := errors.New("result database write failed")
			writeAttempted := false
			if test.write {
				fail := func(tx *gorm.DB) {
					if tx.Statement.Table == "entries" {
						writeAttempted = true
						tx.AddError(writeErr)
					}
				}
				require.NoError(t, r.db.Callback().Update().Before("gorm:update").Register("test:result-write-failure", fail))
				require.NoError(t, r.db.Callback().Create().Before("gorm:create").Register("test:result-write-failure", fail))
				t.Cleanup(func() {
					require.NoError(t, r.db.Callback().Update().Remove("test:result-write-failure"))
					require.NoError(t, r.db.Callback().Create().Remove("test:result-write-failure"))
				})
			}
			err = r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil, session)

			// Distinct failures retain both identities and messages; a single cause needs no multierror wrapper.
			require.Equal(t, test.source, errors.Is(err, os.ErrPermission))
			require.Equal(t, test.write, errors.Is(err, writeErr))
			require.Equal(t, test.write, writeAttempted)
			require.Equal(t, 1, source.opens)
			var combined *multierror.Error
			require.Equal(t, test.source && test.write, errors.As(err, &combined))
			if combined != nil {
				require.Len(t, combined.Errors, 2)
				require.Contains(t, err.Error(), os.ErrPermission.Error())
				require.Contains(t, err.Error(), writeErr.Error())
			}

			// An unwritten result remains pending rather than publishing a fabricated finding.
			if test.write {
				var stored Entry
				require.NoError(t, r.db.First(&stored, entry.ID).Error)
				require.True(t, stored.NeedsHash)
				require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, stored.Finding)
				require.Zero(t, stored.CheckedAtNS)
			}
		})
	}
}
