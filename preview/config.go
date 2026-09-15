package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v2"
)

// Config defines Preview storage and extension-to-generator routing.
type Config struct {
	Root       string            `yaml:"root"`
	Generators []GeneratorConfig `yaml:"generators"`
}

// GeneratorConfig creates one configured Generator and its extension routes.
type GeneratorConfig struct {
	Kind       string         `yaml:"kind"`
	Extensions []string       `yaml:"extensions"`
	Options    map[string]any `yaml:"options"`
}

// Generator creates every asset for one source file inside outputDir.
type Generator interface {
	Generate(ctx context.Context, sourcePath, outputDir string) ([]*Asset, error)
}

// Factory constructs a Generator from its YAML options.
type Factory func(options map[string]any) (Generator, error)

type settingsProvider interface {
	Settings() any
}

type configuredGenerator struct {
	kind         string
	settingsJSON []byte
	generator    Generator
}

var factories = struct {
	sync.RWMutex
	values map[string]Factory
}{values: make(map[string]Factory, 2)}

// RegisterGenerator registers one generator kind during package initialization.
func RegisterGenerator(kind string, factory Factory) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		panic("register Preview generator with empty kind")
	}
	if factory == nil {
		panic(fmt.Sprintf("register Preview generator with nil factory, kind=%q", kind))
	}

	factories.Lock()
	defer factories.Unlock()
	if _, exists := factories.values[kind]; exists {
		panic(fmt.Sprintf("register duplicate Preview generator, kind=%q", kind))
	}
	factories.values[kind] = factory
}

func loadGenerators(configs []GeneratorConfig) (map[string]configuredGenerator, error) {
	if len(configs) == 0 {
		configs = defaultGeneratorConfigs()
	}

	// Resolve each configured kind before publishing any extension route.
	routes := make(map[string]configuredGenerator, 16)
	for _, config := range configs {
		kind := strings.TrimSpace(config.Kind)
		factories.RLock()
		factory := factories.values[kind]
		factories.RUnlock()
		if factory == nil {
			return nil, fmt.Errorf("Preview generator is not registered, kind=%q", kind)
		}
		generator, err := factory(config.Options)
		if err != nil {
			return nil, fmt.Errorf("create Preview generator failed, kind=%q, %w", kind, err)
		}

		settings := any(config.Options)
		if provider, ok := generator.(settingsProvider); ok {
			settings = provider.Settings()
		}
		settingsJSON, err := json.Marshal(normalizeJSON(settings))
		if err != nil {
			return nil, fmt.Errorf("encode Preview generator settings failed, kind=%q, %w", kind, err)
		}
		configured := configuredGenerator{kind: kind, settingsJSON: settingsJSON, generator: generator}

		for _, value := range config.Extensions {
			extension := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), ".")
			if extension == "" || strings.ContainsAny(extension, `/\\`) {
				return nil, fmt.Errorf("invalid Preview extension, kind=%q extension=%q", kind, value)
			}
			if _, exists := routes[extension]; exists {
				return nil, fmt.Errorf("duplicate Preview extension, extension=%q", extension)
			}
			routes[extension] = configured
		}
	}
	return routes, nil
}

func resolveRoot(root, work string) string {
	if strings.TrimSpace(root) == "" {
		return filepath.Join(work, "previews")
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root)
	}
	return filepath.Clean(filepath.Join(work, root))
}

func decodeOptions(options map[string]any, target any) error {
	if len(options) == 0 {
		return nil
	}
	data, err := yaml.Marshal(options)
	if err != nil {
		return fmt.Errorf("encode generator options failed, %w", err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode generator options failed, %w", err)
	}
	return nil
}

func normalizeJSON(value any) any {
	switch typed := value.(type) {
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[fmt.Sprint(key)] = normalizeJSON(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeJSON(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeJSON(item)
		}
		return result
	default:
		return value
	}
}

func defaultGeneratorConfigs() []GeneratorConfig {
	return []GeneratorConfig{
		{Kind: "image", Extensions: []string{"jpg", "jpeg", "png", "webp", "heic"}},
		{Kind: "video", Extensions: []string{"mp4", "mov", "mkv", "avi"}},
	}
}
