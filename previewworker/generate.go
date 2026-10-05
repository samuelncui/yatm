package main

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/internal/previewprotocol"
)

type progress func(phase string, completed, total int) error

func generate(ctx context.Context, request previewprotocol.Request, report progress) ([]previewprotocol.Asset, error) {
	// Open once and retain the same decoder for all seek points.
	if err := report("opening", 0, 0); err != nil {
		return nil, err
	}
	if request.Kind == "image" && isRAW(request.SourcePath) {
		return generateRAW(ctx, request, report)
	}
	m, err := openMedia(ctx, request.SourcePath, request.Kind == "video", request.MaxInputPixels)
	if err != nil {
		return nil, err
	}
	defer m.close()
	if err := report("opened", 0, 0); err != nil {
		return nil, err
	}
	if request.Kind == "video" {
		return generateVideo(ctx, m, request, report)
	}

	// Still images use the same pixel-limited native decode without seeking.
	if _, err := m.sample(ctx, 0, false); err != nil {
		return nil, err
	}
	img, err := m.render(request.MaxWidth, request.MaxHeight, false)
	if err != nil {
		return nil, err
	}
	if err := report("encoding", 0, 1); err != nil {
		return nil, err
	}
	asset, err := saveImage(request.OutputDir, "thumbnail", request.Format, request.Quality, img)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []previewprotocol.Asset{asset}, nil
}

func generateVideo(ctx context.Context, m *media, request previewprotocol.Request, report progress) ([]previewprotocol.Asset, error) {
	// Establish a missing container start time before planning normalized sample positions.
	first, err := m.sample(ctx, 0, true)
	if err != nil {
		return nil, fmt.Errorf("sample first frame failed: %w", err)
	}

	// Allocate only the bounded small sprite; decoded full-size frames are reused.
	interval := math.Max(float64(request.TimelineIntervalSeconds), m.duration/float64(request.TimelineMaxFrames))
	count := max(1, min(request.TimelineMaxFrames, int(math.Ceil(m.duration/interval))))
	columns := min(count, 10)
	rows := (count + columns - 1) / columns
	sprite := image.NewNRGBA(image.Rect(0, 0, columns*request.TimelineWidth, rows*request.TimelineHeight))
	times := make([]float64, 0, count)
	var assets []previewprotocol.Asset
	for index := 0; index < count; index++ {
		// Sparse backward seek yields a real keyframe time, not a claimed precise target.
		actual := first
		if index > 0 {
			actual, err = m.sample(ctx, float64(index)*interval, true)
			if err != nil {
				return nil, fmt.Errorf("sample %d/%d failed: %w", index+1, count, err)
			}
		}
		if actual >= m.duration {
			return nil, fmt.Errorf("sample timestamp exceeds video duration")
		}
		if len(times) > 0 && actual < times[len(times)-1]-0.001 {
			return nil, fmt.Errorf("non-monotonic seek result")
		}
		if len(times) == 0 || actual > times[len(times)-1]+0.001 {
			if len(times) == 0 {
				poster, err := m.render(request.PosterWidth, request.PosterHeight, false)
				if err != nil {
					return nil, err
				}
				asset, err := saveImage(request.OutputDir, "poster", request.Format, request.Quality, poster)
				if err != nil {
					return nil, err
				}
				assets = append(assets, asset)
			}
			tile, err := m.render(request.TimelineWidth, request.TimelineHeight, true)
			if err != nil {
				return nil, err
			}
			position := image.Pt(len(times)%columns*request.TimelineWidth, len(times)/columns*request.TimelineHeight)
			draw.Draw(sprite, tile.Bounds().Add(position), tile, image.Point{}, draw.Src)
			times = append(times, actual)
		}
		if err := report("sampling", index+1, count); err != nil {
			return nil, err
		}
	}

	// Compact duplicate keyframes and map playback intervals to the actual retained samples.
	usedColumns := min(len(times), 10)
	usedRows := (len(times) + usedColumns - 1) / usedColumns
	if usedColumns != columns || usedRows != rows {
		compact := image.NewNRGBA(image.Rect(0, 0, usedColumns*request.TimelineWidth, usedRows*request.TimelineHeight))
		for index := range times {
			source := image.Pt(index%columns*request.TimelineWidth, index/columns*request.TimelineHeight)
			target := image.Pt(index%usedColumns*request.TimelineWidth, index/usedColumns*request.TimelineHeight)
			draw.Draw(compact, image.Rect(target.X, target.Y, target.X+request.TimelineWidth, target.Y+request.TimelineHeight),
				sprite, source, draw.Src)
		}
		sprite = compact
	}
	if err := report("encoding", len(times), count); err != nil {
		return nil, err
	}
	asset, err := saveImage(request.OutputDir, "timeline", request.Format, request.Quality, sprite)
	if err != nil {
		return nil, err
	}
	assets = append(assets, asset)
	contents := timelineVTT(times, m.duration, asset.Name, usedColumns, request.TimelineWidth, request.TimelineHeight)
	if err := os.WriteFile(filepath.Join(request.OutputDir, "timeline.vtt"), []byte(contents), 0o600); err != nil {
		return nil, fmt.Errorf("write timeline map failed: %w", err)
	}
	assets = append(assets, previewprotocol.Asset{Name: "timeline.vtt", Role: "timeline-map", MediaType: "text/vtt"})
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Enforce the aggregate budget before the parent can publish any asset.
	var size int64
	for _, asset := range assets {
		info, err := os.Stat(filepath.Join(request.OutputDir, asset.Name))
		if err != nil {
			return nil, err
		}
		size += info.Size()
		if size > maxAssetsBytes {
			return nil, fmt.Errorf("assets exceed total byte limit")
		}
	}
	return assets, nil
}

func timelineVTT(times []float64, duration float64, name string, columns, width, height int) string {
	var output strings.Builder
	output.WriteString("WEBVTT\n\n")
	for index, start := range times {
		if index == 0 {
			start = 0
		}
		end := duration
		if index+1 < len(times) {
			end = times[index+1]
		}
		fmt.Fprintf(&output, "NOTE sampled at %s\n\n%s --> %s\n%s#xywh=%d,%d,%d,%d\n\n",
			vttTime(times[index]), vttTime(start), vttTime(end), name, index%columns*width, index/columns*height, width, height)
	}
	return output.String()
}

func vttTime(seconds float64) string {
	ms := int64(math.Round(seconds * 1000))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}
