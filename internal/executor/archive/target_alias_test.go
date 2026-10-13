package archive

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

func TestArchivePublishesOnlyCompletedNativeTargets(t *testing.T) {
	// Library names can alias on the destination while the originals live at distinct native paths.
	for _, names := range [][2]string{{"Target", "target"}, {"caf\u00e9", "cafe\u0301"}} {
		for _, kind := range []entity.VolumeType{entity.VolumeType_VOLUME_TYPE_HDD, entity.VolumeType_VOLUME_TYPE_HM_SMR} {
			t.Run(names[0]+"/"+kind.String(), func(t *testing.T) {
				ctx := context.Background()
				exe := setupTestExecutor(t, executor.Scripts{})
				contents := map[string][]byte{names[0]: []byte("first"), names[1]: []byte("second")}
				var ids []int64
				for i, source := range []string{"first", "second"} {
					_, file, _ := publishArchiveOriginal(t, exe, source, contents[names[i]])
					file.ParentID, file.Name = library.Root.ID, names[i]
					require.NoError(t, exe.Lib().SaveFile(ctx, file))
					ids = append(ids, file.ID)
				}
				created, err := (&service{exe: exe}).Create(ctx, &entity.CreateArchiveJobRequest{
					Spec: &entity.ArchiveJobSpec{Selections: archiveSelections(ids...)},
				})
				require.NoError(t, err)
				waitIndexed(t, exe, created.Job.Id)
				value, err := exe.GetJobRunner(ctx, created.Job.Id)
				require.NoError(t, err)
				runner := value.(*jobArchiveRunner)

				// Detect the fixture filesystem's rules, then use the ordinary Volume transfer unchanged.
				root := filepath.Join(exe.Paths().Volumes[0], "archive")
				require.NoError(t, os.Mkdir(root, 0o755))
				probe := filepath.Join(root, names[0])
				require.NoError(t, os.WriteFile(probe, []byte("probe"), 0o600))
				first, err := os.Stat(probe)
				require.NoError(t, err)
				second, err := os.Stat(filepath.Join(root, names[1]))
				require.True(t, err == nil || os.IsNotExist(err))
				alias := err == nil && os.SameFile(first, second)
				require.NoError(t, os.Remove(probe))
				volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{Type: kind})
				require.NoError(t, err)
				stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
					Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Archive",
					Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
				})
				require.NoError(t, err)
				_, err = (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
					Id: created.Job.Id, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Marker.UUID}).Pack(),
				})
				require.NoError(t, err)
				require.Eventually(t, func() bool { return !exe.IsRunning(created.Job.Id) }, 15*time.Second, 10*time.Millisecond)

				// Every published Position must match its physical bytes; a refused alias stays pending.
				var items []Item
				require.NoError(t, runner.db.Order("target_path").Find(&items).Error)
				require.Len(t, items, 2)
				positions, err := exe.Lib().ListMediaFilePositions(ctx, stored.ID, "", 100)
				require.NoError(t, err)
				want := 2
				if alias {
					want = 1
				}
				require.Len(t, positions, want)
				submitted := 0
				for _, item := range items {
					versions, _, err := exe.Lib().ListFileVersions(ctx, item.Data.Expected.FileId, 0, 10)
					require.NoError(t, err)
					if item.Status == entity.CopyStatus_COPY_STATUS_PENDING {
						require.Empty(t, versions)
						continue
					}
					require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, item.Status)
					submitted++
					actual, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.MediaPath)))
					require.NoError(t, err)
					require.Equal(t, contents[item.TargetPath], actual)
					require.Len(t, versions, 1)
					hash := sha256.Sum256(actual)
					require.Equal(t, hash[:], versions[0].Hash)
					found := false
					for _, position := range positions {
						if position.Path == item.MediaPath {
							found = true
							require.Equal(t, hash[:], position.Hash)
						}
					}
					require.True(t, found, "submitted item has no matching Position")
				}
				require.Equal(t, want, submitted)
				if alias && kind == entity.VolumeType_VOLUME_TYPE_HM_SMR {
					require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, items[0].Status, "linear targets retain the first exclusive winner")
				}
			})
		}
	}
}
