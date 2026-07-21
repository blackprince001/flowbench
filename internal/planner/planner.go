package planner

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
)

// ArrivalModel is whether concurrency (closed) or arrival rate (open) is the
// controlled variable. An arrival cap only bites under the open model.
type ArrivalModel string

const (
	Closed ArrivalModel = "closed"
	Open   ArrivalModel = "open"
)

// StopReason is the condition that ends a schedule.
type StopReason string

const (
	StopOnce       StopReason = "once"       // integration/system: once, or once per fixture row
	StopDuration   StopReason = "duration"   // load/soak: until the segments elapse
	StopThresholds StopReason = "thresholds" // stress: until a threshold breaks (the knee)
)

// SegmentKind distinguishes a linear ramp from a flat hold.
type SegmentKind string

const (
	Ramp SegmentKind = "ramp"
	Hold SegmentKind = "hold"
)

// Segment is one phase of the VU curve. A hold has StartVUs == EndVUs.
type Segment struct {
	Kind     SegmentKind `json:"kind"`
	StartVUs int         `json:"start_vus"`
	EndVUs   int         `json:"end_vus"`
	Duration ir.Duration `json:"duration"`
}

// Rate is a request-rate ceiling from the "N/unit" form, e.g. "300/s".
type Rate struct {
	Count int         `json:"count"`
	Per   ir.Duration `json:"per"`
}

// PerSecond normalizes the cap to requests per second.
func (r Rate) PerSecond() float64 {
	return float64(r.Count) / time.Duration(r.Per).Seconds()
}

func (r Rate) String() string {
	return fmt.Sprintf("%d/%s", r.Count, time.Duration(r.Per))
}

// Schedule is the executor's contract: the VU curve, arrival model, stop
// condition, and any arrival cap. Self-contained — the profile is not re-read.
type Schedule struct {
	Mode       ir.Mode      `json:"mode"`
	Arrival    ArrivalModel `json:"arrival"`
	Segments   []Segment    `json:"segments,omitempty"`
	Stop       StopReason   `json:"stop"`
	ArrivalCap *Rate        `json:"arrival_cap,omitempty"`
	PeakVUs    int          `json:"peak_vus"`
	Duration   ir.Duration  `json:"duration,omitempty"` // total of the timed segments
}

// Plan turns a validated profile into a schedule, re-parsing the ramp and
// arrival-cap strings that IR validation leaves opaque.
func Plan(p ir.Profile) (*Schedule, error) {
	cap, err := arrivalCap(p.ArrivalCap)
	if err != nil {
		return nil, err
	}

	switch p.Mode {
	case ir.ModeIntegration, ir.ModeSystem:
		return planOnce(p)
	case ir.ModeLoad, ir.ModeSoak:
		return planSustained(p, cap, StopDuration)
	case ir.ModeStress:
		return planSustained(p, cap, StopThresholds)
	default:
		return nil, fmt.Errorf("planner: unknown mode %q", p.Mode)
	}
}

// planOnce covers integration and system: a small fixed VU count, run once.
// Ramp and hold are ignored, and so is any arrival cap — a sustained req/s
// ceiling has no meaning over a single pass, and per-call pacing is the SDK's
// pace lever (PRD §10.9). Integration defaults to one VU.
func planOnce(p ir.Profile) (*Schedule, error) {
	vus := p.VUs
	if vus == 0 {
		vus = 1
	}
	return &Schedule{
		Mode:    p.Mode,
		Arrival: Closed,
		Stop:    StopOnce,
		PeakVUs: vus,
	}, nil
}

