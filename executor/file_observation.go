package executor

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
)

// ObserveFileContent checks only the selected original's metadata and existing identity evidence.
// It never hashes content, writes attributes, admits files, or opens archived Media.
func (e *Executor) ObserveFileContent(ctx context.Context, fileID int64, summary *entity.FileContentSummary) error {
	// Catalog-only history survives every failed or changed original observation.
	if summary == nil {
		return nil
	}
	summary.ObservedAtMs = time.Now().UnixMilli()
	summary.CurrentObservationValid = false
	defer func() {
		if !summary.CurrentObservationValid {
			summary.SignatureKnown = false
			summary.ArchivedCopies, summary.HealthyCopies, summary.UncheckedCopies, summary.UnhealthyCopies = 0, 0, 0, 0
			summary.RestorableCurrentCopies = 0
		}
	}()
	original, err := e.lib.GetFileLocation(ctx, fileID)
	if err != nil {
		return err
	}
	if original == nil {
		summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_UNLINKED
		return nil
	}

	// A missing or untrusted root is unknown availability, never proof of a missing original.
	summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_UNAVAILABLE
	location, err := e.lib.GetOnlineSource(ctx, original.LocationID)
	if err != nil {
		return err
	}
	if !original.CurrentBinding(location) {
		return library.ErrOnlineUnverified
	}
	if _, _, err := e.CheckLocationPath(location, ""); err != nil {
		return err
	}
	full, info, err := e.CheckLocationPath(location, original.Path)
	if errors.Is(err, os.ErrNotExist) {
		// Recheck the root after the failed lookup so a lost mount does not look like an absent file.
		if _, _, rootErr := e.CheckLocationPath(location, ""); rootErr != nil {
			return rootErr
		}
		summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_MISSING
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_MISSING
		return nil
	}
	summary.OriginalAvailability = entity.OriginalAvailability_ORIGINAL_PRESENT

	// Path continuity does not make an old content signature valid for a replacement object.
	keys, err := ReadTracking(location, full, info)
	if err != nil {
		return err
	}
	valid, err := e.lib.MatchesObservation(ctx, location, &library.OnlinePosition{FileID: fileID, Path: original.Path,
		Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), TrackingKeys: keys})
	if err != nil {
		return err
	}
	if _, _, _, err := e.ResolveLocationEntry(ctx, &entity.LocationEntryRef{LocationId: location.ID, Path: original.Path,
		BindingToken: location.BindingToken, Facts: LocationFacts(info)}); err != nil {
		return err
	}
	summary.CurrentObservationValid = valid
	return nil
}
