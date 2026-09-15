package library

import (
	"context"
	"fmt"
	"math"
	"path"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/ignore"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// InspectSelection reports complete metadata totals and unavailable inputs without filesystem I/O.
func (l *Library) InspectSelection(ctx context.Context, request *entity.InspectSelectionRequest) (*entity.InspectSelectionReply, error) {
	// Bound explicit inputs; all descendants are traversed in existing ordered pages.
	if request == nil || len(request.Selections)+len(request.FileVersionIds) == 0 || len(request.Selections)+len(request.FileVersionIds) > 1000 {
		return nil, fmt.Errorf("inspect between 1 and 1000 selection roots or versions")
	}
	if !request.Restore && len(request.FileVersionIds) != 0 {
		return nil, fmt.Errorf("explicit versions require a Restore selection")
	}
	if !request.Restore && (request.VersionPolicy != nil || request.SkipUnmatchedVersions) {
		return nil, fmt.Errorf("version policy requires a Restore selection")
	}
	if err := ValidateRestoreVersionSelection(request.VersionPolicy, request.SkipUnmatchedVersions); err != nil {
		return nil, err
	}

	// Keep import from replacing identities while inspecting an independent request snapshot.
	request = proto.Clone(request).(*entity.InspectSelectionRequest)
	release, err := l.UseOnlineRead()
	if err != nil {
		return nil, err
	}
	defer release()

	// Compute every total against the same metadata view.
	reply := &entity.InspectSelectionReply{Selections: request.Selections}
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Restore estimates include outputs excluded by the selected destination's Ignore rules.
		view := &Library{db: tx, online: l.online}
		var ignored *ignore.Matcher
		if request.Destination != nil {
			if !request.Restore || request.Destination.LocationId <= 0 {
				return fmt.Errorf("Restore destination requires a valid Location and Restore selection")
			}
			if request.Destination.Path != "" {
				if err := entity.ValidateRelativePath(request.Destination.Path); err != nil {
					return err
				}
			}
			location, err := view.GetOnlineSource(ctx, request.Destination.LocationId)
			if err != nil {
				return err
			}
			ignored = ignore.Compile(location.Exclusions.GetText())
		}

		// Resolve visibility and binding revisions before expanding any selected root.
		if len(request.Selections) > 0 {
			if err := view.FreezeSelections(ctx, request.Selections); err != nil {
				return err
			}
		}

		// Aggregate full counts without retaining expanded paths or content-check statistics.
		add := func(name string, size int64) error {
			// Unknown size is distinct from a known empty file.
			if reply.Files == math.MaxInt64 || size > math.MaxInt64-reply.Bytes {
				return fmt.Errorf("selection totals exceed the supported range")
			}
			reply.Files++
			if size < 0 {
				reply.UnknownSizeFiles++
			} else {
				reply.Bytes += size
			}

			// Output paths are used only to report Restore association exclusions.
			if ignored != nil && ignored.Match(path.Join(request.Destination.Path, strings.TrimPrefix(name, "/")), false) {
				reply.IgnoredOutputs++
			}
			return nil
		}
		addVersion := func(version *FileVersion, name string) error {
			// Keep missing copies separate from an absent version or date-policy match.
			if err := add(name, version.Size); err != nil {
				return err
			}
			if !version.HasRestoreIntegrity() {
				reply.MissingCopies++
				return nil
			}

			// Eligible inventory must agree with the selected version's immutable content.
			allowed := []entity.PositionHealth{entity.PositionHealth_POSITION_HEALTH_UNKNOWN, entity.PositionHealth_HEALTHY}
			if request.AllowDamagedCopies {
				allowed = append(allowed, entity.PositionHealth_DAMAGED, entity.PositionHealth_UNREADABLE)
			}
			var summary struct{ Candidates, Conflicts int64 }
			err := tx.Model(&Position{}).Joins("LEFT JOIN media ON media.id = positions.media_id").
				Where("positions.signature = ? AND positions.is_dir = ? AND positions.health IN ?", version.Signature, false, allowed).
				Select(`COUNT(*) AS candidates, COALESCE(SUM(CASE WHEN positions.size != ?
					OR positions.hash IS NULL OR positions.hash != ? OR media.id IS NULL
					THEN 1 ELSE 0 END), 0) AS conflicts`, version.Size, version.Hash).
				Scan(&summary).Error
			if err != nil {
				return err
			}
			if summary.Candidates == 0 || summary.Conflicts != 0 {
				reply.MissingCopies++
			}
			return nil
		}

		// Resolve the same automatic policy and explicit overrides used by Restore indexing.
		if request.Restore {
			return view.WalkRestoreSelections(ctx, request.Selections, request.FileVersionIds, request.VersionPolicy,
				func(item *RestoreSelectionItem) error {
					if item.Direct {
						reply.ResolvedVersions = append(reply.ResolvedVersions, item.Resolution)
					}
					if item.Version == nil {
						reply.UnmatchedVersions++
						if request.SkipUnmatchedVersions {
							reply.SkippedVersions++
						}
						return nil
					}
					return addVersion(item.Version, item.Path)
				})
		}

		// Backup estimates retain original availability independently of saved-version policy.
		return view.WalkFileSelections(ctx, request.Selections, func(file *File, target string) error {
			// Original availability here means a usable published binding, not a fresh filesystem check.
			original, err := view.GetFileLocation(ctx, file.ID)
			if err != nil {
				return err
			}
			if original == nil {
				reply.MissingOriginals++
				return add(target, -1)
			}
			location, err := view.GetOnlineSource(ctx, original.LocationID)
			if err != nil {
				return err
			}
			if !original.CurrentBinding(location) {
				reply.MissingOriginals++
			}
			return add(target, original.Size)
		})
	})
	return reply, err
}
