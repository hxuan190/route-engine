package config

import (
	"os"
	"strings"
	"time"
)

type LUTConfig struct {
	Addresses []string

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
