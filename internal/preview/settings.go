package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SettingsFromConfig imports legacy generator options, resolving their defaults once.
func SettingsFromConfig(config Config) (*entity.PreviewSettings, error) {
	configs := config.Generators
	if len(configs) == 0 {
		configs = defaultGeneratorConfigs()
	}
	settings := &entity.PreviewSettings{Concurrency: 2, TimeoutSeconds: 600, MaxInputPixels: 64_000_000}
	for _, config := range configs {
		item := &entity.PreviewGeneratorSettings{Enabled: true}
		for _, name := range config.Extensions {
			item.Extensions = append(item.Extensions, &entity.PreviewExtension{Name: name, Enabled: true})
		}
		var generator Generator
		var message proto.Message
		var err error
		switch strings.TrimSpace(config.Kind) {
		case "image":
			generator, err = newImageGenerator(config.Options)
			options := &entity.ImagePreviewSettings{}
			message, item.Options = options, &entity.PreviewGeneratorSettings_Image{Image: options}
		case "video":
			generator, err = newVideoGenerator(config.Options)
			options := &entity.VideoPreviewSettings{}
			message, item.Options = options, &entity.PreviewGeneratorSettings_Video{Video: options}
		default:
			return nil, fmt.Errorf("unsupported Preview settings generator %q", config.Kind)
		}
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(generator.(settingsProvider).Settings())
		if err != nil {
			return nil, err
		}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(encoded, message); err != nil {
			return nil, err
		}
		settings.Generators = append(settings.Generators, item)
	}
	return settings, ValidateSettings(settings)
}

// generatorConfigs validates persisted preferences before any process is launched.
func generatorConfigs(settings *entity.PreviewSettings) ([]GeneratorConfig, error) {
	return generatorConfigsFor(settings.GetGenerators(), settings.GetCommand(), settings.GetMaxInputPixels())
}

func generatorConfigsFor(
	generators []*entity.PreviewGeneratorSettings,
	command string,
	maxInputPixels int64,
) ([]GeneratorConfig, error) {
	if len(generators) == 0 || len(generators) > 32 {
		return nil, fmt.Errorf("Preview requires between 1 and 32 generator routes")
	}
	configs := make([]GeneratorConfig, 0, len(generators))
	for _, item := range generators {
		var message proto.Message
		config := GeneratorConfig{}
		for _, extension := range item.GetExtensions() {
			config.Extensions = append(config.Extensions, extension.GetName())
		}
		switch options := item.GetOptions().(type) {
		case *entity.PreviewGeneratorSettings_Image:
			config.Kind, message = "image", options.Image
		case *entity.PreviewGeneratorSettings_Video:
			config.Kind, message = "video", options.Video
		default:
			return nil, fmt.Errorf("Preview generator options are missing")
		}
		if len(config.Extensions) == 0 || len(config.Extensions) > 100 {
			return nil, fmt.Errorf("Preview generator requires between 1 and 100 extensions")
		}
		encoded, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(message)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &config.Options); err != nil {
			return nil, err
		}
		config.Options["command"] = command
		config.Options["max_input_pixels"] = maxInputPixels
		configs = append(configs, config)
	}
	return configs, nil
}

func ValidateSettings(settings *entity.PreviewSettings) error {
	// Validate scheduling independently of dependency availability and enablement.
	if settings.GetConcurrency() < 1 || settings.GetConcurrency() > 16 {
		return fmt.Errorf("Preview concurrency must be between 1 and 16")
	}
	if settings.GetTimeoutSeconds() < 1 || settings.GetTimeoutSeconds() > 86400 {
		return fmt.Errorf("Preview timeout must be between 1 and 86400 seconds")
	}
	if settings.GetMaxInputPixels() < 1 || settings.GetMaxInputPixels() > 64_000_000 {
		return fmt.Errorf("Preview input pixel limit must be between 1 and 64000000")
	}

	// Validate all saved routes, including disabled options retained for later use.
	configs, err := generatorConfigs(settings)
	if err != nil {
		return err
	}
	_, err = loadGenerators(configs)
	return err
}

// SettingsGetter reads the effective Preview Settings used when a file obtains a generation slot.
type SettingsGetter func(context.Context) (*entity.PreviewSettings, error)

// NewWithSettings constructs Preview storage from typed Settings and observes later live edits.
func NewWithSettings(ctx context.Context, root, work string, get SettingsGetter) (*Manager, error) {
	// Resolve one validated output definition before constructing generator routes.
	if get == nil {
		return nil, fmt.Errorf("Preview Settings getter is missing")
	}
	settings, err := get(ctx)
	if err != nil {
		return nil, err
	}
	if err := ValidateSettings(settings); err != nil {
		return nil, err
	}
	configs, err := generatorConfigs(settings)
	if err != nil {
		return nil, err
	}

	// Bind the same getter for runtime controls without changing the frozen output routes.
	manager, err := New(Config{Root: root, Generators: configs}, work)
	if err != nil {
		return nil, err
	}
	manager.settings = get
	return manager, nil
}
