package ir

import "fmt"

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
