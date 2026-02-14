package config

import (
	"github.com/andrew-solarstorm/go-packages/common"
)

type AggregatorConfig struct {
	// DBPath is the path to the BoltDB file for pool persistence.
	// Default: "./data/aggregator.db"
	DBPath string

	// PersistenceEnabled controls whether pools are persisted to disk.
	// Default: true
	PersistenceEnabled bool

	// PersistInterval is how often pools are batch-saved to disk (in seconds).
	// Default: 30
	PersistInterval int
}

func (c *AggregatorConfig) Key() string {
	return AGGREGATOR_CONFIG_KEY
}

func (c *AggregatorConfig) Load() error {
	c.DBPath = common.GetEnvOrDefault("AGGREGATOR_DB_PATH", "./data/aggregator.db")
	c.PersistenceEnabled = common.GetEnvOrDefault("AGGREGATOR_PERSISTENCE_ENABLED", "true") == "true"
	c.PersistInterval = common.GetEnvOrDefaultInt("AGGREGATOR_PERSIST_INTERVAL", 30)
	return nil
}

func (c *AggregatorConfig) Validate() error {
	return nil
}
