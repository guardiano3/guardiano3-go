package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Transport wraps an http.RoundTripper with everything the generated client
// does not do: authentication, a correlation-friendly idempotency key, retries
// with jitter, and turning error responses into *APIError.
//
// Implemented as a RoundTripper so it composes with the generated client
// (which takes an *http.Client) instead of wrapping every operation by hand --
// 36 operations is 36 chances to forget one.
type Transport struct {
	Base        http.RoundTripper
	Credential  string
	RetryPolicy RetryPolicy
	// sleep is injectable so tests assert the retry logic without waiting.
	sleep func(time.Duration)
}

// NewTransport builds a Transport with the default policy.
func NewTransport(credential string) *Transport {
	return &Transport{Credential: credential, RetryPolicy: DefaultRetryPolicy()}
}

func newIdempotencyKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// A key that is not unique is worse than none: it would make two
		// different writes collide as a replay. Fall back to the clock, which
		// is unique enough for one process's requests.
		return "go-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func isUnsafe(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// RoundTrip authenticates, retries, and maps failures.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	policy := t.RetryPolicy
	if policy.MaxAttempts == 0 {
		policy = DefaultRetryPolicy()
	}
	sleep := t.sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	// The body has to be buffered: a retry needs to send it again, and a
	// request body is a one-shot reader.
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}

	if t.Credential != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+t.Credential)
	}
	// Generated **once**, here, outside the retry loop. Inside it, every retry
	// would be a fresh write -- the exact failure idempotency keys exist to
	// prevent. A key the caller set is never overwritten.
	if isUnsafe(req.Method) && req.Header.Get("Idempotency-Key") == "" {
		req.Header.Set("Idempotency-Key", newIdempotencyKey())
	}

	var lastResp *http.Response
	for attempt := 1; ; attempt++ {
		attemptReq := req.Clone(req.Context())
		if body != nil {
			attemptReq.Body = io.NopCloser(bytes.NewReader(body))
			attemptReq.ContentLength = int64(len(body))
		}

		resp, err := base.RoundTrip(attemptReq)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 400 {
			return resp, nil
		}

		// Read the body to classify, then put it back: the caller still gets a
		// readable response, which is what lets the generated client keep
		// working unchanged.
		payload, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return resp, readErr
		}
		resp.Body = io.NopCloser(bytes.NewReader(payload))
		apiErr := FromResponse(resp.StatusCode, payload, resp.Header)
		lastResp = resp

		if !policy.ShouldRetry(attempt, resp.StatusCode, apiErr.Code) {
			return lastResp, nil
		}
		sleep(policy.Backoff(attempt+1, time.Duration(apiErr.RetryAfter)*time.Second))
	}
}

// Classify reads an error response into an *APIError. Call it on any non-2xx
// the generated client hands back.
func Classify(resp *http.Response) *APIError {
	if resp == nil || resp.StatusCode < 400 {
		return nil
	}
	payload, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(payload))
	return FromResponse(resp.StatusCode, payload, resp.Header)
}
