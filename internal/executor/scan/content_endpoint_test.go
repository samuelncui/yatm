package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

type endpointReadSession struct {
	source   acp.ContentSource
	requests []mediapkg.ReadRequest
}

func (*endpointReadSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{Read: mediapkg.AccessSequential}
}

func (*endpointReadSession) Media() *mediapkg.Descriptor { return &mediapkg.Descriptor{ID: 1} }

func (s *endpointReadSession) Source(request mediapkg.ReadRequest) (mediapkg.SourceRef, error) {
	s.requests = append(s.requests, request)
	return mediapkg.SourceRef{Content: s.source}, nil
}

func (*endpointReadSession) Finalize(context.Context, error) error { return nil }

type scanEndpointSource struct {
	content         []byte
	info            os.FileInfo
	opens           int
	openErr         error
	openFileOnError bool
	statErr         error
	attrs           map[string][]byte
	attrsErr        error
	closeErr        error
	closes          int
}

func (s *scanEndpointSource) Open(context.Context) (fs.File, error) {
	s.opens++
	if s.openErr != nil && !s.openFileOnError {
		return nil, s.openErr
	}
	return &scanEndpointFile{Reader: bytes.NewReader(s.content), source: s}, s.openErr
}

type scanEndpointFile struct {
	*bytes.Reader
	source *scanEndpointSource
}

func (f *scanEndpointFile) Stat() (fs.FileInfo, error) { return f.source.info, f.source.statErr }
func (f *scanEndpointFile) Xattrs() (map[string][]byte, error) {
	return f.source.attrs, f.source.attrsErr
}
func (f *scanEndpointFile) Close() error { f.source.closes++; return f.source.closeErr }

func TestScanEndpointReadsActualFactsWithoutNativePath(t *testing.T) {
	// The frozen Media identifier deliberately differs from every real filesystem path.
	r := newProgressRunner(t)
	content := []byte("observed endpoint content")
	root := t.TempDir()
	metadataFile := filepath.Join(root, "metadata")
	require.NoError(t, os.WriteFile(metadataFile, content, 0640))
	stamp := time.Unix(1700000000, 0)
	require.NoError(t, os.Chtimes(metadataFile, stamp, stamp))
	info, err := os.Stat(metadataFile)
	require.NoError(t, err)
	storage := &entity.StoragePosition{Order: []byte{1, 2, 3}}
	entry := &Entry{Path: "endpoint-display/member.txt", Storage: storage, StorageOrder: storage.Order,
		Size: 999, MtimeNS: stamp.UnixNano(), NeedsHash: true,
		Expected: &entity.ExpectedFile{SizeBytes: 999, Sha256: make([]byte, 32)}}
	require.NoError(t, r.db.Create(entry).Error)
	source := &scanEndpointSource{content: content, info: info}
	session := &endpointReadSession{source: source}
	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{MediaId: 1,
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})

	// The content page binds the endpoint from frozen storage and real ACP writes the verification result.
	require.NoError(t, r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil, session))
	var stored Entry
	require.NoError(t, r.db.First(&stored, entry.ID).Error)

	// Actual bytes and source metadata remain independent of the baseline, with no persisted native path.
	expectedHash := sha256.Sum256(content)
	require.Equal(t, 1, source.opens)
	require.Len(t, session.requests, 1)
	require.Equal(t, entry.Path, session.requests[0].Path)
	require.Equal(t, storage, session.requests[0].Storage)
	require.Empty(t, stored.SourcePath)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_MISMATCH, stored.Finding)
	require.EqualValues(t, len(content), stored.ActualSize)
	require.Equal(t, expectedHash[:], stored.ActualHash)
	require.Equal(t, uint32(info.Mode()), stored.ReadMode)
	require.Equal(t, stamp.UnixNano(), stored.ReadMtimeNS)
	require.EqualValues(t, 999, stored.Expected.SizeBytes)
	require.Equal(t, make([]byte, 32), stored.Expected.Sha256)
	require.False(t, stored.NeedsHash)
}

func TestScanVerifyRawDeviceFailureDoesNotInventFindings(t *testing.T) {
	for name, cause := range map[string]error{
		"uncertain command": tape.ErrUncertain,
		"unknown ownership": tape.ErrOwnershipUnknown,
		"external holder":   tape.ErrBusy,
		"stopped read lane": errors.Join(mediapkg.ErrReadUnavailable, os.ErrPermission),
		"no medium": errors.Join(mediapkg.ErrReadUnavailable, &tape.CommandError{
			Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 2, ASC: 0x3a},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			// A raw drive failure leaves every frozen candidate unexamined, rather than corrupt.
			r := newProgressRunner(t)
			for _, path := range []string{"first", "later"} {
				require.NoError(t, r.db.Create(&Entry{Path: path, NeedsHash: true, Finding: entity.ScanFinding_SCAN_FINDING_NOT_CHECKED,
					Expected: &entity.ExpectedFile{SizeBytes: 1, Sha256: make([]byte, 32)}}).Error)
			}
			session := &endpointReadSession{source: &scanEndpointSource{openErr: cause}}
			config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{MediaId: 1,
				SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
				ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})

			// Real ACP returns the device cause; its result writer must not publish file findings.
			err := r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil, session)
			require.ErrorIs(t, err, cause)
			var rows []Entry
			require.NoError(t, r.db.Order("id").Find(&rows).Error)
			require.Len(t, rows, 2)
			for _, row := range rows {
				require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, row.Finding)
				require.Zero(t, row.CheckedAtNS)
				require.True(t, row.NeedsHash)
			}
		})
	}
}

func TestScanLogicalEndpointStopFindsOnlyActuallyOpenedMember(t *testing.T) {
	// A definite first-member error may be a finding, but ACP's copied soft-stop cause cannot examine later rows.
	r := newProgressRunner(t)
	for index, path := range []string{"first", "later"} {
		require.NoError(t, r.db.Create(&Entry{Path: path, StorageOrder: []byte{byte(index + 1)}, NeedsHash: true, Finding: entity.ScanFinding_SCAN_FINDING_NOT_CHECKED,
			Expected: &entity.ExpectedFile{SizeBytes: 1, Sha256: make([]byte, 32)}}).Error)
	}
	cause := errors.New("first member header is corrupt")
	source := &scanEndpointSource{openErr: cause}
	session := &endpointReadSession{source: source}
	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{MediaId: 1,
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})
	err := r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil, session)
	require.ErrorIs(t, err, cause)
	require.Equal(t, 1, source.opens)

	// The original failing read remains visible; an unopened copy stays unexamined and pending.
	var rows []Entry
	require.NoError(t, r.db.Order("id").Find(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_UNREADABLE, rows[0].Finding)
	require.NotZero(t, rows[0].CheckedAtNS)
	require.False(t, rows[0].NeedsHash)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_NOT_CHECKED, rows[1].Finding)
	require.Zero(t, rows[1].CheckedAtNS)
	require.True(t, rows[1].NeedsHash)
}
