package apis

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/executor/fileops"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type locationService struct {
	entity.UnimplementedLocationServiceServer
	api *API
}

func (api *API) RegisterLocations(server grpc.ServiceRegistrar) {
	fileops.RegisterService(server, api.exe)
	entity.RegisterFilesServiceServer(server, &filesService{api: api})
	entity.RegisterLocationServiceServer(server, &locationService{api: api})
	entity.RegisterFileCatalogServiceServer(server, &fileCatalogService{api: api})
	entity.RegisterSettingsServiceServer(server, &settingsService{api: api})
}

func onlineError(err error) error {
	// Preserve actionable catalog conflicts independently from malformed configuration.
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, executor.ErrAccessExcluded):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, library.ErrOnlineBusy):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, library.ErrOnlineConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, library.ErrOnlineUnverified):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, gorm.ErrRecordNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, library.ErrFileNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return err
	}
}

func onlineLimit(value int32) (int, error) {
	if value == 0 {
		return 100, nil
	}
	if value < 0 || value > 1000 {
		return 0, status.Error(codes.InvalidArgument, "limit must be between 1 and 1000")
	}
	return int(value), nil
}

func (s *locationService) configuration(root string, exclusions *entity.OnlineExclusions) (string, *entity.OnlineExclusions, error) {
	// New bindings must be accessible, safe descendants of the configured source boundary.
	canonical, err := s.api.exe.OnlineRoot(root)
	if err != nil {
		return "", nil, err
	}
	normalized, err := library.NormalizeOnlineExclusions(exclusions)
	if err != nil {
		return "", nil, err
	}

	// Mandatory exclusions are displayed separately and cannot be overridden by user patterns.
	if _, err := s.api.exe.RequiredOnlineExclusions(canonical); err != nil {
		return "", nil, err
	}
	return canonical, normalized, nil
}

func (s *locationService) Create(ctx context.Context, req *entity.CreateLocationRequest) (*entity.LocationReply, error) {
	// Validate all access constraints before changing catalog state.
	if req == nil || req.Location == nil {
		return nil, status.Error(codes.InvalidArgument, "Location source request is missing")
	}
	root, exclusions, err := s.configuration(req.Location.RootPath, locationIgnore(req.Location.Ignore))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	source := &library.Location{Name: req.Location.Name, ExecutorID: "local", RootPath: root, Exclusions: exclusions, WriteTrackingUUID: req.Location.WriteTrackingUuid,
		RestoreTarget: req.Location.RestoreTarget}

	// Unique root conflicts cannot overwrite an existing registration or index.
	if err := s.api.lib.CreateOnlineSource(ctx, source); err != nil {
		return nil, onlineError(err)
	}
	reply := s.reply(ctx, source)
	settings, err := s.api.lib.GetLibrarySettings(ctx)
	if err != nil {
		reply.Warnings = append(reply.Warnings, err.Error())
		return reply, nil
	}
	if settings.AutoCollectFiles {
		job, err := scanjob.Create(ctx, s.api.exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{LocationId: source.ID,
			SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS}})
		if err != nil {
			reply.Warnings = append(reply.Warnings, "Initial collection: "+err.Error())
		} else {
			reply.Location.LastJobId = job.Job.Id
		}
	}
	return reply, nil
}

func (s *locationService) List(ctx context.Context, req *entity.ListLocationsRequest) (*entity.ListLocationsReply, error) {
	// Catalog pages intentionally exclude expensive per-source filesystem probes.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location list request is missing")
	}
	limit, err := onlineLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListLocations(ctx, library.LocationListFilter{
		AfterID: req.AfterId, Limit: limit, RestoreTarget: req.RestoreTarget, Query: req.Query,
	})
	if err != nil {
		return nil, onlineError(err)
	}

	// Preserve ID order for a stable list cursor.
	reply := &entity.ListLocationsReply{HasMore: more, Locations: make([]*entity.Location, 0, len(rows))}
	for _, row := range rows {
		reply.Locations = append(reply.Locations, row.CatalogEntity())
	}
	return reply, nil
}

