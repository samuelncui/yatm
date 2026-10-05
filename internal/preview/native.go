package preview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/previewprotocol"
	_ "golang.org/x/image/webp"
)

type progressKey struct{}

// WithProgress observes bounded worker phases without coupling Preview to a Job logger.
func WithProgress(ctx context.Context, use func(previewprotocol.Event)) context.Context {
	return context.WithValue(ctx, progressKey{}, use)
}

func reportProgress(ctx context.Context, event previewprotocol.Event) {
	if use, ok := ctx.Value(progressKey{}).(func(previewprotocol.Event)); ok {
		use(event)
	}
}

func helperCommand(configured string) (string, error) {
	if configured != "" {
		return exec.LookPath(configured)
	}
	// Prefer the optional helper installed beside the service; never substitute system FFmpeg.
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	filename := filepath.Join(filepath.Dir(executable), "yatm-preview")
	if _, err := os.Stat(filename); err == nil {
		return filename, nil
	}
	return exec.LookPath("yatm-preview")
}

func runNative(ctx context.Context, command string, request *previewprotocol.Request) (*previewprotocol.Event, error) {
	// Contain a single optional process; malformed output and cancellation kill it before waiting.
	name, err := helperCommand(command)
	if err != nil {
		return nil, fmt.Errorf("Preview helper unavailable: %w", err)
	}
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := []string{"--capabilities"}
	if request != nil {
		args = nil
	}
	process := exec.CommandContext(processCtx, name, args...)
	process.WaitDelay = time.Second
	if request != nil {
		encoded, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		process.Stdin = bytes.NewReader(append(encoded, '\n'))
	}
	stderr := &limitedBuffer{limit: commandOutputLimit}
	process.Stderr = stderr
	stdout, err := process.StdoutPipe()
	if err != nil {
		return nil, err
	}
	defer stdout.Close()

	// The scanner owns this read; cancellation must release it even if a child inherits stdout.
	stopRead := context.AfterFunc(processCtx, func() { _ = stdout.Close() })
	defer stopRead()
	if err := process.Start(); err != nil {
		return nil, fmt.Errorf("start Preview helper failed: %w", err)
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			_ = process.Wait()
		}
	}()

	// Read one bounded JSON line at a time; terminal output must be unique and protocol-matched.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var result *previewprotocol.Event
	var protocolErr error
	for count := 0; scanner.Scan(); count++ {
		var event previewprotocol.Event
		if count >= 8192 {
			protocolErr = fmt.Errorf("Preview helper emitted too many events")
			break
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			protocolErr = fmt.Errorf("decode Preview helper event failed: %w", err)
			break
		}
		if event.Protocol != previewprotocol.Version || result != nil {
			protocolErr = fmt.Errorf("invalid Preview helper protocol or terminal sequence")
			break
		}
		switch event.Type {
		case "progress":
			reportProgress(ctx, event)
		case "result", "error", "capabilities":
			result = &event
		default:
			protocolErr = fmt.Errorf("unknown Preview helper event %q", event.Type)
		}
		if protocolErr != nil {
			break
		}
	}
	if protocolErr == nil {
		protocolErr = scanner.Err()
	}
	if protocolErr != nil {
		cancel()
	}

	// Reap the helper and preserve caller cancellation when it closes an inherited pipe.
	waitErr := process.Wait()
	waited = true
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("Preview helper interrupted: %w", err)
	}
	if protocolErr != nil {
		return nil, protocolErr
	}
	if result != nil && result.Type == "error" {
		return nil, fmt.Errorf("Preview helper failed: %s", result.Error)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("Preview helper failed, stderr=%q: %w", stderr.String(), waitErr)
	}
	if result == nil {
		return nil, fmt.Errorf("Preview helper returned no result")
	}
	return result, nil
}

func generateNative(ctx context.Context, command string, request previewprotocol.Request) ([]*Asset, error) {
	// Resolve deployment-relative paths at the process boundary without changing stored configuration.
	if request.SourcePath == "" || request.OutputDir == "" {
		return nil, fmt.Errorf("Preview source and output paths are required")
	}
	var err error
	request.SourcePath, err = filepath.Abs(request.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolve Preview source path failed: %w", err)
	}
	request.OutputDir, err = filepath.Abs(request.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve Preview output path failed: %w", err)
	}

	// Validate encoded headers without decoding pixels before accepting worker metadata.
	result, err := runNative(ctx, command, &request)
	if err != nil {
		return nil, err
	}
	if result.Type != "result" || len(result.Assets) == 0 || len(result.Assets) > maxAssetCount {
		return nil, fmt.Errorf("Preview helper returned invalid assets")
	}
	assets := make([]*Asset, 0, len(result.Assets))
	expected := map[string]bool{"thumbnail": false}
	if request.Kind == "video" {
		expected = map[string]bool{"poster": false, "timeline": false, "timeline-map": false}
	}
	for _, asset := range result.Assets {
		seen, allowed := expected[asset.Role]
		if !allowed || seen || asset.Name == manifestName || asset.Name == "" || asset.Name == "." ||
			filepath.Base(asset.Name) != asset.Name || strings.ContainsAny(asset.Name, `/\\`) {
			return nil, fmt.Errorf("Preview helper returned an invalid asset role or name")
		}
		if asset.Role == "timeline-map" {
			if asset.MediaType != "text/vtt" {
				return nil, fmt.Errorf("Preview timeline map must be WebVTT")
			}
		} else if err := validateNativeImage(ctx, request, asset); err != nil {
			return nil, err
		}
		expected[asset.Role] = true
		assets = append(assets, &Asset{Name: asset.Name, Role: asset.Role, MediaType: asset.MediaType,
			Width: asset.Width, Height: asset.Height})
	}
	for role, present := range expected {
		if !present {
			return nil, fmt.Errorf("Preview helper omitted role %q", role)
		}
	}
	return assets, nil
}

