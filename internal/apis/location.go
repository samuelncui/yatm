package apis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
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
	entity.RegisterFilesServiceServer(server, &filesService{api: api})
	entity.RegisterLocationServiceServer(server, &locationService{api: api})
	entity.RegisterMediaServiceServer(server, &mediaService{api: api})
	entity.RegisterLibraryServiceServer(server, &libraryService{api: api})
	entity.RegisterPreviewServiceServer(server, &previewService{api: api})
	entity.RegisterSettingsServiceServer(server, &settingsService{api: api})
}

func apiError(err error) error {
	// Preserve actionable catalog conflicts independently from malformed configuration.
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, executor.ErrAccessExcluded):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, library.ErrLocationBusy):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, library.ErrLocationConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, library.ErrLocationUnverified):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, settingspkg.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, settingspkg.ErrStored):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, gorm.ErrRecordNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, library.ErrFileNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return err
	}
}

func pageLimit(value int32) (int, error) {
	if value == 0 {
		return 100, nil
	}
	if value < 0 || value > 1000 {
		return 0, status.Error(codes.InvalidArgument, "limit must be between 1 and 1000")
	}
	return int(value), nil
}

func (s *locationService) configuration(root string, exclusions *entity.IgnoreRules) (string, *entity.IgnoreRules, error) {
	// New registrations must be accessible, safe descendants of the configured source boundary.
	canonical, err := s.api.exe.LocationRoot(root)
	if err != nil {
		return "", nil, err
	}
	normalized, err := library.NormalizeIgnoreRules(exclusions)
	if err != nil {
		return "", nil, err
	}

	return canonical, normalized, nil
}

func (s *locationService) Create(ctx context.Context, req *entity.CreateLocationRequest) (*entity.CreateLocationResponse, error) {
	// Validate all access constraints before changing catalog state.
	if req == nil || req.Location == nil {
		return nil, status.Error(codes.InvalidArgument, "Location source request is missing")
	}
	root, exclusions, err := s.configuration(req.Location.RootPath, locationIgnore(req.Location.Config.GetIgnore()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	source := &library.Location{Name: req.Location.Name, ExecutorID: "local", RootPath: root, RestoreTarget: req.Location.RestoreTarget,
		Config: &entity.LocationConfig{Ignore: exclusions, UseMmap: req.Location.Config.GetUseMmap()}}

	// Unique root conflicts cannot overwrite an existing registration or index.
	if err := s.api.lib.CreateLocation(ctx, source); err != nil {
		return nil, apiError(err)
	}
	reply := s.reply(ctx, source)
	return &entity.CreateLocationResponse{Location: reply.Location, Accessibility: reply.Accessibility, Warnings: reply.Warnings}, nil
}

func (s *locationService) List(ctx context.Context, req *entity.ListLocationsRequest) (*entity.ListLocationsResponse, error) {
	// Catalog pages intentionally exclude expensive per-source filesystem probes.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location list request is missing")
	}
	limit, err := pageLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	rows, more, err := s.api.lib.ListLocations(ctx, library.LocationListFilter{
		AfterID: req.AfterId, Limit: limit, RestoreTarget: req.RestoreTarget, Query: req.Query,
	})
	if err != nil {
		return nil, apiError(err)
	}

	// Preserve ID order for a stable list cursor.
	reply := &entity.ListLocationsResponse{HasMore: more, Locations: make([]*entity.Location, 0, len(rows))}
	for _, row := range rows {
		reply.Locations = append(reply.Locations, row.CatalogEntity())
	}
	return reply, nil
}

func (s *locationService) Get(ctx context.Context, req *entity.GetLocationRequest) (*entity.GetLocationResponse, error) {
	// Resolve the requested registration before observing its accessibility.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location get request is missing")
	}
	source, err := s.api.lib.GetLocation(ctx, req.Id)
	if err != nil {
		return nil, apiError(err)
	}
	return s.reply(ctx, source), nil
}

func (s *locationService) Update(ctx context.Context, req *entity.UpdateLocationRequest) (*entity.UpdateLocationResponse, error) {
	// Name-only changes remain possible while the bound filesystem is unavailable.
	if req == nil || req.Location == nil {
		return nil, status.Error(codes.InvalidArgument, "Location update request is missing")
	}
	stored, err := s.api.lib.GetLocation(ctx, req.Location.Id)
	if err != nil {
		return nil, apiError(err)
	}
	exclusions, err := library.NormalizeIgnoreRules(locationIgnore(req.Location.Config.GetIgnore()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	root := req.Location.RootPath
	if stored.RootPath != root || !proto.Equal(stored.Config.GetIgnore(), exclusions) {
		root, exclusions, err = s.configuration(root, exclusions)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	// The update changes configuration without touching cached positions.
	stored.Name, stored.RootPath = req.Location.Name, root
	stored.Config = &entity.LocationConfig{Ignore: exclusions, UseMmap: req.Location.Config.GetUseMmap()}
	stored.RestoreTarget = req.Location.RestoreTarget
	updated, err := s.api.lib.UpdateLocation(ctx, stored)
	if err != nil {
		return nil, apiError(err)
	}
	reply := s.reply(ctx, updated)
	return &entity.UpdateLocationResponse{Location: reply.Location, Accessibility: reply.Accessibility, Warnings: reply.Warnings}, nil
}

func (s *locationService) Delete(ctx context.Context, req *entity.DeleteLocationRequest) (*entity.DeleteLocationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Location delete request is missing")
	}
	result, err := s.api.lib.DeleteLocation(ctx, req.Id, req.Dryrun)
	if err != nil {
		return nil, apiError(err)
	}
	return &entity.DeleteLocationResponse{LocationCount: result.Locations, OriginalCount: result.Originals}, nil
}

func locationIgnore(rules *entity.IgnoreRules) *entity.IgnoreRules {
	format := rules.GetFormat()
	if format == "" {
		format = "gitignore"
	}
	return &entity.IgnoreRules{Format: format, Text: rules.GetText()}
}

func (s *locationService) reply(ctx context.Context, source *library.Location) *entity.GetLocationResponse {
	// Accessibility is an observation with a timestamp, independent of the registration.
	reply := &entity.GetLocationResponse{
		Location: source.CatalogEntity(), Accessibility: &entity.LocationAccessibility{CheckedAtNs: time.Now().UnixNano()},
	}
	_, err := s.api.exe.CheckLocation(source)
	reply.Accessibility.Accessible = err == nil
	if err != nil {
		reply.Accessibility.Error = err.Error()
	}

	// Warn about overlap without materializing all sources or treating duplicate paths as archive copies.
	var after int64
	for {
		rows, more, err := s.api.lib.ListLocations(ctx, library.LocationListFilter{AfterID: after, Limit: 100})
		if err != nil {
			reply.Warnings = append(reply.Warnings, fmt.Sprintf("Cannot check nested Locations: %s", err))
			break
		}
		for _, other := range rows {
			if other.ID == source.ID {
				continue
			}
			separator := string(filepath.Separator)
			if strings.HasPrefix(source.RootPath, strings.TrimSuffix(other.RootPath, separator)+separator) || strings.HasPrefix(other.RootPath, strings.TrimSuffix(source.RootPath, separator)+separator) {
				reply.Warnings = append(reply.Warnings, "Overlapping Locations may index the same files; originals in a Location are not independent archive copies.")
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
