package scan

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestCreateFreezesPreviewSettingsBeforeLaterLibraryEdit(t *testing.T) {
	ctx := context.Background()
	exe, location := setupAnalyzeWithPreview(t, &scanPreviewer{})
	settings, err := exe.Lib().Settings().Preview.Current(ctx)
	require.NoError(t, err)
	settings.Generators = []*entity.PreviewGeneratorSettings{{
		Enabled: true, Extensions: []*entity.PreviewExtension{{Name: "jpg", Enabled: true}},
		Options: &entity.PreviewGeneratorSettings_Image{Image: &entity.ImagePreviewSettings{
			MaxWidth: 240, MaxHeight: 240, Format: "webp", Quality: 80,
		}},
	}}
	_, err = exe.Lib().Settings().Preview.Save(ctx, settings)
	require.NoError(t, err)
	reply, err := Create(ctx, exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{Selections: scanLocationSelections(location.ID), PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY}})
	require.NoError(t, err)

	settings.Generators[0].Extensions = []*entity.PreviewExtension{{Name: "png", Enabled: true}}
	_, err = exe.Lib().Settings().Preview.Save(ctx, settings)
	require.NoError(t, err)
	raw, err := exe.GetJobRunner(ctx, reply.Job.Id)
	require.NoError(t, err)
	var config Config
	require.NoError(t, raw.(*runner).db.First(&config, 1).Error)
	require.Equal(t, []*entity.PreviewExtension{{Name: "jpg", Enabled: true}}, config.PreviewJobSettings.GetGenerators()[0].GetExtensions())
	require.Eventually(t, func() bool { return !exe.IsRunning(reply.Job.Id) }, 15*time.Second, 10*time.Millisecond)
}
