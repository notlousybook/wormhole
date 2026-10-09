package wormhole

import (
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed config.free.yaml
var defaultConfigYAML []byte

// NOTE: config.free.yaml is duplicated here (embedded) so serverless builds
// (Vercel) have a config without a filesystem. Keep both copies in sync.

// LoadDefaultConfig parses the embedded free config. Used by serverless
// deployments; the standalone binary uses LoadConfig with a real file.
func LoadDefaultConfig() (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(defaultConfigYAML, &cfg); err != nil {
		return nil, fmt.Errorf("parse embedded config: %w", err)
	}
	if err := applyDefaults(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
