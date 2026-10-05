// Package previewprotocol defines the stdlib-only boundary to the optional native worker.
package previewprotocol

const Version = 1

// Request describes one source and an otherwise empty, caller-owned output directory.
type Request struct {
	Protocol                int    `json:"protocol"`
	Kind                    string `json:"kind"`
	SourcePath              string `json:"source_path"`
	OutputDir               string `json:"output_dir"`
	Format                  string `json:"format"`
	Quality                 int    `json:"quality"`
	MaxWidth                int    `json:"max_width,omitempty"`
	MaxHeight               int    `json:"max_height,omitempty"`
	PosterWidth             int    `json:"poster_width,omitempty"`
	PosterHeight            int    `json:"poster_height,omitempty"`
	TimelineWidth           int    `json:"timeline_width,omitempty"`
	TimelineHeight          int    `json:"timeline_height,omitempty"`
	TimelineIntervalSeconds int    `json:"timeline_interval_seconds,omitempty"`
	TimelineMaxFrames       int    `json:"timeline_max_frames,omitempty"`
	MaxInputPixels          int64  `json:"max_input_pixels"`
}

// Asset is a relative file written by the worker; the caller validates it before publication.
type Asset struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	MediaType string `json:"media_type"`
	Width     uint32 `json:"width,omitempty"`
	Height    uint32 `json:"height,omitempty"`
}

// Capabilities reports this executable's currently linked codecs, not source-file guarantees.
type Capabilities struct {
	Version               string              `json:"version"`
	FFmpegVersion         string              `json:"ffmpeg_version"`
	LibRawVersion         string              `json:"libraw_version"`
	InputExtensionsByKind map[string][]string `json:"input_extensions_by_kind"`
	Kinds                 []string            `json:"kinds"`
	InputExtensions       []string            `json:"input_extensions"`
	OutputFormats         []string            `json:"output_formats"`
}

// Event is one JSON line. Exactly one result, error or capabilities event terminates a request.
type Event struct {
	Protocol     int           `json:"protocol"`
	Type         string        `json:"type"`
	Phase        string        `json:"phase,omitempty"`
	Completed    int           `json:"completed,omitempty"`
	Total        int           `json:"total,omitempty"`
	ElapsedMS    int64         `json:"elapsed_ms,omitempty"`
	Assets       []Asset       `json:"assets,omitempty"`
	Error        string        `json:"error,omitempty"`
	Capabilities *Capabilities `json:"capabilities,omitempty"`
}
