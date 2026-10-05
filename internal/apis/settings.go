package apis

import (
	"context"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type settingsService struct {
	entity.UnimplementedSettingsServiceServer
	api *API
}

func (s *settingsService) Get(ctx context.Context, req *entity.GetSettingsRequest) (*entity.GetSettingsResponse, error) {
	// Resolve the requested typed group through the shared Settings module.
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "Settings request is missing")
	}
	switch req.GetGroup() {
	case entity.SettingsGroup_SETTINGS_GROUP_LIBRARY:
		value, err := s.api.lib.Settings().Library.Current(ctx)
		if err != nil {
			return nil, apiError(err)
		}
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Library{Library: value}}}, nil
	case entity.SettingsGroup_SETTINGS_GROUP_PREVIEW:
		value, err := s.api.lib.Settings().Preview.Current(ctx)
		if err != nil {
			return nil, apiError(err)
		}
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Preview{Preview: value}}}, nil
	case entity.SettingsGroup_SETTINGS_GROUP_JOB:
		value, err := s.api.lib.Settings().Job.Current(ctx)
		if err != nil {
			return nil, apiError(err)
		}
		return &entity.GetSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Job{Job: value}}}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Settings group is missing")
	}
}

func (s *settingsService) Update(ctx context.Context, req *entity.UpdateSettingsRequest) (*entity.UpdateSettingsResponse, error) {
	// Replace exactly the typed group supplied by the caller.
	if req == nil || req.Value == nil {
		return nil, status.Error(codes.InvalidArgument, "Settings value is missing")
	}
	switch value := req.Value.GetValue().(type) {
	case *entity.SettingsValue_Library:
		saved, err := s.api.lib.Settings().Library.Save(ctx, value.Library)
		if err != nil {
			return nil, apiError(err)
		}
		return &entity.UpdateSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Library{Library: saved}}}, nil
	case *entity.SettingsValue_Preview:
		saved, err := s.api.lib.Settings().Preview.Save(ctx, value.Preview)
		if err != nil {
			return nil, apiError(err)
		}
		return &entity.UpdateSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Preview{Preview: saved}}}, nil
	case *entity.SettingsValue_Job:
		saved, err := s.api.lib.Settings().Job.Save(ctx, value.Job)
		if err != nil {
			return nil, apiError(err)
		}
		return &entity.UpdateSettingsResponse{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Job{Job: saved}}}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Settings value is missing")
	}
}
