package legacy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var legacyLTFSOption = regexp.MustCompile(`(?:^|[[:space:]])(capture_index|work_directory)=("[^"]*"|'[^']*'|[^\s;]+)`)

// LegacyLTFSIndexRoot resolves literal capture options from the original service working directory.
func LegacyLTFSIndexRoot(mountScript, scriptRoot string) string {
	// Load the configured script while retaining the conventional legacy location as fallback.
	fallback := filepath.Join(scriptRoot, legacyLTFSIndexDirectory)
	data, err := os.ReadFile(mountScript)
	if err != nil {
		return fallback
	}

	// Prefer an explicit capture_index path over LTFS's work_directory fallback.
	var script strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		script.WriteString(line)
		script.WriteByte('\n')
	}
	matches := legacyLTFSOption.FindAllStringSubmatch(script.String(), -1)
	for _, option := range []string{"capture_index", "work_directory"} {
		for _, match := range matches {
			if match[1] != option {
				continue
			}
			value := strings.Trim(match[2], `"'`)
			if value == "" || strings.ContainsAny(value, "$`") {
				continue
			}
			if !filepath.IsAbs(value) {
				value = filepath.Join(scriptRoot, value)
			}
			return filepath.Clean(value)
		}
	}
	return fallback
}
