package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Duration is a time.Duration that travels as a Go duration string ("30s",
// "1m30s") in the canonical encoding, so IR files stay readable and both
// authoring surfaces emit the same representation.
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

// DecodeScenario decodes canonical JSON strictly: unknown fields and
// trailing data are errors, so a surface emitting fields this engine build
// doesn't know fails loudly instead of being silently dropped.
func DecodeScenario(data []byte) (*Scenario, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Scenario
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("decode scenario: %w", err)
	}
	// Only io.EOF proves the document was the whole input. Decoder.More is
	// not enough here: it reports false for a peeked "}" or "]", treating
	// stray closers as end-of-container rather than trailing data.
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("decode scenario: trailing data after JSON document")
	}
	return &s, nil
}
