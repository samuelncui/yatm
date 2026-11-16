package preview

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	blockingCalls   int64
	blockingStarted = make(chan struct{}, 1)
	blockingRelease = make(chan struct{})
)

type blockingGenerator struct{ testGenerator }

func (g *blockingGenerator) Generate(ctx context.Context, source string, outputDir string) ([]*Asset, error) {
	blockingStarted <- struct{}{}
	<-blockingRelease
	return g.testGenerator.Generate(ctx, source, outputDir)
}

func init() {
	RegisterGenerator("test-fixture-blocking", func(map[string]any) (Generator, error) {
		return &blockingGenerator{testGenerator{calls: &blockingCalls, marker: "blocking"}}, nil
	})
}

func TestMissingOnlyGenerationReusesTheBundleItWaitedFor(t *testing.T) {
	// A missing-only request queued behind a forced generation reuses its published bundle.
	root := t.TempDir()
	manager, err := New(Config{Root: root, Generators: []GeneratorConfig{{Kind: "test-fixture-blocking", Extensions: []string{"fixture"}}}}, t.TempDir())
	require.NoError(t, err)
	content := []byte("waiting source")
	sourcePath := filepath.Join(t.TempDir(), "source.fixture")
	require.NoError(t, os.WriteFile(sourcePath, content, 0o644))
	hash := sha256.Sum256(content)
	generate := func(force bool) chan error {
		done := make(chan error, 1)
		go func() {
			_, err := manager.Generate(context.Background(), sourcePath, hash[:], int64(len(content)), 0, force, nil)
			done <- err
		}()
		return done
	}

	forced := generate(true)
	<-blockingStarted
	waiting := generate(false)
	// Let the second request queue behind the running generation; arriving later only reuses earlier.
	time.Sleep(100 * time.Millisecond)
	close(blockingRelease)
	require.NoError(t, <-forced)
	require.NoError(t, <-waiting)
	require.Equal(t, int64(1), atomic.LoadInt64(&blockingCalls))
}