func (s *locationService) Get(ctx context.Context, req *entity.LocationRef) (*entity.LocationReply, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location get request is missing")
	}
	source, err := s.api.lib.GetOnlineSource(ctx, req.Id)
	if err != nil {
		return nil, onlineError(err)
	}
	return s.reply(ctx, source), nil
}

func (s *locationService) Update(ctx context.Context, req *entity.UpdateLocationRequest) (*entity.LocationReply, error) {
	// Name-only changes remain possible while the bound filesystem is unavailable.
	if req == nil || req.Location == nil {
		return nil, status.Error(codes.InvalidArgument, "Location update request is missing")
	}
	stored, err := s.api.lib.GetOnlineSource(ctx, req.Location.Id)
	if err != nil {
		return nil, onlineError(err)
	}
	if stored.Revision != req.Location.Revision {
		return nil, onlineError(library.ErrOnlineConflict)
	}
	exclusions, err := library.NormalizeOnlineExclusions(locationIgnore(req.Location.Ignore))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	root := req.Location.RootPath
	if stored.RootPath != root || !proto.Equal(stored.Exclusions, exclusions) {
		root, exclusions, err = s.configuration(root, exclusions)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	// A revision-checked update changes configuration without touching cached positions.
	stored.Name, stored.RootPath, stored.Exclusions = req.Location.Name, root, exclusions
	stored.WriteTrackingUUID = req.Location.WriteTrackingUuid
	stored.RestoreTarget = req.Location.RestoreTarget
	updated, err := s.api.lib.UpdateOnlineSource(ctx, stored, false)
	if err != nil {
		return nil, onlineError(err)
	}
	return s.reply(ctx, updated), nil
}

func (s *locationService) Confirm(ctx context.Context, req *entity.LocationRef) (*entity.LocationReply, error) {
	// Confirmation explicitly revalidates an imported path in this installation.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location confirm request is missing")
	}
	stored, err := s.api.lib.GetOnlineSource(ctx, req.Id)
	if err != nil {
		return nil, onlineError(err)
	}
	if stored.Revision != req.Revision {
		return nil, onlineError(library.ErrOnlineConflict)
	}
	root, exclusions, err := s.configuration(stored.RootPath, stored.Exclusions)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	// Confirmation admits this binding; each file still requires a current verified observation.
	stored.RootPath, stored.Exclusions, stored.ExecutorID = root, exclusions, "local"
	updated, err := s.api.lib.UpdateOnlineSource(ctx, stored, true)
	if err != nil {
		return nil, onlineError(err)
	}
	return s.reply(ctx, updated), nil
}

func (s *locationService) Delete(ctx context.Context, req *entity.LocationRef) (*entity.DeleteLocationReply, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location delete request is missing")
	}
	if err := s.api.lib.DeleteOnlineSource(ctx, req.Id, req.Revision); err != nil {
		return nil, onlineError(err)
	}
	return &entity.DeleteLocationReply{}, nil
}

func (s *locationService) ListEntries(ctx context.Context, req *entity.ListLocationEntriesRequest) (*entity.ListLocationEntriesReply, error) {
	// Physical rows never come from Library inventory, even when collection is unavailable.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location entries request is missing")
	}
	reply, err := s.api.exe.ListLocationEntries(ctx, req)
	if err != nil {
		return nil, onlineError(err)
	}
	// Browsing has no admission side effects, including for previously linked entries.
	for _, entry := range reply.Entries {
		if err := s.hydrateEntry(ctx, entry, false); err != nil {
			return nil, onlineError(err)
		}
	}
	return reply, nil
}

func (s *locationService) GetEntry(ctx context.Context, req *entity.GetLocationEntryRequest) (*entity.LocationEntry, error) {
	// Destination directories and unadmitted files have ordinary live references too.
	entry, err := s.api.exe.ObserveLocationEntry(ctx, req.GetLocationId(), req.GetPath())
	if err != nil {
		return nil, onlineError(err)
	}
	if err := s.hydrateEntry(ctx, entry, false); err != nil {
		return nil, onlineError(err)
	}
	return entry, nil
}

