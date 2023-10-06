package config

import (
	"fmt"
	"os"

	"github.com/samuelncui/yatm/executor"
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
}

func GetConfig(path string) *Config {
	cf, err := os.Open(path)
	if err != nil {
		panic(fmt.Errorf("open config file failed, %w", err))
	}

	conf := new(Config)
	if err := yaml.NewDecoder(cf).Decode(conf); err != nil {
		panic(fmt.Errorf("decode config file failed, %w", err))
	}

	logrus.Infof("read config success, conf= '%+v'", conf)
	return conf
}
