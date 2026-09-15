package executor

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTemporaryResourceProtectionIsConcurrentAndScoped(t *testing.T) {
	// Only an active attempt's random name is excluded; ordinary hidden paths remain ordinary.
	exe := &Executor{}
	_, err := exe.ProtectTemporaryNames(".notes")
	require.Error(t, err)
	require.False(t, exe.isTemporaryResource("/data/.notes/file.txt"))
	var readers sync.WaitGroup
	failures := make(chan error, 8)
	for index := 0; index < 8; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for attempt := 0; attempt < 30; attempt++ {
				name := ".yatm-restore-" + uuid.NewString()
				release, err := exe.ProtectTemporaryNames(name)
				if err != nil {
					failures <- err
					return
				}
				protected := exe.isTemporaryResource(filepath.Join("/data", name, "nested", "file.txt"))
				release()
				if !protected || exe.isTemporaryResource(filepath.Join("/data", name)) {
					failures <- fmt.Errorf("temporary resource protection did not follow attempt lifetime")
					return
				}
			}
		}()
	}
	readers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Empty(t, exe.temporaryNames)
}

func TestTemporaryFileOperationProtectionUsesOnlyScopedNames(t *testing.T) {
	exe := &Executor{}
	for _, invalid := range []string{".yatm-fileops-", ".yatm-other-" + uuid.NewString(), ".yatm-fileops-" + uuid.NewString() + "/child"} {
		_, err := exe.ProtectTemporaryNames(invalid)
		require.Error(t, err)
	}
	name := ".yatm-fileops-" + uuid.NewString() + "-"
	release, err := exe.ProtectTemporaryNames(name)
	require.NoError(t, err)
	filename := filepath.Join("/data", name+"generated", "manifest.db")
	require.True(t, exe.isTemporaryResource(filename))
	require.False(t, exe.isTemporaryResource("/data/.yatm-fileops-not-an-active-operation/file"))
	release()
	require.False(t, exe.isTemporaryResource(filename))
}
