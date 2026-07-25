// A local service that demands a different auth scheme on every endpoint, so
// each one either authenticates or gets a 401 — the point being that a scheme
// which quietly sends nothing fails the run rather than passing it.
//
// The credentials below are this service's own. The flow supplies them from
// the environment (ADR 0005), never from a file.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

const (
	apiToken     = "tok_demo_bearer_9f8e7d"
	basicUser    = "reports-service"
	basicPass    = "s3cr3t-basic-pw"
	apiKey       = "ak_demo_5b4a3c2d"
	sessionValue = "sess_demo_abc123"
	clientID     = "client_demo"
	clientSecret = "cs_demo_shhh"
	accessToken  = "at_demo_issued_by_the_stub"
	signingKey   = "whsec_demo_signing_key"
	signingKeyID = "key-2026-07"
)

// signingWindow is how stale a signature may be. Credentials are attached per
// attempt, so a retried request re-signs and stays inside it; a signer that
// stamped once per step would fall out of it under backoff.
const signingWindow = 30 * time.Second

func main() {
	http.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		if !want(w, r, "Authorization", "Bearer "+apiToken) {
			return
		}
		ok(w, `{"orders":[]}`)
	})

	// Public. It refuses a credential rather than ignoring one, so a flow-level
	// default leaking onto an opted-out step shows up as a failure.
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			deny(w, "this endpoint is public; sending a credential to it is a bug")
			return
		}
		ok(w, `{"status":"up"}`)
	})

	http.HandleFunc("/reports", func(w http.ResponseWriter, r *http.Request) {
		user, pass, present := r.BasicAuth()
		if !present || user != basicUser || pass != basicPass {
			deny(w, "basic auth required")
			return
		}
		ok(w, `{"reports":3}`)
	})

	http.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if !want(w, r, "X-Api-Key", apiKey) {
			return
		}
		ok(w, `{"hits":12}`)
	})

	// The same key, in the query string — the placement plenty of older APIs
	// take and nothing else does.
	http.HandleFunc("/legacy/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != apiKey {
			deny(w, "api_key query parameter required")
			return
		}
		ok(w, `{"hits":12}`)
	})

	http.HandleFunc("/account", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil || c.Value != sessionValue {
			deny(w, "session cookie required")
			return
		}
		ok(w, `{"account":"ada"}`)
	})

	// The client-credentials grant. Counting the hits is the interesting part:
	// however many VUs run, this should be reached once.
	grants := 0
	http.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			deny(w, "malformed grant")
			return
		}
		if r.PostForm.Get("grant_type") != "client_credentials" {
			deny(w, "unsupported grant_type")
			return
		}
		// RFC 6749 allows the credentials as HTTP Basic or as form fields.
		id, sec, viaBasic := r.BasicAuth()
		if !viaBasic {
			id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
		}
		if id != clientID || sec != clientSecret {
			deny(w, "invalid_client")
			return
		}
		grants++
		log.Printf("issued access token (grant %d, scope %q)", grants, r.PostForm.Get("scope"))
		ok(w, fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, accessToken))
	})

	http.HandleFunc("/payments", func(w http.ResponseWriter, r *http.Request) {
		if !want(w, r, "Authorization", "Bearer "+accessToken) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"accepted":true}`))
	})

	// Recomputes the signature from what actually arrived, rather than trusting
	// that the client signed the right thing.
	http.HandleFunc("/webhooks/replay", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			deny(w, "unreadable body")
			return
		}
		if r.Header.Get("X-Key-Id") != signingKeyID {
			deny(w, "unknown signing key")
			return
		}
		stamp, err := strconv.ParseInt(r.Header.Get("X-Timestamp"), 10, 64)
		if err != nil {
			deny(w, "missing or malformed X-Timestamp")
			return
		}
		if age := time.Since(time.Unix(stamp, 0)); age > signingWindow || age < -signingWindow {
			deny(w, "signature timestamp is outside the replay window")
			return
		}

		digest := sha256.Sum256(body)
		canonical := fmt.Sprintf("%s\n%s\n%d\n%s",
			r.Method, r.URL.EscapedPath(), stamp, hex.EncodeToString(digest[:]))
		mac := hmac.New(sha256.New, []byte(signingKey))
		mac.Write([]byte(canonical))
		want := hex.EncodeToString(mac.Sum(nil))

		if !hmac.Equal([]byte(r.Header.Get("X-Signature")), []byte(want)) {
			deny(w, "bad signature")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"replayed":true}`))
	})

	// Reflects the credential into its own response, so a captured payload has
	// something in it that redaction has to scrub.
	http.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		seen, _ := json.Marshal(r.Header.Get("Authorization"))
		ok(w, fmt.Sprintf(`{"seen":%s}`, seen))
	})

	log.Println("auth stub listening on :8090 — every endpoint demands a different scheme")
	log.Println("export the credentials it expects:")
	log.Printf("  export DEMO_API_TOKEN=%s DEMO_USER=%s DEMO_PASSWORD=%s \\", apiToken, basicUser, basicPass)
	log.Printf("         DEMO_API_KEY=%s DEMO_SESSION=%s \\", apiKey, sessionValue)
	log.Printf("         DEMO_CLIENT_ID=%s DEMO_CLIENT_SECRET=%s \\", clientID, clientSecret)
	log.Printf("         DEMO_SIGNING_SECRET=%s DEMO_SIGNING_KEY_ID=%s", signingKey, signingKeyID)
	log.Fatal(http.ListenAndServe(":8090", nil))
}

func want(w http.ResponseWriter, r *http.Request, header, value string) bool {
	if r.Header.Get(header) != value {
		deny(w, header+" did not carry the expected credential")
		return false
	}
	return true
}

func ok(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(body))
}

func deny(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `{"error":%q}`, reason)
}
