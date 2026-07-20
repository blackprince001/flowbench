package ir

import (
	"bytes"
	"encoding/json"
	"errors"
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
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("decode scenario: %w", err)
	}
	// only io.EOF proves the whole input was consumed; Decoder.More reports
	// false for a peeked "}" or "]", missing stray trailing closers
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("decode scenario: trailing data after JSON document")
	}
	return &s, nil
}
