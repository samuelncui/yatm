package preview

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/previewprotocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type nativeWaitingContext struct {
	context.Context
	started chan struct{}
	once    sync.Once
}

func (c *nativeWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.started) })
	return c.Context.Done()
}

func TestRunNativeRejectsInvalidProtocolStreams(t *testing.T) {
	for _, test := range []struct {
		name string
		mode string
		want string
	}{
		{name: "malformed", mode: "malformed", want: "decode Preview helper event failed"},
		{name: "oversized", mode: "oversized", want: "token too long"},
		{name: "protocol mismatch", mode: "mismatch", want: "invalid Preview helper protocol"},
		{name: "missing terminal", mode: "missing", want: "returned no result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Drive the parent boundary with controlled child output instead of a real decoder.
			helper := writeNativeFixture(t)
			t.Setenv("YATM_NATIVE_FIXTURE_MODE", test.mode)
			_, err := runNative(context.Background(), helper, &previewprotocol.Request{Protocol: previewprotocol.Version})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestRunNativeCancelsStalledHelper(t *testing.T) {
	// Cancellation must terminate a helper that never emits a terminal event.
	helper := writeNativeFixture(t)
	t.Setenv("YATM_NATIVE_FIXTURE_MODE", "stall")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := runNative(ctx, helper, &previewprotocol.Request{Protocol: previewprotocol.Version})
	require.Error(t, err)
}

func TestRunNativeReportsProgressAndAssets(t *testing.T) {
	// A valid JSONL sequence forwards progress and preserves helper asset metadata.
	helper := writeNativeFixture(t)
	t.Setenv("YATM_NATIVE_FIXTURE_MODE", "result")
	var progress []previewprotocol.Event
	ctx := WithProgress(context.Background(), func(event previewprotocol.Event) {
		progress = append(progress, event)
	})
	dir := t.TempDir()
	writeNativePNG(t, filepath.Join(dir, "thumbnail.png"), 8, 6)
	assets, err := generateNative(ctx, helper, previewprotocol.Request{Protocol: previewprotocol.Version,
		Kind: "image", Format: "png", MaxWidth: 8, MaxHeight: 6,
		SourcePath: filepath.Join(dir, "source.png"), OutputDir: dir})
	require.NoError(t, err)
	require.Equal(t, []previewprotocol.Event{{Protocol: previewprotocol.Version, Type: "progress", Phase: "opening"}}, progress)
	require.Equal(t, []*Asset{{Name: "thumbnail.png", Role: "thumbnail", MediaType: "image/png", Width: 8, Height: 6}}, assets)
}

func TestGenerateNativeRejectsEmptyPaths(t *testing.T) {
	for _, request := range []previewprotocol.Request{
		{SourcePath: "source.png"},
		{OutputDir: "previews"},
		{},
	} {
		// Empty paths must not become the working directory or start a helper.
		_, err := generateNative(context.Background(), "unavailable-preview-helper", request)
		require.ErrorContains(t, err, "source and output paths are required")
	}
}

func TestGenerationScheduleSharesWorkAndRechecksLiveDisablement(t *testing.T) {
	// Hold the sole active slot so a different key waits for the live settings recheck.
	var lock sync.Mutex
	settings, err := SettingsFromConfig(Config{})
	require.NoError(t, err)
	settings.Enabled, settings.Concurrency = true, 1
	enabled := proto.Clone(settings).(*entity.PreviewSettings)
	manager := &Manager{pending: make(map[string]*generation), changed: make(chan struct{})}
	manager.settings = func(context.Context) (*entity.PreviewSettings, error) {
		lock.Lock()
		defer lock.Unlock()
		return settings, nil
	}
	first, owner, err := manager.beginGeneration(context.Background(), "first", "source.png", "image")
	require.NoError(t, err)
	require.True(t, owner)

	// A separate key remains queued at capacity and is rejected once master enablement changes.
	type result struct {
		pending *generation
		owner   bool
		err     error
	}
	queued := make(chan result, 1)
	go func() {
		pending, owner, err := manager.beginGeneration(context.Background(), "second", "source.png", "image")
		queued <- result{pending: pending, owner: owner, err: err}
	}()
	select {
	case outcome := <-queued:
		t.Fatalf("queued generation started early: %+v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
	lock.Lock()
	settings = &entity.PreviewSettings{Concurrency: 1}
	lock.Unlock()
	manager.finishGeneration("first", first, true, nil)
	outcome := <-queued
	require.ErrorIs(t, outcome.err, ErrDisabled)
	require.Nil(t, outcome.pending)
	require.False(t, outcome.owner)

	// Identical keys share a completed result without consuming another active slot.
	lock.Lock()
	settings = enabled
	lock.Unlock()
	pending, owner, err := manager.beginGeneration(context.Background(), "shared", "source.png", "image")
	require.NoError(t, err)
	require.True(t, owner)
	waiting := &nativeWaitingContext{Context: context.Background(), started: make(chan struct{})}
	shared := make(chan result, 1)
	go func() {
		waited, owner, err := manager.beginGeneration(waiting, "shared", "source.png", "image")
		shared <- result{pending: waited, owner: owner, err: err}
	}()
	<-waiting.started
	manager.finishGeneration("shared", pending, true, nil)
	outcome = <-shared
	require.NoError(t, outcome.err)
	require.Same(t, pending, outcome.pending)
	require.False(t, outcome.owner)
}

func TestGenerationScheduleReacquiresIncompatibleOrFailedWork(t *testing.T) {
	for _, test := range []struct {
		name, identity string
		err            error
	}{
		{name: "force does not reuse missing-only", identity: "force:png"},
		{name: "format does not reuse other format", identity: "missing:jpeg"},
		{name: "failed source does not poison another", identity: "missing:png", err: errors.New("source unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &Manager{pending: make(map[string]*generation), changed: make(chan struct{})}
			first, _, err := manager.beginGeneration(context.Background(), "content", "first.png", "missing:png")
			require.NoError(t, err)
			waiting := &nativeWaitingContext{Context: context.Background(), started: make(chan struct{})}
			done := make(chan struct{})
			var next *generation
			var owner bool
			var nextErr error
			go func() {
				next, owner, nextErr = manager.beginGeneration(waiting, "content", "second.png", test.identity)
				close(done)
			}()
			<-waiting.started
			manager.finishGeneration("content", first, true, test.err)
			<-done
			require.NoError(t, nextErr)
			require.True(t, owner)
			require.NotSame(t, first, next)
			manager.finishGeneration("content", next, true, nil)
		})
	}
}

func TestNativeImageValidatesActualHeader(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "thumbnail.png")
	request := previewprotocol.Request{Format: "png", MaxWidth: 8, MaxHeight: 6, OutputDir: dir}
	asset := previewprotocol.Asset{Name: "thumbnail.png", Role: "thumbnail", MediaType: "image/png", Width: 8, Height: 6}
	writeNativePNG(t, filename, 8, 6)
	require.NoError(t, validateNativeImage(context.Background(), request, asset))
	asset.Width = 7
	require.ErrorContains(t, validateNativeImage(context.Background(), request, asset), "does not match")
	asset.Width, asset.MediaType = 8, "text/html"
	require.ErrorContains(t, validateNativeImage(context.Background(), request, asset), "invalid image metadata")
	asset.MediaType = "image/png"
	require.NoError(t, os.WriteFile(filename, []byte("not an image"), 0o600))
	require.ErrorContains(t, validateNativeImage(context.Background(), request, asset), "image header")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, validateNativeImage(ctx, request, asset), context.Canceled)
}

func writeNativePNG(t *testing.T, filename string, width, height int) {
	t.Helper()
	file, err := os.Create(filename)
	require.NoError(t, err)
	require.NoError(t, png.Encode(file, image.NewRGBA(image.Rect(0, 0, width, height))))
	require.NoError(t, file.Close())
}

type nativeCancelAtCheck struct {
	context.Context
	checks, cancelAt int
}

func (c *nativeCancelAtCheck) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestBundlePublicationCancellationPreservesExistingContent(t *testing.T) {
	for _, test := range []struct {
		name      string
		cancelAt  int
		withAsset bool
	}{
		{name: "before work", cancelAt: 1},
		{name: "during streaming", cancelAt: 2, withAsset: true},
		{name: "before rename", cancelAt: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			bundle := filepath.Join(dir, "existing.zip")
			require.NoError(t, os.WriteFile(bundle, []byte("existing bundle"), 0o600))
			manifest := &entity.PreviewManifest{}
			if test.withAsset {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "thumbnail.png"), make([]byte, 65536), 0o600))
				manifest.Assets = []*entity.PreviewAsset{{Name: "thumbnail.png", Role: "thumbnail", MediaType: "image/png"}}
			}
			ctx := &nativeCancelAtCheck{Context: context.Background(), cancelAt: test.cancelAt}
			require.ErrorIs(t, publishBundle(ctx, bundle, dir, manifest), context.Canceled)
			data, err := os.ReadFile(bundle)
			require.NoError(t, err)
			require.Equal(t, "existing bundle", string(data))
			temporary, err := filepath.Glob(filepath.Join(dir, ".bundle-*"))
			require.NoError(t, err)
			require.Empty(t, temporary)
		})
	}
}

