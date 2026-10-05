package apis

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPreviewSettingsDefaultWithoutWriteAndOmissionPreservesStoredValue(t *testing.T) {
	// Reading each typed group uses the common service without persisting its defaults.
	ctx := context.Background()
	api, _, _ := setupLocationAPI(t)
	service := &settingsService{api: api}
	libraryBefore, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_LIBRARY})
	require.NoError(t, err)
	beforeValue, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_PREVIEW})
	require.NoError(t, err)
	before := beforeValue.GetValue().GetPreview()
	require.NotEmpty(t, before.GetGenerators())
	stored, err := api.lib.Settings().Preview.Read(ctx)
	require.NoError(t, err)
	require.False(t, stored.Present, "reading defaults must not persist a migration")

	// An editor reads the group, changes what it shows and writes the complete value back.
	custom := proto.Clone(before).(*entity.PreviewSettings)
	custom.Generators[0].Extensions = []*entity.PreviewExtension{{Name: "jpg", Enabled: true}}
	custom.Enabled = true
	saved, err := service.Update(ctx, &entity.UpdateSettingsRequest{Value: &entity.SettingsValue{Value: &entity.SettingsValue_Preview{Preview: custom}}})
	require.NoError(t, err)
	require.True(t, proto.Equal(custom, saved.GetValue().GetPreview()))

	// The saved group is what the next read returns, and the Library group never carries it.
	current, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_PREVIEW})
	require.NoError(t, err)
	require.True(t, proto.Equal(saved.GetValue().GetPreview(), current.GetValue().GetPreview()))
	library, err := service.Get(ctx, &entity.GetSettingsRequest{Group: entity.SettingsGroup_SETTINGS_GROUP_LIBRARY})
	require.NoError(t, err)
	require.True(t, proto.Equal(libraryBefore, library))
}
