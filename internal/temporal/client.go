package temporal

import (
	"fmt"

	"go.temporal.io/sdk/client"
)

// ClientConfig configures the Temporal client connection.
type ClientConfig struct {
	HostPort  string
	Namespace string
}

func (c ClientConfig) withDefaults() ClientConfig {
	if c.HostPort == "" {
		c.HostPort = client.DefaultHostPort // localhost:7233
	}
	if c.Namespace == "" {
		c.Namespace = client.DefaultNamespace // default
	}
	return c
}

// Dial opens a Temporal client. The caller owns its lifecycle (Close).
func Dial(cfg ClientConfig) (client.Client, error) {
	cfg = cfg.withDefaults()
	c, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("temporal: dial %s: %w", cfg.HostPort, err)
	}
	return c, nil
}
