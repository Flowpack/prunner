package config

import (
	"os"

	"github.com/apex/log"
	"github.com/friendsofgo/errors"
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
		return errors.Errorf("jwt_secret must be at least %d characters long", minJWTSecretLength)
	}

	return nil
}

func LoadOrCreateConfig(configPath string, cliConfig Config) (*Config, error) {
	if err := cliConfig.validate(); err == nil {
		log.Debug("Using config from CLI")
		return &cliConfig, nil
	} else if err != ErrMissingJWTSecret {
		return nil, errors.Wrap(err, "invalid CLI config")
	}

	log.Debugf("Reading config from %s", configPath)
	f, err := os.Open(configPath)
	if os.IsNotExist(err) {
		log.Infof("No config found, creating file at %s", configPath)
		return createDefaultConfig(configPath)
	} else if err != nil {
		return nil, errors.Wrap(err, "opening config file")
	}
	defer f.Close()

	c := new(Config)

	err = yaml.NewDecoder(f).Decode(c)
	if err != nil {
		return nil, errors.Wrap(err, "decoding config")
	}

	err = c.validate()
	if err != nil {
		return nil, errors.Wrap(err, "invalid config")
	}

	return c, nil
}

func createDefaultConfig(configPath string) (*Config, error) {
	f, err := os.Create(configPath)
	if err != nil {
		return nil, errors.Wrap(err, "creating config file")
	}
	defer f.Close()

	jwtSecret, err := helper.GenerateRandomString(32)
	if err != nil {
		return nil, errors.Wrap(err, "generating random string")
	}
	c := &Config{
		JWTSecret: jwtSecret,
	}

	err = yaml.NewEncoder(f).Encode(c)
	if err != nil {
		return nil, errors.Wrap(err, "encoding config")
	}

	return c, nil
}
