package ir_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

// chainedLoginScenario hand-builds the PRD section 11 sample as IR: the
// chained login → extract token → act (with retry/backoff) → assert path
// under a stress profile. Every test that needs a valid scenario starts
// from a fresh copy of this.
func chainedLoginScenario() *ir.Scenario {
	return &ir.Scenario{
		Name:   "checkout_stress",
		Target: "local",
		DataPools: []ir.DataPool{{
			Name:         "user",
			Source:       "fixtures/users.csv",
			Format:       ir.PoolCSV,
			Distribution: ir.DistributeUniquePerVU,
		}},
		Flows: []ir.Flow{{
			Name: "authenticated_checkout",
			Data: "user",
			Steps: []ir.Step{
				{
					ID:   "login",
					Type: ir.StepCall,
					Call: &ir.CallSpec{
						Method: "POST",
						URL:    "/auth/login",
						Body:   json.RawMessage(`{"email":"{{ user.email }}","password":"{{ user.password }}"}`),
					},
					Extract: []ir.Extraction{{Var: "token", Path: "$.data.access_token"}},
					Assert: []ir.Assertion{
						{Source: ir.AssertStatus, Op: ir.OpEq, Value: json.RawMessage(`200`)},
						{Source: ir.AssertVar, Key: "token", Op: ir.OpExists},
					},
				},
				{
					ID:   "create_order",
					Type: ir.StepCall,
					Call: &ir.CallSpec{
						Method:  "POST",
						URL:     "/orders",
						Headers: map[string]string{"Authorization": "Bearer {{ token }}"},
						Body:    json.RawMessage(`{"items":"{{ user.cart }}"}`),
					},
					Extract: []ir.Extraction{{Var: "order_id", Path: "$.data.id"}},
					Retry: &ir.RetryPolicy{
						OnStatus:    []int{429, 503},
						Backoff:     ir.BackoffHonorRetryAfter,
						MaxAttempts: 5,
					},
				},
				{
					ID:   "pay",
					Type: ir.StepCall,
					Call: &ir.CallSpec{
						Method:  "POST",
						URL:     "/orders/{{ order_id }}/pay",
						Headers: map[string]string{"Authorization": "Bearer {{ token }}"},
					},
					Assert: []ir.Assertion{
						{Source: ir.AssertStatus, Op: ir.OpEq, Value: json.RawMessage(`202`)},
					},
				},
			},
		}},
		Profile: ir.Profile{
			Mode:       ir.ModeStress,
			Ramp:       "0 -> 500 over 5m",
			Hold:       ir.Duration(10 * time.Minute),
			ArrivalCap: "300/s",
			Thresholds: []string{"p95(latency) < 800ms", "error_rate < 1%"},
		},
	}
}

func TestChainedLoginFixtureValidates(t *testing.T) {
	if err := chainedLoginScenario().Validate(); err != nil {
		t.Fatalf("hand-built chained-login fixture should validate, got:\n%v", err)
	}
}

func TestScenarioRoundTripsThroughJSON(t *testing.T) {
	fx := chainedLoginScenario()

	encoded, err := json.Marshal(fx)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := ir.DecodeScenario(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(fx, decoded) {
		t.Errorf("decoded scenario differs from the original\noriginal: %+v\ndecoded:  %+v", fx, decoded)
	}

	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Errorf("re-encoding is not byte-identical\nfirst:  %s\nsecond: %s", encoded, reencoded)
	}
}

// TestGoldenChainedLoginWireFormat locks the canonical wire shape: if a type
// change alters the encoding, this fails until the golden file is
// deliberately regenerated with `go test ./internal/ir -run Golden -update`.
func TestGoldenChainedLoginWireFormat(t *testing.T) {
	fx := chainedLoginScenario()
	got, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	golden := filepath.Join("testdata", "chained_login.json")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if !bytes.Equal(compactJSON(t, got), compactJSON(t, want)) {
		t.Errorf("wire format drifted from %s; regenerate with -update if intended", golden)
	}

	decoded, err := ir.DecodeScenario(want)
	if err != nil {
		t.Fatalf("golden file no longer decodes strictly: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Errorf("golden file no longer validates:\n%v", err)
	}
}

func compactJSON(t *testing.T, in []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, in); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return buf.Bytes()
}

func TestDurationJSON(t *testing.T) {
	d := ir.Duration(90 * time.Second)
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"1m30s"` {
		t.Errorf("marshal = %s, want %q", b, `"1m30s"`)
	}

	var back ir.Duration
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != d {
		t.Errorf("round-trip = %v, want %v", back, d)
	}

	for name, in := range map[string]string{
		"bare number":    `90000000000`,
		"unparseable":    `"ninety seconds"`,
		"missing suffix": `"90"`,
	} {
		var d ir.Duration
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("%s (%s) should not unmarshal", name, in)
		}
	}
}

func TestDecodeScenarioRejectsUnknownFields(t *testing.T) {
	_, err := ir.DecodeScenario([]byte(`{"name":"x","flows":[],"profile":{"mode":"load"},"bogus":1}`))
	if err == nil {
		t.Fatal("unknown fields should fail strict decoding")
	}
}

func TestDecodeScenarioRejectsTrailingData(t *testing.T) {
	const doc = `{"name":"x","flows":[],"profile":{"mode":"load"}}`
	// The brace/bracket cases are json.Decoder.More()'s blind spot: More()
	// treats a peeked "}" or "]" as end-of-container, so only an explicit
	// EOF check catches them.
	for name, trailing := range map[string]string{
		"second document": ` {"second":true}`,
		"stray brace":     `}`,
		"stray bracket":   `]`,
		"garbage":         `garbage`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ir.DecodeScenario([]byte(doc + trailing)); err == nil {
				t.Fatalf("trailing %s should fail decoding", name)
			}
		})
	}

	if _, err := ir.DecodeScenario([]byte(doc + "\n  \n")); err != nil {
		t.Errorf("trailing whitespace should be fine, got: %v", err)
	}
}
