package planner

import (
	"testing"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
)

func dur(s string) ir.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return ir.Duration(d)
}

// TestPlanStressExample is the issue's acceptance case: the PRD stress example
// (ramp 0->500 over 5m, hold 10m, cap 300/s) must plan to a ramp then a hold,
// open arrival, a 300/s cap, and stop-on-thresholds.
func TestPlanStressExample(t *testing.T) {
	got, err := Plan(ir.Profile{
		Mode:       ir.ModeStress,
		Ramp:       "0 -> 500 over 5m",
		Hold:       dur("10m"),
		ArrivalCap: "300/s",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	want := &Schedule{
		Mode:    ir.ModeStress,
		Arrival: Open,
		Segments: []Segment{
			{Kind: Ramp, StartVUs: 0, EndVUs: 500, Duration: dur("5m")},
			{Kind: Hold, StartVUs: 500, EndVUs: 500, Duration: dur("10m")},
		},
		Stop:       StopThresholds,
		ArrivalCap: &Rate{Count: 300, Per: dur("1s")},
		PeakVUs:    500,
		Duration:   dur("15m"),
	}
	assertSchedule(t, got, want)
}

func TestPlanByMode(t *testing.T) {
	tests := []struct {
		name string
		in   ir.Profile
		want *Schedule
	}{
		{
			name: "integration defaults to one VU, run once",
			in:   ir.Profile{Mode: ir.ModeIntegration},
			want: &Schedule{Mode: ir.ModeIntegration, Arrival: Closed, Stop: StopOnce, PeakVUs: 1},
		},
		{
			name: "integration ignores ramp and hold",
			in:   ir.Profile{Mode: ir.ModeIntegration, Ramp: "0 -> 50 over 1m", Hold: dur("5m")},
			want: &Schedule{Mode: ir.ModeIntegration, Arrival: Closed, Stop: StopOnce, PeakVUs: 1},
		},
		{
			name: "system carries the author's VU count",
			in:   ir.Profile{Mode: ir.ModeSystem, VUs: 3},
			want: &Schedule{Mode: ir.ModeSystem, Arrival: Closed, Stop: StopOnce, PeakVUs: 3},
		},
		{
			name: "load ramps then holds, duration-bounded",
			in:   ir.Profile{Mode: ir.ModeLoad, Ramp: "0 -> 200 over 2m", Hold: dur("10m")},
			want: &Schedule{
				Mode:    ir.ModeLoad,
				Arrival: Closed,
				Segments: []Segment{
					{Kind: Ramp, StartVUs: 0, EndVUs: 200, Duration: dur("2m")},
					{Kind: Hold, StartVUs: 200, EndVUs: 200, Duration: dur("10m")},
				},
				Stop:     StopDuration,
				PeakVUs:  200,
				Duration: dur("12m"),
			},
		},
		{
			name: "load with a flat VU count and no ramp is a single hold",
			in:   ir.Profile{Mode: ir.ModeLoad, VUs: 500, Hold: dur("10m")},
			want: &Schedule{
				Mode:     ir.ModeLoad,
				Arrival:  Closed,
				Segments: []Segment{{Kind: Hold, StartVUs: 500, EndVUs: 500, Duration: dur("10m")}},
				Stop:     StopDuration,
				PeakVUs:  500,
				Duration: dur("10m"),
			},
		},
		{
			name: "ramp with no hold is just the ramp",
			in:   ir.Profile{Mode: ir.ModeLoad, Ramp: "10 -> 100 over 3m"},
			want: &Schedule{
				Mode:     ir.ModeLoad,
				Arrival:  Closed,
				Segments: []Segment{{Kind: Ramp, StartVUs: 10, EndVUs: 100, Duration: dur("3m")}},
				Stop:     StopDuration,
				PeakVUs:  100,
				Duration: dur("3m"),
			},
		},
		{
			name: "soak is a moderate flat hold, duration-bounded",
			in:   ir.Profile{Mode: ir.ModeSoak, VUs: 50, Hold: dur("8h")},
			want: &Schedule{
				Mode:     ir.ModeSoak,
				Arrival:  Closed,
				Segments: []Segment{{Kind: Hold, StartVUs: 50, EndVUs: 50, Duration: dur("8h")}},
				Stop:     StopDuration,
				PeakVUs:  50,
				Duration: dur("8h"),
			},
		},
		{
			name: "an arrival cap switches load to the open model",
			in:   ir.Profile{Mode: ir.ModeLoad, VUs: 100, Hold: dur("5m"), ArrivalCap: "50/s"},
			want: &Schedule{
				Mode:       ir.ModeLoad,
				Arrival:    Open,
				Segments:   []Segment{{Kind: Hold, StartVUs: 100, EndVUs: 100, Duration: dur("5m")}},
				Stop:       StopDuration,
				ArrivalCap: &Rate{Count: 50, Per: dur("1s")},
				PeakVUs:    100,
				Duration:   dur("5m"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Plan(tt.in)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			assertSchedule(t, got, tt.want)
		})
	}
}

func TestPlanErrors(t *testing.T) {
	tests := []struct {
		name string
		in   ir.Profile
	}{
		{"unknown mode", ir.Profile{Mode: "chaos"}},
		{"load with neither ramp nor hold has no duration", ir.Profile{Mode: ir.ModeLoad, VUs: 100}},
		{"stress with a hold but no VUs has no peak", ir.Profile{Mode: ir.ModeStress, Hold: dur("5m")}},
		{"malformed ramp", ir.Profile{Mode: ir.ModeLoad, Ramp: "up to 500", Hold: dur("1m")}},
		{"ramp missing over clause", ir.Profile{Mode: ir.ModeLoad, Ramp: "0 -> 500"}},
		{"ramp non-numeric target", ir.Profile{Mode: ir.ModeLoad, Ramp: "0 -> lots over 1m"}},
		{"ramp non-positive duration", ir.Profile{Mode: ir.ModeLoad, Ramp: "0 -> 500 over 0s"}},
		{"malformed arrival cap", ir.Profile{Mode: ir.ModeLoad, Ramp: "0 -> 10 over 1m", ArrivalCap: "fast"}},
		{"arrival cap zero count", ir.Profile{Mode: ir.ModeLoad, Ramp: "0 -> 10 over 1m", ArrivalCap: "0/s"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Plan(tt.in); err == nil {
				t.Fatalf("Plan(%+v): want error, got nil", tt.in)
			}
		})
	}
}

func TestParseRamp(t *testing.T) {
	tests := []struct {
		in         string
		start, end int
		dur        ir.Duration
	}{
		{"0 -> 500 over 5m", 0, 500, dur("5m")},
		{"10->100 over 90s", 10, 100, dur("90s")},
		{"  0  ->  50  over  1h30m  ", 0, 50, dur("1h30m")},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseRamp(tt.in)
			if err != nil {
				t.Fatalf("parseRamp: %v", err)
			}
			if got.StartVUs != tt.start || got.EndVUs != tt.end || got.Duration != tt.dur {
				t.Fatalf("parseRamp(%q) = {%d -> %d over %s}, want {%d -> %d over %s}",
					tt.in, got.StartVUs, got.EndVUs, got.Duration, tt.start, tt.end, tt.dur)
			}
		})
	}
}

func TestArrivalCap(t *testing.T) {
	tests := []struct {
		in    string
		count int
		per   ir.Duration
	}{
		{"300/s", 300, dur("1s")},
		{"20/m", 20, dur("1m")},
		{"5/h", 5, dur("1h")},
		{"100/2s", 100, dur("2s")},
		{"250/ms", 250, dur("1ms")},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := arrivalCap(tt.in)
			if err != nil {
				t.Fatalf("arrivalCap: %v", err)
			}
			if got.Count != tt.count || got.Per != tt.per {
				t.Fatalf("arrivalCap(%q) = %d/%s, want %d/%s", tt.in, got.Count, time.Duration(got.Per), tt.count, time.Duration(tt.per))
			}
		})
	}

	if r, err := arrivalCap(""); err != nil || r != nil {
		t.Fatalf("arrivalCap(\"\") = %v, %v; want nil, nil", r, err)
	}
}

