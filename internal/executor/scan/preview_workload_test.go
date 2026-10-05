package scan

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type workloadPreviewer struct {
	scanPreviewer
	lookups int
	present map[string]bool
	cancel  context.CancelFunc
}

func (p *workloadPreviewer) Exists(signature []byte) (bool, error) {
	p.lookups++
	if p.cancel != nil {
		p.cancel()
	}
	return p.present[string(signature)], nil
}

func TestPreviewWorkloadStreamsDuplicateGroupsWithoutRowQueries(t *testing.T) {
	// Repeated signatures span many manifest pages and include unsupported earlier names.
	p := &workloadPreviewer{present: map[string]bool{}}
	exe, _ := setupAnalyzeWithPreview(t, p)
	r := newProgressRunner(t)
	r.exe = exe
	for group := 0; group < 4; group++ {
		hash := sha256.Sum256([]byte(fmt.Sprint(group)))
		var entries []*Entry
		for index := 0; index < 300; index++ {
			name := fmt.Sprintf("%d/%d.jpg", group, index)
			if index == 0 {
				name += ".txt"
			}
			entries = append(entries, &Entry{LocationID: 1, ScopePath: "selected", Path: name, SourcePath: name, SHA256: hash[:], Size: 12})
		}
		require.NoError(t, r.db.CreateInBatches(entries, 100).Error)
	}

	// The content store is the authority: a bundle it already holds removes that group from the
	// workload, without a row query per entry and without calling the generator.
	held, err := library.NewFileSignature(sha256Sum("0"), 12)
	require.NoError(t, err)
	p.present[string(held)] = true
	queries := 0
	require.NoError(t, r.db.Callback().Row().After("gorm:row").Register("count-workload", func(*gorm.DB) { queries++ }))
	require.NoError(t, r.db.Callback().Query().After("gorm:query").Register("count-workload", func(*gorm.DB) { queries++ }))
	total, err := r.previewWorkload(context.Background(),
		frozenLimits(&Config{Spec: &entity.ScanJobSpec{PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY}}),
		&Scope{ID: 1, LocationID: 1, Path: "selected"})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Equal(t, 1, queries)
	require.Equal(t, 4, p.lookups, "one store answer per content group")
	require.Empty(t, p.paths, "classification never calls Generate")
}

func sha256Sum(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func TestPreviewWorkloadCancellationClosesStream(t *testing.T) {
	// Cancel from the first asset check while further content groups remain unread.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &workloadPreviewer{cancel: cancel}
	exe, _ := setupAnalyzeWithPreview(t, p)
	r := newProgressRunner(t)
	r.exe = exe
	for index := 0; index < 10; index++ {
		hash := sha256.Sum256([]byte(fmt.Sprint(index)))
		require.NoError(t, r.db.Create(&Entry{Path: fmt.Sprint(index), SourcePath: "image.jpg", SHA256: hash[:]}).Error)
	}
	_, err := r.previewWorkload(ctx, frozenLimits(&Config{Spec: &entity.ScanJobSpec{}}), &Scope{})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, p.lookups, 10)

	// The released iterator leaves the bundle usable for a subsequent attempt.
	var count int64
	require.NoError(t, r.db.Model(&Entry{}).Count(&count).Error)
	require.EqualValues(t, 10, count)
}
