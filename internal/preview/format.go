package preview

import (
	"fmt"
	"strings"
)

func normalizeOutputFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "webp":
		return "webp", nil
	case "jpg", "jpeg":
		return "jpeg", nil
	case "png":
		return "png", nil
	default:
		return "", fmt.Errorf("unsupported Preview image format, format=%q", value)
	}
}