func TestRatePerSecond(t *testing.T) {
	tests := []struct {
		rate Rate
		want float64
	}{
		{Rate{Count: 300, Per: dur("1s")}, 300},
		{Rate{Count: 60, Per: dur("1m")}, 1},
		{Rate{Count: 100, Per: dur("2s")}, 50},
	}
	for _, tt := range tests {
		if got := tt.rate.PerSecond(); got != tt.want {
			t.Errorf("(%s).PerSecond() = %v, want %v", tt.rate, got, tt.want)
		}
	}
}

func assertSchedule(t *testing.T, got, want *Schedule) {
	t.Helper()
	if got.Mode != want.Mode || got.Arrival != want.Arrival || got.Stop != want.Stop ||
		got.PeakVUs != want.PeakVUs || got.Duration != want.Duration {
		t.Fatalf("schedule scalars:\n got %+v\nwant %+v", got, want)
	}
	if len(got.Segments) != len(want.Segments) {
		t.Fatalf("segments: got %+v, want %+v", got.Segments, want.Segments)
	}
	for i := range want.Segments {
		if got.Segments[i] != want.Segments[i] {
			t.Fatalf("segment %d: got %+v, want %+v", i, got.Segments[i], want.Segments[i])
		}
	}
	switch {
	case want.ArrivalCap == nil && got.ArrivalCap != nil:
		t.Fatalf("arrival cap: got %+v, want nil", got.ArrivalCap)
	case want.ArrivalCap != nil && got.ArrivalCap == nil:
		t.Fatalf("arrival cap: got nil, want %+v", want.ArrivalCap)
	case want.ArrivalCap != nil && *got.ArrivalCap != *want.ArrivalCap:
		t.Fatalf("arrival cap: got %+v, want %+v", *got.ArrivalCap, *want.ArrivalCap)
	}
}
