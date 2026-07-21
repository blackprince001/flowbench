package span

// Payload is a call's captured request and response bodies, redacted and size-
// capped, the stored form the waterfall view shows. It is populated only for
// kept traces (see Finalize), so unsampled iterations carry none.
type Payload struct {
	Request   string `json:"request,omitempty"`
	Response  string `json:"response,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// SetRaw records the call's bodies by reference for possible capture later.
// Cheap — no copy, no redaction — so it is safe to call on every call.
func (s *Span) SetRaw(req, resp []byte) {
	s.rawReq, s.rawResp = req, resp
}

// Finalize turns each captured raw body in the tree into a stored Payload,
// redacting first (so a secret can't survive being split by the cap) and then
// truncating at maxBytes. A negative maxBytes captures no bodies; the raw
// references are always released. Call this only on traces being kept.
func Finalize(root *Span, redact func([]byte) []byte, maxBytes int) {
	if root == nil {
		return
	}
	if root.rawReq != nil || root.rawResp != nil {
		if maxBytes >= 0 {
			req, t1 := capBody(root.rawReq, redact, maxBytes)
			resp, t2 := capBody(root.rawResp, redact, maxBytes)
			if req != "" || resp != "" {
				root.Payload = &Payload{Request: req, Response: resp, Truncated: t1 || t2}
			}
		}
		root.rawReq, root.rawResp = nil, nil
	}
	for _, c := range root.Children {
		Finalize(c, redact, maxBytes)
	}
}

func capBody(b []byte, redact func([]byte) []byte, maxBytes int) (string, bool) {
	if len(b) == 0 {
		return "", false
	}
	r := redact(b)
	if maxBytes > 0 && len(r) > maxBytes {
		return string(r[:maxBytes]), true
	}
	return string(r), false
}
