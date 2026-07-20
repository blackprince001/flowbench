package parser

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"

	"github.com/blackprince001/flowbench/internal/ir"
)

// ParseTargetFile reads and validates a target config file.
func ParseTargetFile(path string) (*ir.TargetConfig, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read target file: %w", err)
	}
	return ParseTarget(src, path)
}

// ParseTarget decodes a target config: YAML is converted to canonical JSON and
// strictly decoded, so an unknown key fails loudly rather than being dropped.
func ParseTarget(src []byte, filename string) (*ir.TargetConfig, error) {
	var raw any
	if err := yaml.Unmarshal(src, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	j, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	tc, err := ir.DecodeTargetConfig(j)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	if err := tc.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return tc, nil
}
