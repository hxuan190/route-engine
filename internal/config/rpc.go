package config

import (
	"errors"
	"os"
	"slices"
)

type RPCConfig struct {
	RPCUrl string
	// GRPCUrl   string
	WSUrl             string
	RPCApiKey         string
	SessionSponsorKey string // Base58-encoded private key for session transaction signing
}

func (r *RPCConfig) Key() string {
	return RPC_CONFIG_KEY
}

func (r *RPCConfig) Load() error {
	r.RPCUrl = os.Getenv("RPC_URL")
	// r.GRPCUrl = os.Getenv("GRPC_URL")
	r.WSUrl = os.Getenv("WS_URL")
	r.RPCApiKey = os.Getenv("RPC_KEY")
	r.SessionSponsorKey = os.Getenv("SESSION_SPONSOR_KEY")
	return nil
}

func (r *RPCConfig) Validate() error {
	if slices.Contains([]string{r.WSUrl, r.RPCUrl, r.RPCApiKey}, "") {
		return errors.New("invalid rpc config")
	}
	return nil
}
