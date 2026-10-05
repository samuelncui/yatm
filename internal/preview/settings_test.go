package preview

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestNewWithSettingsUsesTypedDefaultsAndLiveGetter(t *testing.T) {
	// Construction freezes output routes while later calls keep reading runtime controls.
	stored, err := SettingsFromConfig(Config{Generators: []GeneratorConfig{{
		Kind: "image", Extensions: []string{"jpg"},
		Options: map[string]any{"command": "legacy-ffmpeg", "max_width": 240, "max_height": 200, "quality": 60},
	}}})
	require.NoError(t, err)
	manager, err := NewWithSettings(context.Background(), t.TempDir(), "", func(context.Context) (*entity.PreviewSettings, error) {
		return proto.Clone(stored).(*entity.PreviewSettings), nil
	})
	require.NoError(t, err)

	require.True(t, manager.Supports("source.jpg", nil))
	require.False(t, manager.Supports("source.png", nil))
	image := stored.GetGenerators()[0].GetImage()
	require.EqualValues(t, 240, image.MaxWidth)
	require.EqualValues(t, 200, image.MaxHeight)
	require.EqualValues(t, 60, image.Quality)
}

func TestValidateSettingsRejectsMissingAndUnboundedRoutes(t *testing.T) {
	// Scheduling limits are required even when the master Preview switch is off.
	require.ErrorContains(t, ValidateSettings(&entity.PreviewSettings{}), "concurrency must be between 1 and 16")
	settings, err := SettingsFromConfig(Config{})
	require.NoError(t, err)
	settings.Generators[0].Options = nil
	require.ErrorContains(t, ValidateSettings(settings), "options are missing")

	// Generator dimensions remain bounded independently from extension enablement.
	settings, err = SettingsFromConfig(Config{})
	require.NoError(t, err)
	settings.Generators[0].GetImage().MaxWidth = maxPreviewDimension + 1
	require.ErrorContains(t, ValidateSettings(settings), "dimensions exceed limit")
}

func TestSettingsFromConfigDefaultsToDisabledMasterAndEnabledRoutes(t *testing.T) {
	// Defaults preserve configured categories for later activation without starting generation.
	settings, err := SettingsFromConfig(Config{})
	require.NoError(t, err)
	require.False(t, settings.GetEnabled())
	require.EqualValues(t, 2, settings.GetConcurrency())
	require.EqualValues(t, 600, settings.GetTimeoutSeconds())
	require.EqualValues(t, 64_000_000, settings.GetMaxInputPixels())
	require.Equal(t, "", settings.GetCommand())
	require.Equal(t, []string{"jpg", "jpeg", "png", "webp", "heic", "dng", "cr2", "cr3", "nef", "nrw", "arw", "raf", "orf", "rw2", "pef", "srw"},
		extensionNames(settings.GetGenerators()[0].GetExtensions()))
	require.Equal(t, []string{"mp4", "mov", "mkv", "avi"},
		extensionNames(settings.GetGenerators()[1].GetExtensions()))
	for _, generator := range settings.GetGenerators() {
		require.True(t, generator.GetEnabled())
		for _, extension := range generator.GetExtensions() {
			require.NotEmpty(t, extension.GetName())
			require.True(t, extension.GetEnabled())
		}
	}

	// Video output defaults survive conversion into typed Settings and back into generator routes.
	video := settings.GetGenerators()[1].GetVideo()
	require.EqualValues(t, 320, video.GetTimelineWidth())
	require.EqualValues(t, 180, video.GetTimelineHeight())
	require.EqualValues(t, 10, video.GetTimelineIntervalSeconds())
	require.EqualValues(t, 30, video.GetTimelineMaxFrames())
	configs, err := generatorConfigs(settings)
	require.NoError(t, err)
	routes, err := loadGenerators(configs)
	require.NoError(t, err)
	options := routes["mp4"].generator.(*videoGenerator).options
	require.Equal(t, 320, options.TimelineWidth)
	require.Equal(t, 180, options.TimelineHeight)
	require.Equal(t, 10, options.TimelineIntervalSecond)
	require.Equal(t, 30, options.TimelineMaxFrames)
}

func TestSettingsFromConfigCreatesIndependentDefaults(t *testing.T) {
	// Resolve separate defaults through YATM rather than testing protobuf's clone operation.
	settings, err := SettingsFromConfig(Config{})
	require.NoError(t, err)
	other, err := SettingsFromConfig(Config{})
	require.NoError(t, err)

	// Editing one result must not change another caller's extension or output preferences.
	settings.Generators[0].Extensions[0].Name = "changed"
	settings.Generators[1].GetVideo().TimelineWidth = 1
	require.Equal(t, "jpg", other.Generators[0].Extensions[0].Name)
	require.EqualValues(t, 320, other.Generators[1].GetVideo().TimelineWidth)
}

func extensionNames(extensions []*entity.PreviewExtension) []string {
	names := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		names = append(names, extension.GetName())
	}
	return names
}
