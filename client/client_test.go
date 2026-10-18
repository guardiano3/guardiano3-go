package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func envelopeBody(code, message string) string {
	return `{"error":{"code":"` + code + `","message":"` + message +
		`","requestId":"req_0123456789abcdef0123456789abcdef"}}`
}

func TestErrorCodeDecidesTheKindNotTheStatus(t *testing.T) {
	// The whole point of the taxonomy: a caller branches on what went wrong,
	// not on an integer.
	err := FromResponse(429, []byte(envelopeBody("QUOTA_EXCEEDED", "spent")), nil)
	if !err.IsQuota() {
		t.Fatalf("429 QUOTA_EXCEEDED should be a quota failure, got %s", err.Kind)
	}
	if !err.IsRateLimit() {
		t.Fatal("a quota is still a rate limit; callers that catch broadly must still catch it")
	}
}

func TestAnUnknownCodeFallsBackToTheStatus(t *testing.T) {
	// New codes are added without a version bump. A client that broke on one
	// would make adding a code a breaking change.
	err := FromResponse(404, []byte(envelopeBody("SOME_FUTURE_CODE", "nope")), nil)
	if !err.IsNotFound() {
		t.Fatalf("unknown code should fall back to the status kind, got %s", err.Kind)
	}
	if err.Code != "SOME_FUTURE_CODE" {
		t.Fatal("the unrecognised code must still reach the caller")
	}
}

func TestARequestIdReachesTheErrorString(t *testing.T) {
	// If it lives only on a field, nobody pastes it into a support ticket.
	err := FromResponse(404, []byte(envelopeBody("NOT_FOUND", "no such session")), nil)
	if !strings.Contains(err.Error(), "req_0123456789abcdef0123456789abcdef") {
		t.Fatalf("request id missing from %q", err.Error())
	}
	if !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Fatalf("code missing from %q", err.Error())
	}
}

func TestTheHeaderSuppliesTheRequestIdWhenTheBodyCannot(t *testing.T) {
	// An error raised before the handler ran has no envelope but still carries
	// the correlation id.
	headers := http.Header{}
	headers.Set("X-Request-Id", "req_fromheader")
	err := FromResponse(502, []byte("upstream exploded"), headers)
	if err.RequestID != "req_fromheader" {
		t.Fatalf("expected the header id, got %q", err.RequestID)
	}
	if !err.IsServer() {
		t.Fatal("5xx without an envelope should still be a server error")
	}
}

func TestQuotaIsNeverRetried(t *testing.T) {
	// Waiting 400ms does not clear a daily allowance; retrying burns the
	// caller's remaining budget on requests that cannot succeed.
	p := DefaultRetryPolicy()
	if p.ShouldRetry(1, 429, "QUOTA_EXCEEDED") {
		t.Fatal("QUOTA_EXCEEDED must not be retried")
	}
	if !p.ShouldRetry(1, 429, "RATE_LIMITED") {
		t.Fatal("RATE_LIMITED is exactly what retrying is for")
	}
}

func TestFiveHundredIsNotRetried(t *testing.T) {
	// The API returns 500 for an unexpected fault, not a transient one.
	// Hammering it turns one incident into a bigger one.
	p := DefaultRetryPolicy()
	if p.ShouldRetry(1, 500, "INTERNAL_ERROR") {
		t.Fatal("500 must not be retried")
	}
	for _, status := range []int{502, 503, 504} {
		if !p.ShouldRetry(1, status, "") {
			t.Fatalf("%d should be retried", status)
		}
	}
}

func TestRetryAfterBeatsTheGuess(t *testing.T) {
	p := DefaultRetryPolicy()
	if got := p.Backoff(3, 2*time.Second); got != 2*time.Second {
		t.Fatalf("the server's Retry-After should win, got %v", got)
	}
	// ...but never past the ceiling, or a hostile header parks the client.
	if got := p.Backoff(1, time.Hour); got != p.MaxBackoff {
		t.Fatalf("Retry-After must be capped at MaxBackoff, got %v", got)
	}
}

func TestBackoffIsJitteredWithinBounds(t *testing.T) {
	// Without jitter every client refused at the same instant retries at the
	// same instant and re-creates the overload.
	p := DefaultRetryPolicy()
	p.rand = func() float64 { return 1.0 } // maximum positive jitter
	high := p.Backoff(2, 0)
	p.rand = func() float64 { return 0.0 } // maximum negative jitter
	low := p.Backoff(2, 0)
	if !(low < high) {
		t.Fatalf("jitter produced no spread: low=%v high=%v", low, high)
	}
	if high > p.MaxBackoff {
		t.Fatalf("jitter pushed the delay past the ceiling: %v", high)
	}
}

// --- transport ------------------------------------------------------------

type stubRoundTripper struct {
	responses []*http.Response
	requests  []*http.Request
}

