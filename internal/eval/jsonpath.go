package eval

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// segment is one step of a parsed JSONPath: a map key or an array index.
type segment struct {
	key   string
	index int
	isKey bool
}

// parsePath parses the JSONPath subset the DSL supports: a leading "$", dot
// keys ($.a.b), bracket keys ($['a.b']), and array indices ($.a[0]). Filters,
// wildcards, and recursive descent are intentionally unsupported.
func parsePath(path string) ([]segment, error) {
	if path == "" || path[0] != '$' {
		return nil, fmt.Errorf("path %q must start with $", path)
	}
	var segs []segment
	i := 1
	for i < len(path) {
		switch path[i] {
		case '.':
			i++
			if i < len(path) && path[i] == '.' {
				return nil, fmt.Errorf("recursive descent (..) is not supported: %q", path)
			}
			start := i
			for i < len(path) && isIdentByte(path[i]) {
				i++
			}
			if i == start {
				return nil, fmt.Errorf("empty key after '.' in %q", path)
			}
			segs = append(segs, segment{key: path[start:i], isKey: true})
		case '[':
			seg, next, err := parseBracket(path, i)
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg)
			i = next
		default:
			return nil, fmt.Errorf("unexpected %q at position %d in %q", path[i], i, path)
		}
	}
	return segs, nil
}

func parseBracket(path string, i int) (segment, int, error) {
	i++ // past '['
	if i < len(path) && (path[i] == '\'' || path[i] == '"') {
		quote := path[i]
		i++
		start := i
		for i < len(path) && path[i] != quote {
			i++
		}
		if i >= len(path) {
			return segment{}, 0, fmt.Errorf("unterminated quoted key in %q", path)
		}
		key := path[start:i]
		i++ // past closing quote
		if i >= len(path) || path[i] != ']' {
			return segment{}, 0, fmt.Errorf("expected ] after quoted key in %q", path)
		}
		return segment{key: key, isKey: true}, i + 1, nil
	}
	start := i
	for i < len(path) && path[i] != ']' {
		i++
	}
	if i >= len(path) {
		return segment{}, 0, fmt.Errorf("unterminated [ in %q", path)
	}
	n, err := strconv.Atoi(strings.TrimSpace(path[start:i]))
	if err != nil {
		return segment{}, 0, fmt.Errorf("array index %q in %q is not an integer", path[start:i], path)
	}
	return segment{index: n}, i + 1, nil
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// queryJSON walks path into a JSON body. The bool is false when the body is
// valid JSON but the path names nothing; a non-JSON body is an error.
func queryJSON(body []byte, path string) (any, bool, error) {
	segs, err := parsePath(path)
	if err != nil {
		return nil, false, err
	}
	if len(body) == 0 {
		return nil, false, nil
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false, fmt.Errorf("response body is not JSON: %w", err)
	}
	cur := root
	for _, s := range segs {
		if s.isKey {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false, nil
			}
			if cur, ok = m[s.key]; !ok {
				return nil, false, nil
			}
			continue
		}
		arr, ok := cur.([]any)
		if !ok {
			return nil, false, nil
		}
		idx := s.index
		if idx < 0 {
			idx += len(arr)
		}
		if idx < 0 || idx >= len(arr) {
			return nil, false, nil
		}
		cur = arr[idx]
	}
	return cur, true, nil
}
