package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf(`duration must be a string like "30s": %w`, err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func DecodeScenario(data []byte) (*Scenario, error) {
	var s Scenario
	if err := decodeStrict(data, &s, "scenario"); err != nil {
		return nil, err
	}
	return &s, nil
}

func DecodeTargetConfig(data []byte) (*TargetConfig, error) {
	var t TargetConfig
	if err := decodeStrict(data, &t, "target"); err != nil {
		return nil, err
	}
	return &t, nil
}

func decodeStrict(data []byte, v any, what string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	// only io.EOF proves the whole input was consumed; Decoder.More reports
	// false for a peeked "}" or "]", missing stray trailing closers
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("decode %s: trailing data after JSON document", what)
	}
	return nil
}