func (s *stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	s.requests = append(s.requests, req.Clone(req.Context()))
	resp := s.responses[0]
	if len(s.responses) > 1 {
		s.responses = s.responses[1:]
	}
	return resp, nil
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func TestIdempotencyKeyIsStableAcrossRetries(t *testing.T) {
	// The assertion that matters. A key generated inside the retry loop would
	// make each attempt a fresh write -- the exact failure idempotency keys
	// exist to prevent.
	stub := &stubRoundTripper{responses: []*http.Response{
		response(503, "unavailable"),
		response(201, `{"id":"sess_1"}`),
	}}
	tr := &Transport{Base: stub, Credential: "go3_live_x", sleep: func(time.Duration) {}}

	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/sessions", strings.NewReader(`{"a":1}`))
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if len(stub.requests) != 2 {
		t.Fatalf("expected one retry, got %d attempts", len(stub.requests))
	}
	first := stub.requests[0].Header.Get("Idempotency-Key")
	second := stub.requests[1].Header.Get("Idempotency-Key")
	if first == "" || first != second {
		t.Fatalf("idempotency key changed across a retry: %q then %q", first, second)
	}
}

func TestTheBodyIsReplayedOnRetry(t *testing.T) {
	// A buffered body is not optional: a request body is a one-shot reader, so
	// without buffering the retry sends an empty payload and the server sees a
	// different request.
	stub := &stubRoundTripper{responses: []*http.Response{
		response(503, "unavailable"),
		response(200, `{}`),
	}}
	tr := &Transport{Base: stub, sleep: func(time.Duration) {}}
	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/analyze/text", strings.NewReader(`{"text":"hi"}`))
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	for i, r := range stub.requests {
		payload, _ := io.ReadAll(r.Body)
		if string(payload) != `{"text":"hi"}` {
			t.Fatalf("attempt %d sent %q", i+1, payload)
		}
	}
}

func TestACallerSuppliedIdempotencyKeyIsNotOverwritten(t *testing.T) {
	stub := &stubRoundTripper{responses: []*http.Response{response(201, `{}`)}}
	tr := &Transport{Base: stub, sleep: func(time.Duration) {}}
	req, _ := http.NewRequest(http.MethodPost, "http://x/v1/sessions", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "mine")
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if got := stub.requests[0].Header.Get("Idempotency-Key"); got != "mine" {
		t.Fatalf("the caller's key was replaced with %q", got)
	}
}

func TestSafeMethodsGetNoIdempotencyKey(t *testing.T) {
	stub := &stubRoundTripper{responses: []*http.Response{response(200, `{}`)}}
	tr := &Transport{Base: stub, sleep: func(time.Duration) {}}
	req, _ := http.NewRequest(http.MethodGet, "http://x/v1/whoami", nil)
	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if stub.requests[0].Header.Get("Idempotency-Key") != "" {
		t.Fatal("a GET does not need replay protection and should not claim it")
	}
}

func TestTheResponseBodyIsStillReadableAfterClassification(t *testing.T) {
	// The transport reads the body to classify the failure. If it did not put
	// it back, the generated client would see an empty payload -- which is
	// exactly the bug the Python SDK's dogfood tests found.
	stub := &stubRoundTripper{responses: []*http.Response{
		response(404, envelopeBody("NOT_FOUND", "no such session")),
	}}
	tr := &Transport{Base: stub, sleep: func(time.Duration) {}}
	req, _ := http.NewRequest(http.MethodGet, "http://x/v1/sessions/nope", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(payload), "NOT_FOUND") {
		t.Fatalf("body was consumed by classification: %q", payload)
	}
}

func TestClassifyProducesAnErrorsAsCompatibleValue(t *testing.T) {
	resp := response(403, envelopeBody("INSUFFICIENT_SCOPE", "nope"))
	var err error = Classify(resp)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatal("Classify must produce something errors.As can unwrap")
	}
	if !apiErr.IsPermission() {
		t.Fatalf("INSUFFICIENT_SCOPE should be a permission failure, got %s", apiErr.Kind)
	}
}

// --- pagination -----------------------------------------------------------

func TestIterateWalksEveryPageAndStopsWithoutACursor(t *testing.T) {
	pages := map[string]Page[string]{
		"":   {Items: []string{"a", "b"}, NextCursor: "p2"},
		"p2": {Items: []string{"c"}, NextCursor: ""},
	}
	var seen []string
	err := Iterate(func(cursor string) (Page[string], error) { return pages[cursor], nil },
		func(item string) bool { seen = append(seen, item); return true })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, "") != "abc" {
		t.Fatalf("got %v", seen)
	}
}

func TestPaginationIsLazy(t *testing.T) {
	// Taking the first item must cost one request, not the whole history.
	calls := 0
	_ = Iterate(func(cursor string) (Page[int], error) {
		calls++
		return Page[int]{Items: []int{1, 2}, NextCursor: "more"}, nil
	}, func(int) bool { return false })
	if calls != 1 {
		t.Fatalf("expected one fetch, got %d", calls)
	}
}

func TestAPageClaimingMoreWithNoCursorTerminates(t *testing.T) {
	// Looping on a "has more" flag alone spins forever. The cursor is the
	// only thing that can be trusted to advance.
	calls := 0
	err := Iterate(func(cursor string) (Page[int], error) {
		calls++
		if calls > 5 {
			t.Fatal("iterator did not terminate")
		}
		return Page[int]{Items: []int{calls}, NextCursor: ""}, nil
	}, func(int) bool { return true })
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestCollectRespectsItsLimit(t *testing.T) {
	got, err := Collect(func(cursor string) (Page[int], error) {
		return Page[int]{Items: []int{1, 2, 3}, NextCursor: "next"}, nil
	}, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v err=%v", got, err)
	}
}
