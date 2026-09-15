package config

import (
	"fmt"
	"os"

	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/preview"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Domain      string `yaml:"domain"`
	Listen      string `yaml:"listen"`
	DebugListen string `yaml:"debug_listen"`

	Database struct {
		Dialect string `yaml:"dialect"`
		DSN     string `yaml:"dsn"`
	} `yaml:"database"`

	Paths       executor.Paths   `yaml:"paths"`
	TapeDevices []string         `yaml:"tape_devices"`
	Scripts     executor.Scripts `yaml:"scripts"`
	Preview     preview.Config   `yaml:"preview"`
}

func GetConfig(path string) *Config {
	conf, err := Load(path)
	if err != nil {
		panic(err)
	}
	logrus.Info("configuration loaded")
	return conf
}

// Load reads configuration without opening databases, contacting the server or logging secrets.
func Load(path string) (*Config, error) {
	// Keep read-only preflight independent of service resources and admission state.
	cf, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file failed, %w", err)
	}
	defer cf.Close()

	// Decode with the same rules used by the service, leaving ownership with the caller.
	conf := new(Config)
	if err := yaml.NewDecoder(cf).Decode(conf); err != nil {
		return nil, fmt.Errorf("decode config file failed, %w", err)
	}
	return conf, nil
}
