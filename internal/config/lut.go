package config

import (
	"os"
	"strings"
	"time"
)

const LUT_CONFIG_KEY = "lut-config"

type LUTConfig struct {
	// Addresses is a list of on-chain Address Lookup Table public keys (base58).
	Addresses []string

	// RefreshInterval controls how often LUT states are re-fetched from RPC.
	RefreshInterval time.Duration
}

func (c *LUTConfig) Key() string {
	return LUT_CONFIG_KEY
}

func (c *LUTConfig) Load() error {
	raw := os.Getenv("LUT_ADDRESSES")
	if raw != "" {
		parts := strings.Split(raw, ",")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				c.Addresses = append(c.Addresses, p)
			}
		}
	}
	c.RefreshInterval = 60 * time.Second
	return nil
}

func (c *LUTConfig) Validate() error {
	return nil
}
