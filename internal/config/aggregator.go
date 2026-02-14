package config

import (
	"os"
	"strconv"
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
	c.DBPath = getEnvOrDefault("AGGREGATOR_DB_PATH", "./data/route-engine.db")
	c.PersistenceEnabled = getEnvOrDefault("AGGREGATOR_PERSISTENCE_ENABLED", "true") == "true"
	c.PersistInterval = getEnvOrDefaultInt("AGGREGATOR_PERSIST_INTERVAL", 30)
	return nil
}

func (c *AggregatorConfig) Validate() error {
	return nil
}

func getEnvOrDefaultInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultValue
}
