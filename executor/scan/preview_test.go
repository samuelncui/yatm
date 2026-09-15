package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

type scanPreviewer struct {
	lock    sync.Mutex
	paths   []string
	hashes  [][]byte
	updates []bool
	fail    bool
	before  func(context.Context)
}

func (*scanPreviewer) Supports(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".jpg")
}
func (p *scanPreviewer) Generate(ctx context.Context, name string, hash []byte, _, _ int64, update bool) ([]byte, error) {
	if p.before != nil {
		p.before(ctx)
	}
	p.lock.Lock()
	defer p.lock.Unlock()
	p.paths = append(p.paths, name)
	p.hashes = append(p.hashes, append([]byte(nil), hash...))
	p.updates = append(p.updates, update)
	if p.fail {
		return nil, errors.New("generator failed")
	}
	return []byte{1}, nil
}
func (*scanPreviewer) Manifest([]byte) (*entity.PreviewManifest, error) { return nil, os.ErrNotExist }
func (*scanPreviewer) Open([]byte, string) (io.ReadCloser, error)       { return nil, os.ErrNotExist }

func TestScanKnownOnlyDoesNotHashUnknownPreviewSources(t *testing.T) {
	for _, policy := range []entity.PreviewPolicy{entity.PreviewPolicy_PREVIEW_MISSING_ONLY, entity.PreviewPolicy_PREVIEW_REGENERATE_ALL} {
		t.Run(policy.String(), func(t *testing.T) {
			// Neither Preview mode may upgrade cache-only signature acquisition to an actual hash read.
			previews := &scanPreviewer{}
			exe, location := setupAnalyzeWithPreview(t, previews)
			writeAnalyzeFile(t, location, "unknown.jpg", "new image")
			r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{LocationId: location.ID,
				SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, PreviewPolicy: policy})
			require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())

			// The unsigned original is admitted, but no Preview or disposable hash cache is manufactured.
			rows, err := exe.Lib().OnlineFilesPage(context.Background(), location.ID, "", 10)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Empty(t, rows[0].Signature)
			_, cached, err := acp.ReadCachedSignature(filepath.Join(location.RootPath, "unknown.jpg"))
			require.NoError(t, err)
			require.False(t, cached)
			progress, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: r.job.ID})
			require.NoError(t, err)
			require.EqualValues(t, 1, progress.PreviewsSkipped)
			require.Empty(t, previews.paths)
		})
	}
}

func TestScanPreviewPoliciesControlSignedContentGeneration(t *testing.T) {
	for _, test := range []struct {
		policy entity.PreviewPolicy
		calls  int
		force  bool
	}{
		{policy: entity.PreviewPolicy_PREVIEW_NONE},
		{policy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY, calls: 1},
		{policy: entity.PreviewPolicy_PREVIEW_REGENERATE_ALL, calls: 1, force: true},
	} {
		t.Run(test.policy.String(), func(t *testing.T) {
			// Equal signed content shares generation without changing the requested replacement policy.
			previews := &scanPreviewer{}
			exe, location := setupAnalyzeWithPreview(t, previews)
			writeAnalyzeFile(t, location, "a.jpg", "same image")
			writeAnalyzeFile(t, location, "b.jpg", "same image")
			r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{LocationId: location.ID, PreviewPolicy: test.policy})
			require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
			require.Len(t, previews.paths, test.calls)
			for _, force := range previews.updates {
				require.Equal(t, test.force, force)
			}
		})
	}
}

func TestScanPreviewRejectsSequentialMedia(t *testing.T) {
	// Only metadata is created; rejection happens before a Tape Job or physical device operation exists.
	exe, _ := setupAnalyzeWithPreview(t, &scanPreviewer{})
	media, err := exe.Lib().CreateMedia(context.Background(), &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "PREVIEW001", Name: "Tape",
		Profile: (&entity.TapeMediaProfile{Format: "ltfs_v1"}).Pack(),
	})
	require.NoError(t, err)

	// Both generation modes remain unavailable for a sequential source.
	for _, policy := range []entity.PreviewPolicy{entity.PreviewPolicy_PREVIEW_MISSING_ONLY, entity.PreviewPolicy_PREVIEW_REGENERATE_ALL} {
		_, err := Create(context.Background(), exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{
			MediaId: media.ID, PreviewPolicy: policy,
		}})
		require.ErrorContains(t, err, "sequential Media does not support Preview generation")
	}
}

