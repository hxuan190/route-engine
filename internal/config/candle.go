package config

import "github.com/andrew-solarstorm/go-packages/common"

const (
	CANDLE_CONFIG_KEY = "candle-config"
)

type CandleConfig struct {
	DBPath string
}

func (c *CandleConfig) Key() string {
	return CANDLE_CONFIG_KEY
}

func (c *CandleConfig) Load() error {
	c.DBPath = common.GetEnvOrDefault("CANDLE_DB_PATH", "./data/candle.db")
	return nil
}

func (c *CandleConfig) Validate() error {
	return nil
}
