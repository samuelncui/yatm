package preview

import (
	"fmt"
	"strings"
)

type outputFormat struct {
	extension string
	mediaType string
	arguments []string
}

func resolveOutputFormat(value string, quality int) (outputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "webp":
		return outputFormat{
			extension: "webp", mediaType: "image/webp",
			arguments: []string{"-c:v", "libwebp", "-quality", fmt.Sprint(quality)},
		}, nil
	case "jpg", "jpeg":
		qualityScale := 31 - quality*29/100
		return outputFormat{
			extension: "jpg", mediaType: "image/jpeg",
			arguments: []string{"-c:v", "mjpeg", "-q:v", fmt.Sprint(qualityScale)},
		}, nil
	case "png":
		return outputFormat{
			extension: "png", mediaType: "image/png",
			arguments: []string{"-c:v", "png"},
		}, nil
	default:
		return outputFormat{}, fmt.Errorf("unsupported Preview image format, format=%q", value)
	}
}
