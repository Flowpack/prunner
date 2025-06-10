package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/apex/log"
	"gopkg.in/yaml.v2"

	"github.com/Flowpack/prunner/helper"
)

type Config struct {
	JWTSecret string `yaml:"jwt_secret"`
}

var ErrMissingJWTSecret = errors.New("missing jwt_secret")

func (c Config) validate() error {
	if c.JWTSecret == "" {
		return ErrMissingJWTSecret
	}
	const minJWTSecretLength = 16
	if len(c.JWTSecret) < minJWTSecretLength {
		return fmt.Errorf("jwt_secret must be at least %d characters long", minJWTSecretLength)
	}

	return nil
}

func LoadOrCreateConfig(configPath string, cliConfig Config) (c *Config, err error) {
	if err := cliConfig.validate(); err == nil {
		log.Debug("Using config from CLI")
		return &cliConfig, nil
	} else if err != ErrMissingJWTSecret {
		return nil, fmt.Errorf("invalid CLI config: %w", err)
	}

	log.Debugf("Reading config from %s", configPath)
	f, err := os.Open(configPath)
	if os.IsNotExist(err) {
		log.Infof("No config found, creating file at %s", configPath)
		return createDefaultConfig(configPath)
	} else if err != nil {
		return nil, fmt.Errorf("opening config file: %w", err)
	}
	defer func(f *os.File) {
		err = errors.Join(err, f.Close())
	}(f)

	c = new(Config)

	err = yaml.NewDecoder(f).Decode(c)
	if err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}

	err = c.validate()
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return c, nil
}

func createDefaultConfig(configPath string) (c *Config, err error) {
	f, err := os.Create(configPath)
	if err != nil {
		return nil, fmt.Errorf("creating config file: %w", err)
	}
	defer func(f *os.File) {
		err = errors.Join(err, f.Close())
	}(f)

	jwtSecret, err := helper.GenerateRandomString(32)
	if err != nil {
		return nil, fmt.Errorf("generating random string: %w", err)
	}
	c = &Config{
		JWTSecret: jwtSecret,
	}

	err = yaml.NewEncoder(f).Encode(c)
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}

	return c, nil
}