func validateNativeImage(
	ctx context.Context, request previewprotocol.Request, asset previewprotocol.Asset,
) (returnErr error) {
	// Check declared bounds before reading an encoded header; never allocate its pixel buffer.
	if err := ctx.Err(); err != nil {
		return err
	}
	if asset.MediaType != "image/"+request.Format || asset.Width == 0 || asset.Height == 0 ||
		uint64(asset.Width)*uint64(asset.Height) > maxPreviewTimelinePixels {
		return fmt.Errorf("Preview helper returned invalid image metadata")
	}
	width, height := int(asset.Width), int(asset.Height)
	maxWidth, maxHeight := request.MaxWidth, request.MaxHeight
	switch asset.Role {
	case "poster":
		maxWidth, maxHeight = request.PosterWidth, request.PosterHeight
	case "timeline":
		if request.TimelineWidth < 1 || request.TimelineHeight < 1 || request.TimelineMaxFrames < 1 ||
			width%request.TimelineWidth != 0 || height%request.TimelineHeight != 0 ||
			(width/request.TimelineWidth)*(height/request.TimelineHeight) > request.TimelineMaxFrames+9 {
			return fmt.Errorf("Preview helper returned invalid timeline geometry")
		}
		maxWidth = min(request.TimelineMaxFrames, 10) * request.TimelineWidth
		maxHeight = ((request.TimelineMaxFrames + 9) / 10) * request.TimelineHeight
	}
	if width > maxWidth || height > maxHeight {
		return fmt.Errorf("Preview helper exceeded requested image dimensions")
	}

	// Inspect only the bounded encoded header and release its descriptor on every result.
	filename := filepath.Join(request.OutputDir, asset.Name)
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPreviewAssetSize {
		return fmt.Errorf("Preview helper returned an invalid image file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Preview image failed, %w", err))
		}
	}()
	config, format, err := image.DecodeConfig(io.LimitReader(&contextReader{ctx: ctx, reader: file}, 1<<20))
	if err != nil {
		return fmt.Errorf("read Preview image header: %w", err)
	}
	if format != request.Format || config.Width != width || config.Height != height {
		return fmt.Errorf("Preview image header does not match its metadata")
	}
	return ctx.Err()
}

// Capabilities probes the optional dependency without enabling generation or touching stored assets.
func (m *Manager) Capabilities(ctx context.Context) (*entity.GetPreviewCapabilitiesResponse, error) {
	// Resolve live command preferences, with a short probe timeout independent of file generation.
	settings, err := m.currentSettings(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runNative(ctx, settings.GetCommand(), nil)
	if err != nil {
		return &entity.GetPreviewCapabilitiesResponse{Reason: err.Error()}, nil
	}
	if result.Type != "capabilities" || result.Capabilities == nil {
		return &entity.GetPreviewCapabilitiesResponse{Reason: "Preview helper returned invalid capabilities"}, nil
	}
	c := result.Capabilities

	// Preserve provider-specific input groups for the controlled settings selector.
	inputs := make(map[string]*entity.PreviewInputFormats, len(c.InputExtensionsByKind))
	for kind, extensions := range c.InputExtensionsByKind {
		inputs[kind] = &entity.PreviewInputFormats{Extensions: extensions}
	}
	return &entity.GetPreviewCapabilitiesResponse{Available: true, Version: c.Version, FfmpegVersion: c.FFmpegVersion,
		Kinds: c.Kinds, InputExtensions: c.InputExtensions, OutputFormats: c.OutputFormats,
		InputExtensionsByKind: inputs, LibrawVersion: c.LibRawVersion}, nil
}

// CheckGeneration rejects explicit unavailable generation before creating a Job.
func (m *Manager) CheckGeneration(ctx context.Context) error {
	// Generation enablement is separate from installed capability and existing asset readability.
	settings, err := m.currentSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.GetEnabled() {
		return ErrDisabled
	}
	capabilities, err := m.Capabilities(ctx)
	if err != nil {
		return err
	}
	if !capabilities.Available {
		return fmt.Errorf("Preview generation unavailable: %s", capabilities.Reason)
	}
	return nil
}
