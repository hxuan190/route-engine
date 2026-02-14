package config

import (
	"errors"
	"os"
)

type ServerEnv = string

var (
	DevEnv     ServerEnv = "dev"
	StagingEnv ServerEnv = "staging"
	ProdEnv    ServerEnv = "prod"
)

const (
	DATABASE_CONFIG_KEY   = "database-config"
	GENERAL_CONFIG_KEY    = "general-config"
	AUTH_CONFIG_KEY       = "auth-config"
	RPC_CONFIG_KEY        = "rpc-config"
	AGGREGATOR_CONFIG_KEY = "aggregator-config"
	LUT_CONFIG_KEY        = "lut-config"
)

func getEnvOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

type GeneralConfig struct {
	HTTPPort string
	HTTPHost string
	Env      string
	LogLevel string
}

func (gc *GeneralConfig) Key() string {
	return GENERAL_CONFIG_KEY
}

func (gc *GeneralConfig) Load() error {
	gc.HTTPPort = getEnvOrDefault("HTTP_PORT", "8080")
	gc.HTTPHost = getEnvOrDefault("HTTP_HOST", "localhost")
	gc.Env = getEnvOrDefault("ENV", "dev")
	gc.LogLevel = getEnvOrDefault("LOG_LEVEL", "INFO")
	return gc.Validate()
}

func (gc *GeneralConfig) Validate() error {
	if gc.HTTPPort == "" || gc.HTTPHost == "" || gc.Env == "" {
		return errors.New("invalid server config")
	}
	return nil
}