func (s *locationService) Admit(ctx context.Context, req *entity.LocationEntryRef) (*entity.LocationEntry, error) {
	// Explicit user actions can admit an ordinary file regardless of automatic Ignore.
	original, err := s.api.exe.AdmitLocationEntry(ctx, req)
	if err != nil {
		return nil, onlineError(err)
	}
	entry := &entity.LocationEntry{Path: req.Path, Reference: req, Original: original.ToEntity()}
	if err := s.hydrateEntry(ctx, entry, false); err != nil {
		return nil, onlineError(err)
	}
	return entry, nil
}

func (s *locationService) hydrateEntry(ctx context.Context, entry *entity.LocationEntry, collect bool) error {
	// Actual type and facts are authoritative; optional catalog metadata cannot create physical rows.
	if entry.Reference == nil || !os.FileMode(entry.Reference.Facts.Mode).IsRegular() {
		return nil
	}
	ref := entry.Reference
	original, err := s.api.lib.GetFileLocationAtPath(ctx, ref.LocationId, ref.Path)
	if err != nil {
		return err
	}
	if collect {
		current, admitErr := s.api.exe.AdmitLocationEntry(ctx, ref)
		if admitErr != nil {
			return admitErr
		}
		original = current
	}
	if original == nil {
		return nil
	}
	return s.hydrateFileEntry(ctx, entry, original)
}

func (s *locationService) hydrateFileEntry(ctx context.Context, entry *entity.LocationEntry, original *library.FileLocation) error {
	// Annotation and content summaries are optional associations on a real filesystem row.
	entry.Original = original.ToEntity()
	file, err := s.api.lib.GetFile(ctx, original.FileID)
	if err != nil {
		return err
	}
	if err := s.api.hydrateFileTags(ctx, file); err != nil {
		return err
	}
	if err := s.api.lib.HydrateFileContent(ctx, file); err != nil {
		return err
	}
	entry.File = convertFiles(file)[0]
	return nil
}

func locationIgnore(rules *entity.IgnoreRules) *entity.OnlineExclusions {
	format := rules.GetFormat()
	if format == "" {
		format = "gitignore"
	}
	return &entity.OnlineExclusions{Format: format, Text: rules.GetText()}
}

func (s *locationService) reply(ctx context.Context, source *library.Location) *entity.LocationReply {
	// Accessibility is an observation with a timestamp, independent of durable binding state.
	reply := &entity.LocationReply{Location: source.CatalogEntity(), Accessibility: &entity.OnlineAccessibility{CheckedAtMs: time.Now().UnixMilli()}}
	_, err := s.api.exe.CheckOnlineSource(source)
	reply.Accessibility.Accessible = err == nil
	if err != nil {
		reply.Accessibility.Error = err.Error()
	}
	if required, err := s.api.exe.RequiredOnlineExclusions(source.RootPath); err == nil {
		reply.RequiredExclusions = required
	}

	// Warn about overlap without materializing all sources or treating duplicate paths as backups.
	var after int64
	for {
		rows, more, err := s.api.lib.ListOnlineSources(ctx, after, 100, nil)
		if err != nil {
			reply.Warnings = append(reply.Warnings, fmt.Sprintf("Cannot check nested sources: %s", err))
			break
		}
		for _, other := range rows {
			if other.ID == source.ID {
				continue
			}
			separator := string(filepath.Separator)
			if strings.HasPrefix(source.RootPath, strings.TrimSuffix(other.RootPath, separator)+separator) || strings.HasPrefix(other.RootPath, strings.TrimSuffix(source.RootPath, separator)+separator) {
				reply.Warnings = append(reply.Warnings, "Overlapping sources may index the same files; online locations are not independent backups.")
				return reply
			}
		}
		if !more {
			break
		}
		after = rows[len(rows)-1].ID
	}
	return reply
}
