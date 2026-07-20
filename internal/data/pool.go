package data

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/blackprince001/flowbench/internal/ir"
)

// Row is one fixture record: field name to string value. Non-scalar JSON
// values are carried as their JSON text so templates inject them verbatim.
type Row map[string]string

// ErrExhausted is returned by Next when a unique-per-vu pool runs out of rows
// under the "fail" exhaustion policy.
var ErrExhausted = errors.New("data pool exhausted")

// Pool dispenses rows to concurrent consumers. Every draw goes through the
// mutex, so unique-per-vu hands each row to exactly one caller and the seeded
// RNG stays reproducible.
type Pool struct {
	name    string
	rows    []Row
	dist    ir.PoolDistribution
	exhaust ir.PoolExhaustion

	mu     sync.Mutex
	cursor int
	rng    *rand.Rand
}

// Load reads dp.Source (resolved against baseDir when relative) and builds a
// pool. seed makes the random distribution reproducible.
func Load(dp ir.DataPool, baseDir string, seed int64) (*Pool, error) {
	rows, err := loadRows(dp, baseDir)
	if err != nil {
		return nil, err
	}
	return New(dp, rows, seed)
}

// New builds a pool from already-loaded rows.
func New(dp ir.DataPool, rows []Row, seed int64) (*Pool, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("data pool %q has no rows", dp.Name)
	}
	dist := dp.Distribution
	if dist == "" {
		dist = ir.DistributeUniquePerVU
	}
	exhaust := dp.OnExhausted
	if exhaust == "" {
		exhaust = ir.ExhaustFail
	}
	return &Pool{
		name:    dp.Name,
		rows:    rows,
		dist:    dist,
		exhaust: exhaust,
		rng:     rand.New(rand.NewSource(seed)),
	}, nil
}

func (p *Pool) Name() string { return p.name }
func (p *Pool) Len() int     { return len(p.rows) }

// Next returns a row for one consumer. ok is false only when a unique-per-vu
// pool is exhausted under the "stop" policy; under "fail" it returns
// ErrExhausted, and every other case returns a row.
func (p *Pool) Next() (Row, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.dist {
	case ir.DistributeRandom:
		return p.rows[p.rng.Intn(len(p.rows))], true, nil
	case ir.DistributeRoundRobin:
		row := p.rows[p.cursor]
		p.cursor = (p.cursor + 1) % len(p.rows)
		return row, true, nil
	default: // unique-per-vu: a distinct row per draw, no sharing
		if p.cursor < len(p.rows) {
			row := p.rows[p.cursor]
			p.cursor++
			return row, true, nil
		}
		return p.exhausted()
	}
}

// exhausted applies the on_exhausted policy; the caller holds the mutex.
func (p *Pool) exhausted() (Row, bool, error) {
	switch p.exhaust {
	case ir.ExhaustCycle:
		p.cursor = 1
		return p.rows[0], true, nil
	case ir.ExhaustStop:
		return nil, false, nil
	default: // fail
		return nil, false, fmt.Errorf("%w: %q ran out after %d rows", ErrExhausted, p.name, len(p.rows))
	}
}

func loadRows(dp ir.DataPool, baseDir string) ([]Row, error) {
	if dp.Source == "" {
		return nil, fmt.Errorf("data pool %q has no source file", dp.Name)
	}
	path := dp.Source
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read data pool %q: %w", dp.Name, err)
	}

	format := dp.Format
	if format == "" {
		format = inferFormat(path)
	}
	switch format {
	case ir.PoolCSV:
		return parseCSV(b)
	case ir.PoolJSON:
		return parseJSON(b)
	default:
		return nil, fmt.Errorf("data pool %q: cannot infer format from %q; set format explicitly", dp.Name, filepath.Ext(path))
	}
}

func inferFormat(path string) ir.PoolFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return ir.PoolCSV
	case ".json":
		return ir.PoolJSON
	}
	return ""
}

func parseCSV(b []byte) ([]Row, error) {
	records, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("CSV has no header row")
	}
	header := records[0]
	rows := make([]Row, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := make(Row, len(header))
		for j, key := range header {
			row[key] = rec[j]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseJSON(b []byte) ([]Row, error) {
	var raw []map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: expected an array of objects: %w", err)
	}
	rows := make([]Row, 0, len(raw))
	for i, obj := range raw {
		row := make(Row, len(obj))
		for k, v := range obj {
			s, err := scalarString(v)
			if err != nil {
				return nil, fmt.Errorf("JSON row %d field %q: %w", i, k, err)
			}
			row[k] = s
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func scalarString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}
