package main

import (
	"fmt"
	"net"
)

// config holds the settings read once at start-up.
type config struct {
	addr string
}

// loadConfig builds a config from the environment. The flags in args are
// handled by run, so none of them change the configuration.
func loadConfig(_ []string, getenv func(string) string) (config, error) {
	cfg := config{addr: ":8080"}
	if v := getenv("WEB_ADDR"); v != "" {
		cfg.addr = v
	}
	if _, _, err := net.SplitHostPort(cfg.addr); err != nil {
		return config{}, fmt.Errorf("checking WEB_ADDR: %w", err)
	}
	return cfg, nil
}
