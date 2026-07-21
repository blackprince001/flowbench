package collector

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/executor"
	"github.com/blackprince001/flowbench/internal/span"
)

// Op is a threshold comparison operator.
type Op string

const (
	OpLt  Op = "<"
	OpLte Op = "<="
	OpGt  Op = ">"
	OpGte Op = ">="
)

// kind is the metric family a threshold reads.
type kind int

const (
	kindLatency kind = iota // an aggregation of the CO-aware per-flow-run latency
	kindErrorRate
	kindThrottleRate
)

// Threshold is a parsed pass/fail gate, e.g. `p95(latency) < 800ms`. limit is
// held in the metric's canonical unit: nanoseconds for latency, a 0..1 fraction
// for a rate.
type Threshold struct {
	Raw   string
	label string
	kind  kind
	agg   string  // latency only: "p", "avg", "max", "min"
	quant float64 // latency "p" aggregation only
	op    Op
	limit float64
}

// Outcome is one evaluated threshold or trend.
type Outcome struct {
	Expr   string
	Pass   bool
	Detail string
}

var funcRe = regexp.MustCompile(`^([a-z][a-z0-9]*)\(\s*([a-z_]+)\s*\)$`)

// ParseThresholds parses each expression, returning the first error with its
// expression so a malformed threshold is a clear pre-run error.
func ParseThresholds(exprs []string) ([]Threshold, error) {
	ths := make([]Threshold, 0, len(exprs))
	for _, e := range exprs {
		t, err := ParseThreshold(e)
		if err != nil {
			return nil, err
		}
		ths = append(ths, t)
	}
	return ths, nil
}

// ParseThreshold parses one expression of the form `<metric> <op> <value>`.
func ParseThreshold(expr string) (Threshold, error) {
	lhs, op, rhs, err := splitOp(expr)
	if err != nil {
		return Threshold{}, fmt.Errorf("threshold %q: %w", expr, err)
	}

	t := Threshold{Raw: strings.TrimSpace(expr), label: lhs, op: op}
	if err := t.parseMetric(lhs); err != nil {
		return Threshold{}, fmt.Errorf("threshold %q: %w", expr, err)
	}
	if err := t.parseLimit(rhs); err != nil {
		return Threshold{}, fmt.Errorf("threshold %q: %w", expr, err)
	}
	return t, nil
}

// splitOp finds the comparison operator, matching two-character operators
// before their single-character prefixes.
func splitOp(expr string) (lhs string, op Op, rhs string, err error) {
	for _, o := range []Op{OpLte, OpGte, OpLt, OpGt} {
		if i := strings.Index(expr, string(o)); i >= 0 {
			return strings.TrimSpace(expr[:i]), o, strings.TrimSpace(expr[i+len(o):]), nil
		}
	}
	return "", "", "", fmt.Errorf("no comparison operator (want one of <, <=, >, >=)")
}

func (t *Threshold) parseMetric(lhs string) error {
	switch lhs {
	case "error_rate":
		t.kind = kindErrorRate
		return nil
	case "throttle_rate":
		t.kind = kindThrottleRate
		return nil
	}

	m := funcRe.FindStringSubmatch(lhs)
	if m == nil {
		return fmt.Errorf("unknown metric %q (want error_rate, throttle_rate, or pNN/avg/max/min(latency))", lhs)
	}
	fn, arg := m[1], m[2]
	if arg != "latency" {
		return fmt.Errorf("%q takes latency, not %q", fn, arg)
	}
	t.kind = kindLatency

	switch fn {
	case "avg", "max", "min":
		t.agg = fn
		return nil
	}
	if strings.HasPrefix(fn, "p") {
		n, err := strconv.Atoi(fn[1:])
		if err != nil || n <= 0 || n > 100 {
			return fmt.Errorf("percentile %q must be p1..p100", fn)
		}
		t.agg, t.quant = "p", float64(n)/100
		return nil
	}
	return fmt.Errorf("unknown latency aggregation %q (want pNN, avg, max, or min)", fn)
}

func (t *Threshold) parseLimit(rhs string) error {
	if t.kind == kindLatency {
		d, err := time.ParseDuration(rhs)
		if err != nil || d < 0 {
			return fmt.Errorf("latency threshold needs a non-negative duration like 800ms, got %q", rhs)
		}
		t.limit = float64(d)
		return nil
	}

	s := rhs
	div := 1.0
	if strings.HasSuffix(s, "%") {
		s, div = strings.TrimSuffix(s, "%"), 100
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v < 0 {
		return fmt.Errorf("rate threshold needs a non-negative number or percent, got %q", rhs)
	}
	t.limit = v / div
	return nil
}

// Evaluate applies the thresholds to a run's samples point-in-time, one Outcome
// per threshold. This is the load/stress posture.
func Evaluate(thresholds []Threshold, res *executor.Result) []Outcome {
	out := make([]Outcome, 0, len(thresholds))
	for _, t := range thresholds {
		actual := t.actual(res.Samples)
		out = append(out, Outcome{
			Expr:   t.Raw,
			Pass:   compare(actual, t.op, t.limit),
			Detail: fmt.Sprintf("%s = %s, want %s %s", t.label, t.format(actual), t.op, t.format(t.limit)),
		})
	}
	return out
}

// actual computes the threshold's metric over the given samples.
func (t Threshold) actual(samples []executor.Sample) float64 {
	switch t.kind {
	case kindErrorRate:
		return errorRate(samples)
	case kindThrottleRate:
		return throttleRate(samples)
	default:
		return float64(latencyAgg(samples, t.agg, t.quant))
	}
}

func (t Threshold) format(v float64) string {
	if t.kind == kindLatency {
		return time.Duration(v).Round(time.Microsecond).String()
	}
	return fmt.Sprintf("%.2f%%", v*100)
}

func compare(actual float64, op Op, limit float64) bool {
	switch op {
	case OpLt:
		return actual < limit
	case OpLte:
		return actual <= limit
	case OpGt:
		return actual > limit
	case OpGte:
		return actual >= limit
	}
	return false
}

func latencyAgg(samples []executor.Sample, agg string, quant float64) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	switch agg {
	case "p":
		return executor.Percentile(samples, quant)
	case "avg":
		var sum time.Duration
		for _, s := range samples {
			sum += s.Latency()
		}
		return sum / time.Duration(len(samples))
	case "max":
		var m time.Duration
		for _, s := range samples {
			if l := s.Latency(); l > m {
				m = l
			}
		}
		return m
	case "min":
		m := samples[0].Latency()
		for _, s := range samples[1:] {
			if l := s.Latency(); l < m {
				m = l
			}
		}
		return m
	}
	return 0
}

func errorRate(samples []executor.Sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	n := 0
	for _, s := range samples {
		if s.Outcome == span.OutcomeFailed {
			n++
		}
	}
	return float64(n) / float64(len(samples))
}

func throttleRate(samples []executor.Sample) float64 {
	if len(samples) == 0 {
		return 0
	}
	n := 0
	for _, s := range samples {
		if s.Throttled {
			n++
		}
	}
	return float64(n) / float64(len(samples))
}
