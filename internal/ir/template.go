package ir

import "fmt"

// ExpandTemplates replaces every well-formed "{{ ref }}" placeholder in s
// with the value resolve returns for ref ("token", "user.email",
// "env.API_HOST"). The IR owns the placeholder syntax, so every consumer
// (executor, adapters) expands it identically. Malformed placeholders are
// left verbatim — validation already rejects them pre-run. The first
// resolution failure aborts the expansion.
func ExpandTemplates(s string, resolve func(ref string) (string, error)) (string, error) {
	var firstErr error
	out := templateRe.ReplaceAllStringFunc(s, func(m string) string {
		ref := templateRe.FindStringSubmatch(m)[1]
		v, err := resolve(ref)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("resolve {{ %s }}: %w", ref, err)
			}
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}