func TestIndexedScanCapturesFrozenLocationResource(t *testing.T) {
	// Parent-owned content must retain its original Location in the companion Job's resource history.
	previews := &scanPreviewer{}
	exe, location := setupAnalyzeWithPreview(t, previews)
	writeAnalyzeFile(t, location, "photo.jpg", "photo")
	hash := sha256.Sum256([]byte("photo"))
	job, err := CreateIndexed(context.Background(), exe, 0, &entity.ScanJobSpec{PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY}, func(_ context.Context, yield func(string, *entity.ExpectedFile) error) error {
		return yield(filepath.Join(location.RootPath, "photo.jpg"), &entity.ExpectedFile{Sha256: hash[:], Size: 5, OriginalLocationId: location.ID})
	})
	require.NoError(t, err)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_COMPLETED)
	jobs, err := exe.ListJob(context.Background(), &entity.JobFilter{LocationId: &location.ID})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1)
	require.Equal(t, job.ID, jobs.Jobs[0].ID)
}

func TestScanCheckPreviewsRequireMatchingActualContent(t *testing.T) {
	// Freeze known content and one unverifiable entry independently from later filesystem changes.
	ctx := context.Background()
	previews := &scanPreviewer{}
	exe, volume, media := setupVerifyExecutorWithPreview(t, previews)
	good := saveVerifyFile(t, exe, volume, media, "good.jpg", []byte("good"))
	bad := saveVerifyFile(t, exe, volume, media, "bad.jpg", []byte("before"))
	unknown := saveVerifyFile(t, exe, volume, media, "unknown.jpg", []byte("unknown"))
	unknown.Hash = nil
	require.NoError(t, exe.Lib().SavePosition(ctx, unknown))
	// Stable metadata does not permit previewing corrupt bytes under the recorded good content identity.
	require.NoError(t, os.WriteFile(filepath.Join(volume.Root, bad.Path), []byte("damage"), 0o644))
	require.NoError(t, os.Chtimes(filepath.Join(volume.Root, bad.Path), bad.ModTime, bad.ModTime))
	// Unchanged content with a different physical timestamp can still produce a verified preview.
	require.NoError(t, os.Chtimes(filepath.Join(volume.Root, good.Path), good.ModTime.Add(time.Hour), good.ModTime.Add(time.Hour)))
	reply, err := Create(ctx, exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{MediaId: media.ID,
		ResultPolicy: entity.ScanResultPolicy_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ, PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY}})
	require.NoError(t, err)
	waitScanStatus(t, exe, reply.Job.Id, entity.JobStatus_COMPLETED)
	progress, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: reply.Job.Id})
	require.NoError(t, err)
	require.EqualValues(t, 1, progress.Matched)
	require.EqualValues(t, 1, progress.Damaged)
	require.EqualValues(t, 1, progress.Unverifiable)
	require.EqualValues(t, 1, progress.PreviewsReady)
	require.EqualValues(t, 2, progress.PreviewsSkipped)
	require.Equal(t, []string{filepath.Join(volume.Root, good.Path)}, previews.paths)
	require.Equal(t, good.Hash, previews.hashes[0])
}

