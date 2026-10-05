package apis

import (
	"context"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLocationAccessibilityUsesNanoseconds(t *testing.T) {
	// Registration, inspection and update all observe accessibility with the current runtime clock.
	_, service, root := setupLocationAPI(t)
	ctx := context.Background()
	before := time.Now().UnixNano()
	created, err := service.Create(ctx, &entity.CreateLocationRequest{
		Location: &entity.Location{Name: "Originals", RootPath: root},
	})
	require.NoError(t, err)
	inspected, err := service.Get(ctx, &entity.GetLocationRequest{Id: created.Location.Id})
	require.NoError(t, err)
	updated, err := service.Update(ctx, &entity.UpdateLocationRequest{Location: created.Location})
	require.NoError(t, err)
	after := time.Now().UnixNano()

	// Millisecond or second values cannot satisfy the actual observation interval in ns.
	for _, accessibility := range []*entity.LocationAccessibility{
		created.Accessibility, inspected.Accessibility, updated.Accessibility,
	} {
		require.True(t, accessibility.Accessible)
		require.GreaterOrEqual(t, accessibility.CheckedAtNs, before)
		require.LessOrEqual(t, accessibility.CheckedAtNs, after)
	}
}