func TestGenerationScheduleRejectsDisabledRoute(t *testing.T) {
	settings, err := SettingsFromConfig(Config{})
	require.NoError(t, err)
	settings.Enabled = true
	for _, generator := range settings.Generators {
		generator.Enabled = false
	}
	manager := &Manager{pending: make(map[string]*generation), changed: make(chan struct{}),
		settings: func(context.Context) (*entity.PreviewSettings, error) { return settings, nil }}
	pending, owner, err := manager.beginGeneration(context.Background(), "content", "source.png", "image")
	require.ErrorIs(t, err, ErrDisabled)
	require.Nil(t, pending)
	require.False(t, owner)
	require.Empty(t, manager.pending)
}

func writeNativeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "native-fixture")
	script := `#!/bin/sh
case "$YATM_NATIVE_FIXTURE_MODE" in
malformed) printf '{bad json}\n' ;;
oversized) dd if=/dev/zero bs=1048577 count=1 2>/dev/null | tr '\000' x; printf '\n' ;;
mismatch) printf '{"protocol":2,"type":"result"}\n' ;;
missing) printf '{"protocol":1,"type":"progress","phase":"opening"}\n' ;;
stall) sleep 5 ;;
result)
  printf '{"protocol":1,"type":"progress","phase":"opening"}\n'
  printf '{"protocol":1,"type":"result","assets":[{"name":"thumbnail.png","role":"thumbnail","media_type":"image/png","width":8,"height":6}]}\n'
  ;;
esac
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}