func TestScanKnownOnlyUsesValidXattrWithoutHashFallback(t *testing.T) {
	// Populate a real disposable cache through ACP before running cache-only acquisition.
	previews := &scanPreviewer{}
	exe, location := setupAnalyzeWithPreview(t, previews)
	writeAnalyzeFile(t, location, "cached.jpg", "before")
	name := filepath.Join(location.RootPath, "cached.jpg")
	stream := &singleRead{filename: name}
	require.NoError(t, acp.RunStream(context.Background(), stream, stream, acp.WithHash(true), acp.WithSignatureCache(true)))
	cache, valid, err := acp.ReadCachedSignature(name)
	require.NoError(t, err)
	if !valid {
		t.Skip("signature xattrs unavailable")
	}
	// Same-key changed bytes deliberately prove cache-only never falls back to a real hash.
	require.NoError(t, os.WriteFile(name, []byte("change"), 0644))
	require.NoError(t, os.Chtimes(name, time.Unix(0, cache.MtimeNS), time.Unix(0, cache.MtimeNS)))
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{LocationId: location.ID,
		SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY, PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	require.Len(t, previews.hashes, 1)
	require.Equal(t, cache.SHA256[:], previews.hashes[0])
	rows, err := exe.Lib().OnlineFilesPage(context.Background(), location.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, rows, "report-only cannot admit originals")
}

func TestScanPreviewSharesSelectionPublicationAndContentDeduplication(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "failed"}[fail], func(t *testing.T) {
			previews := &scanPreviewer{fail: fail}
			exe, location := setupAnalyzeWithPreview(t, previews)
			writeAnalyzeFile(t, location, "images/a.jpg", "one")
			writeAnalyzeFile(t, location, "images/b.jpg", "one")
			writeAnalyzeFile(t, location, "images/c.jpg", "two")
			writeAnalyzeFile(t, location, "images/readme.txt", "text")
			writeAnalyzeFile(t, location, "excluded/image.jpg", "excluded")
			r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: []*entity.FileSelection{{Target: &entity.FileSelection_Location{
				Location: &entity.LocationSelection{LocationId: location.ID, Path: "images"}}}}, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS,
				PreviewPolicy: entity.PreviewPolicy_PREVIEW_REGENERATE_ALL})
			require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
			rows, err := exe.Lib().OnlineFilesPage(context.Background(), location.ID, "", 10)
			require.NoError(t, err)
			require.Len(t, rows, 4)
			if !fail {
				require.Len(t, previews.paths, 2)
			}
			for _, update := range previews.updates {
				require.True(t, update)
			}
			jobs, err := exe.ListJob(context.Background(), &entity.JobFilter{})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 1, "Preview must not create a child Job")
		})
	}
}

func TestScanPreviewExcludesImportThroughoutGeneration(t *testing.T) {
	// Pause generation at the metadata replacement boundary to prove maintenance exclusion.
	previews := &scanPreviewer{}
	exe, location := setupAnalyzeWithPreview(t, previews)
	writeAnalyzeFile(t, location, "photo.jpg", "photo")
	var exported bytes.Buffer
	require.NoError(t, exe.Lib().Export(context.Background(), &exported, []entity.LibraryEntityType{entity.LibraryEntityType_FILE}))
	var importErr error
	previews.before = func(ctx context.Context) { importErr = exe.Lib().Import(ctx, bytes.NewReader(exported.Bytes())) }
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{LocationId: location.ID, PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY})
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, r.Phase())
	require.ErrorIs(t, importErr, library.ErrOnlineBusy)
}

func TestScanPreviewDriftRetainsPreviousOriginals(t *testing.T) {
	// Retain a valid original before introducing drift inside Preview generation.
	previews := &scanPreviewer{}
	exe, location := setupAnalyzeWithPreview(t, previews)
	writeAnalyzeFile(t, location, "photo.jpg", "before")
	require.Equal(t, entity.JobPhase_JOB_PHASE_COMPLETED, runAnalyze(t, exe, location.ID, false).Phase())
	before, err := exe.Lib().OnlineFilesPage(context.Background(), location.ID, "", 10)
	require.NoError(t, err)
	previews.before = func(context.Context) {
		require.NoError(t, os.WriteFile(filepath.Join(location.RootPath, "photo.jpg"), []byte("changed"), 0644))
	}
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{LocationId: location.ID, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY})
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, r.Phase())
	after, err := exe.Lib().OnlineFilesPage(context.Background(), location.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, before[0].Signature, after[0].Signature)
}

func TestScanIndexedCompanionValidatesFrozenContent(t *testing.T) {
	// A companion may only generate for the parent's frozen expected content.
	previews := &scanPreviewer{}
	exe, location := setupAnalyzeWithPreview(t, previews)
	writeAnalyzeFile(t, location, "photo.jpg", "before")
	name := filepath.Join(location.RootPath, "photo.jpg")
	wrong := sha256.Sum256([]byte("wrong!"))
	job, err := CreateIndexed(context.Background(), exe, 0, &entity.ScanJobSpec{PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY}, func(_ context.Context, yield func(string, *entity.ExpectedFile) error) error {
		return yield(name, &entity.ExpectedFile{Sha256: wrong[:], Size: 6})
	})
	require.NoError(t, err)
	failed := waitScanStatus(t, exe, job.ID, entity.JobStatus_INDEXING)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, failed.Phase)
	require.Empty(t, previews.paths)
}
