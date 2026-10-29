package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (s *orderedRestoreSession) ReadPlanningBudget() time.Duration { return s.planningBudget }

func TestRestoreReadPlanningBudgetKeepsManifestAndVerifiedReuse(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete manifest", true: "settled local output"}[reuse], func(t *testing.T) {
			// Expiring during index construction cannot truncate the original selected-Media manifest.
			runner, copies, session := restoreOrderFixture(t, "a", "b", "c")
			session.planningBudget = time.Nanosecond
			if reuse {
				// Fixture Copy insertion follows a map; bind reuse by its frozen path, not slice position.
				var target string
				for _, copy := range copies {
					if copy.MediaPath == "a" {
						target = copy.TargetPath
						break
					}
				}
				require.NotEmpty(t, target)
				path := runner.restoreTarget(target)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
				require.NoError(t, os.WriteFile(path, []byte("a"), 0600))
			}

			// Budget fallback leaves native reads available and does not repeat a settled reuse action.
			require.NoError(t, runOrderedRestore(t, context.Background(), runner, copies, session, 1))
			want := []string{"a", "b", "c"}
			if reuse {
				want = want[1:]
			}
			require.Equal(t, want, session.opened)
			require.Zero(t, session.observations)
		})
	}
}

func TestRestoreReadPlanningBudgetKeepsSourceFailureBoundary(t *testing.T) {
	// Soft planning expiry leaves the ordinary physical read failure and acquired-reader drain intact.
	runner, copies, session := restoreOrderFixture(t, "a", "b", "c")
	session.planningBudget = time.Nanosecond
	failure := errors.New("physical read failed after optional planning")
	session.readErr = failure
	require.ErrorIs(t, runOrderedRestore(t, context.Background(), runner, copies, session, 2), failure)
	require.Equal(t, []string{"a"}, session.opened)
	require.Equal(t, 1, session.closes)
	require.Zero(t, session.observations)
}
