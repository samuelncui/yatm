package executor

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

// ObserveFileContent checks only the selected original's metadata and existing identity evidence.
// It never hashes content, writes attributes, admits files, or opens archived Media.
func (e *Executor) ObserveFileContent(ctx context.Context, fileID int64, summary *entity.FileContentSummary) error {
	_, err := e.ObserveFileContentEntry(ctx, fileID, summary)
	return err
}

// ObserveFileContentEntry returns the same live object used to derive the summary so callers do not repeat the read.
func (e *Executor) ObserveFileContentEntry(ctx context.Context, fileID int64, summary *entity.FileContentSummary) (*entity.LocationEntry, error) {
	// Catalog-only history survives every failed or changed original observation.
	if summary == nil {
		return nil, nil
	}
	summary.ObservedAtNs = time.Now().UnixNano()
	summary.CurrentObservationValid = false
	defer func() {
		if !summary.CurrentObservationValid {
			summary.SignatureKnown = false
			summary.ArchivedCopyCount, summary.HealthyCopyCount, summary.UncheckedCopyCount, summary.UnhealthyCopyCount = 0, 0, 0, 0
			summary.RestorableCurrentCopyCount = 0
		}
	}()
	original, err := e.lib.GetFileLocation(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if original == nil {
		summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED
		return nil, nil
	}

	// A missing or untrusted root is unknown availability, never proof of a missing original.
	summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE
	location, err := e.lib.GetLocation(ctx, original.LocationID)
	if err != nil {
		return nil, err
	}
	root, err := e.CheckLocation(location)
	if err != nil {
		return nil, err
	}
	_, info, err := e.checkPreparedLocationPath(location, root, original.Path)
	if errors.Is(err, os.ErrNotExist) {
		summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING
		return nil, nil
	}
	live, err := locationEntry(location.ID, original.Path, info)
	if err != nil {
		return nil, err
	}
	summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT

	// Path continuity does not make an old content signature valid for a replacement object.
	keys := ObserveTracking(location, info)
	valid, err := e.lib.MatchesObservation(ctx, location, &library.ObservedEntry{FileID: fileID, Path: original.Path,
		Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: live.Reference.Facts.MtimeNs, TrackingKeys: keys})
	if err != nil {
		return live, err
	}
	summary.CurrentObservationValid = valid
	return live, nil
}