// planSustained covers load, stress, and soak: an optional ramp to a peak then
// a hold. The peak is the ramp target, else the flat VU count. At least one
// timed segment is required.
func planSustained(p ir.Profile, cap *Rate, stop StopReason) (*Schedule, error) {
	var segs []Segment
	peak := p.VUs

	if p.Ramp != "" {
		r, err := parseRamp(p.Ramp)
		if err != nil {
			return nil, err
		}
		segs = append(segs, r)
		peak = r.EndVUs
	}

	if p.Hold > 0 {
		segs = append(segs, Segment{Kind: Hold, StartVUs: peak, EndVUs: peak, Duration: p.Hold})
	}

	if len(segs) == 0 {
		return nil, fmt.Errorf("planner: %s mode needs a ramp or a hold to define a duration", p.Mode)
	}
	if peak <= 0 {
		return nil, fmt.Errorf("planner: %s mode needs a positive vu count", p.Mode)
	}

	arrival := Closed
	if cap != nil {
		arrival = Open
	}

	var total time.Duration
	for _, s := range segs {
		total += time.Duration(s.Duration)
	}

	return &Schedule{
		Mode:       p.Mode,
		Arrival:    arrival,
		Segments:   segs,
		Stop:       stop,
		ArrivalCap: cap,
		PeakVUs:    peak,
		Duration:   ir.Duration(total),
	}, nil
}

// parseRamp reads "start -> end over duration", e.g. "0 -> 500 over 5m".
func parseRamp(s string) (Segment, error) {
	fail := func() (Segment, error) {
		return Segment{}, fmt.Errorf("planner: invalid ramp %q, want \"start -> end over duration\" like \"0 -> 500 over 5m\"", s)
	}

	left, right, ok := cut(s, "over")
	if !ok {
		return fail()
	}
	from, to, ok := cut(left, "->")
	if !ok {
		return fail()
	}

	start, err := strconv.Atoi(strings.TrimSpace(from))
	if err != nil {
		return fail()
	}
	end, err := strconv.Atoi(strings.TrimSpace(to))
	if err != nil {
		return fail()
	}
	d, err := time.ParseDuration(strings.TrimSpace(right))
	if err != nil {
		return fail()
	}

	if start < 0 || end < 0 {
		return Segment{}, fmt.Errorf("planner: ramp %q has a negative vu count", s)
	}
	if d <= 0 {
		return Segment{}, fmt.Errorf("planner: ramp %q has a non-positive duration", s)
	}

	return Segment{Kind: Ramp, StartVUs: start, EndVUs: end, Duration: ir.Duration(d)}, nil
}

// arrivalCap parses the optional "N/unit" cap, returning nil when unset. The
// divisor may be a bare unit (s, m, h, ms) or a full duration ("2s").
func arrivalCap(s string) (*Rate, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	count, per, ok := cut(s, "/")
	if !ok {
		return nil, fmt.Errorf("planner: invalid arrival_cap %q, want \"N/unit\" like \"300/s\"", s)
	}

	n, err := strconv.Atoi(strings.TrimSpace(count))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("planner: arrival_cap %q needs a positive count", s)
	}

	window := strings.TrimSpace(per)
	if window == "" {
		return nil, fmt.Errorf("planner: invalid arrival_cap %q, want \"N/unit\" like \"300/s\"", s)
	}
	if window[0] < '0' || window[0] > '9' {
		window = "1" + window // bare unit → one of that unit
	}
	d, err := time.ParseDuration(window)
	if err != nil || d <= 0 {
		return nil, fmt.Errorf("planner: invalid arrival_cap window in %q", s)
	}

	return &Rate{Count: n, Per: ir.Duration(d)}, nil
}

// cut splits s on the first sep and trims both halves, reporting absence.
func cut(s, sep string) (before, after string, ok bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):]), true
}

// CheckCeilings enforces a target's safety ceilings against a planned schedule,
// ahead of any load. A zero ceiling is unset. The RPS ceiling only binds a
// declared arrival cap; without one the request rate is emergent and cannot be
// bounded before the run.
func CheckCeilings(s *Schedule, maxVUs, maxRPS int) error {
	if maxVUs > 0 && s.PeakVUs > maxVUs {
		return fmt.Errorf("planner: schedule peaks at %d VUs, above the target ceiling of %d", s.PeakVUs, maxVUs)
	}
	if maxRPS > 0 && s.ArrivalCap != nil {
		if rps := s.ArrivalCap.PerSecond(); rps > float64(maxRPS) {
			return fmt.Errorf("planner: arrival cap %s exceeds the target ceiling of %d rps", s.ArrivalCap, maxRPS)
		}
	}
	return nil
}
