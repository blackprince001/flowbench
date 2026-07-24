package span

// Payload is what a call span captured: the response's identity — method, URL,
// status — plus the request and response bodies, redacted and size-capped. It is
// populated only for kept traces (see Finalize), so unsampled iterations carry
// none.
//
// Status matters as much as the bodies. A span's outcome says `failed`; only the
// status says whether that was a 500, a 404 or a 403, and on a throttle
// Retry-After is the server telling you how long it wanted you to wait.
type Payload struct {
	Method     string `json:"method,omitempty"`
	URL        string `json:"url,omitempty"`
	Status     int    `json:"status,omitempty"`
	RetryAfter string `json:"retry_after,omitempty"`
	ReqBytes   int    `json:"req_bytes,omitempty"`
	RespBytes  int    `json:"resp_bytes,omitempty"`
	Request    string `json:"request,omitempty"`
	Response   string `json:"response,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// SetRaw records the call's bodies by reference for possible capture later.
// Cheap — no copy, no redaction — so it is safe to call on every call.
func (s *Span) SetRaw(req, resp []byte) {
	s.rawReq, s.rawResp = req, resp
}

// SetCall records the call's identity and result. Only scalars and existing
// string references are held, so like SetRaw this costs nothing on the calls
// whose traces are later dropped.
func (s *Span) SetCall(method, url string, status int, retryAfter string) {
	s.callMethod, s.callURL, s.callStatus, s.retryAfter = method, url, status, retryAfter
}

// Finalize turns each captured raw body in the tree into a stored Payload,
// redacting first (so a secret can't survive being split by the cap) and then
// truncating at maxBytes. A negative maxBytes captures no bodies; the raw
// references are always released. Call this only on traces being kept.
func Finalize(root *Span, redact func([]byte) []byte, maxBytes int) {
	if root == nil {
		return
	}
	if root.callStatus != 0 || root.rawReq != nil || root.rawResp != nil {
		p := &Payload{
			Method:     root.callMethod,
			Status:     root.callStatus,
			RetryAfter: root.retryAfter,
			ReqBytes:   len(root.rawReq),
			RespBytes:  len(root.rawResp),
		}
		// A URL can carry a credential in its path or query, so it is redacted
		// like any captured body — and unlike the bodies it is never size-capped
		// away, so redaction is the only thing protecting it.
		if root.callURL != "" {
			p.URL = string(redact([]byte(root.callURL)))
		}
		if maxBytes >= 0 {
			var t1, t2 bool
			p.Request, t1 = capBody(root.rawReq, redact, maxBytes)
			p.Response, t2 = capBody(root.rawResp, redact, maxBytes)
			p.Truncated = t1 || t2
		}
		root.Payload = p
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
