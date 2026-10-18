// Package client is the hand-written half of the GuardianO3 Go SDK.
//
// The generated half (../generated) is models and operations, rebuilt from
// openapi/v1.json by scripts/generate.sh and never edited. This package is
// what the generator cannot produce: errors you can switch on, retries that are
// safe, idempotency keys that survive a retry, and pagination that hides the
// cursor.
//
// It mirrors the Python and TypeScript cores deliberately -- the same code
// mapping, the same retry rules, the same non-retryable set. Three SDKs that
// behave differently under load produce three different incidents.
package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// ErrorDetail is one field-level validation failure from `error.details`.
type ErrorDetail struct {
	Field string `json:"field"`
	Issue string `json:"issue"`
}

// APIError is every failure the API reports through its error envelope.
//
// Go has no exception hierarchy, so the taxonomy the other SDKs express with
// classes is expressed here with predicate methods and errors.As: a caller
// writes `var apiErr *client.APIError; if errors.As(err, &apiErr) &&
// apiErr.IsNotFound()`. Kind is exported for a switch.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	Details    []ErrorDetail
	RetryAfter int
	Kind       Kind
}

// Kind groups codes the way a caller reacts to them, not the way HTTP does.
type Kind string

const (
	KindAuthentication Kind = "authentication"
	KindPermission     Kind = "permission"
	KindNotFound       Kind = "not_found"
	KindConflict       Kind = "conflict"
	KindValidation     Kind = "validation"
	KindRateLimit      Kind = "rate_limit"
	KindQuota          Kind = "quota"
	KindServer         Kind = "server"
	KindUnknown        Kind = "unknown"
)

// Error puts the request id in the string form on purpose. It is the value
// support asks for, and if it lives only on a field nobody pastes it into the
// report.
func (e *APIError) Error() string {
	out := e.Message
	if e.Code != "" {
		out = fmt.Sprintf("%s [%s]", out, e.Code)
	}
	if e.RequestID != "" {
		out = fmt.Sprintf("%s (request %s)", out, e.RequestID)
	}
	return out
}

func (e *APIError) IsAuthentication() bool { return e.Kind == KindAuthentication }
func (e *APIError) IsPermission() bool     { return e.Kind == KindPermission }
func (e *APIError) IsNotFound() bool       { return e.Kind == KindNotFound }
func (e *APIError) IsConflict() bool       { return e.Kind == KindConflict }
func (e *APIError) IsValidation() bool     { return e.Kind == KindValidation }
func (e *APIError) IsServer() bool         { return e.Kind == KindServer }

// IsRateLimit is true for a quota failure too: a quota IS a rate limit, just
// one that waiting will not clear. Callers who must tell them apart use
// IsQuota, and the retry policy already does.
func (e *APIError) IsRateLimit() bool { return e.Kind == KindRateLimit || e.Kind == KindQuota }
func (e *APIError) IsQuota() bool     { return e.Kind == KindQuota }

// kindByCode is the same table as every other GuardianO3 SDK. Divergence here
// would mean the same failure reads differently depending on which SDK a team
// happened to pick.
var kindByCode = map[string]Kind{
	"UNAUTHENTICATED":           KindAuthentication,
	"INVALID_CREDENTIALS":       KindAuthentication,
	"INVALID_TOKEN":             KindAuthentication,
	"TOKEN_REVOKED":             KindAuthentication,
	"INVALID_REFRESH_TOKEN":     KindAuthentication,
	"REFRESH_TOKEN_REUSED":      KindAuthentication,
	"INVALID_API_KEY":           KindAuthentication,
	"REAUTHENTICATION_REQUIRED": KindAuthentication,
	"MFA_REQUIRED":              KindAuthentication,
	"FORBIDDEN":                 KindPermission,
	"INSUFFICIENT_SCOPE":        KindPermission,
	"EMAIL_NOT_VERIFIED":        KindPermission,
	"ACCOUNT_SUSPENDED":         KindPermission,
	"WRONG_ENVIRONMENT":         KindPermission,
	"NOT_FOUND":                 KindNotFound,
	"ALREADY_EXISTS":            KindConflict,
	"CONFLICT":                  KindConflict,
	"IDEMPOTENCY_KEY_REUSED":    KindConflict,
	"VALIDATION_ERROR":          KindValidation,
	"INVALID_REQUEST":           KindValidation,
	"PASSWORD_POLICY_VIOLATION": KindValidation,
	"BREACHED_PASSWORD":         KindValidation,
	"UNSUPPORTED_MEDIA_TYPE":    KindValidation,
	"PAYLOAD_TOO_LARGE":         KindValidation,
	"RATE_LIMITED":              KindRateLimit,
	"CONCURRENCY_LIMIT":         KindRateLimit,
	"QUOTA_EXCEEDED":            KindQuota,
	"INTERNAL_ERROR":            KindServer,
	"SERVICE_UNAVAILABLE":       KindServer,
	"UPSTREAM_ERROR":            KindServer,
	"UPSTREAM_TIMEOUT":          KindServer,
	"NOT_CONFIGURED":            KindServer,
}

var kindByStatus = map[int]Kind{
	http.StatusBadRequest:            KindValidation,
	http.StatusUnauthorized:          KindAuthentication,
	http.StatusForbidden:             KindPermission,
	http.StatusNotFound:              KindNotFound,
	http.StatusConflict:              KindConflict,
	http.StatusRequestEntityTooLarge: KindValidation,
	http.StatusUnsupportedMediaType:  KindValidation,
	422:                              KindValidation,
	http.StatusTooManyRequests:       KindRateLimit,
}

type envelope struct {
	Error struct {
		Code      string        `json:"code"`
		Message   string        `json:"message"`
		RequestID string        `json:"requestId"`
		Details   []ErrorDetail `json:"details"`
	} `json:"error"`
}

// FromResponse turns an error response into an *APIError.
//
// An unrecognised code falls back to the kind for its HTTP status rather than
// producing something unhandleable. New codes are added without a version bump,
// so a client that broke on one would make adding a code a breaking change.
func FromResponse(status int, body []byte, headers http.Header) *APIError {
	err := &APIError{StatusCode: status, Kind: KindUnknown}

	var env envelope
	if jsonErr := json.Unmarshal(body, &env); jsonErr == nil && env.Error.Code != "" {
		err.Code = env.Error.Code
		err.Message = env.Error.Message
		err.RequestID = env.Error.RequestID
		err.Details = env.Error.Details
	}
	if err.Message == "" {
		err.Message = fmt.Sprintf("request failed with status %d", status)
	}
	// The header is the fallback: an error raised before the handler ran has no
	// envelope but still carries the correlation id.
	if err.RequestID == "" && headers != nil {
		err.RequestID = headers.Get("X-Request-Id")
	}
	if headers != nil {
		if after, convErr := strconv.Atoi(headers.Get("Retry-After")); convErr == nil {
			err.RetryAfter = after
		}
	}

	if kind, ok := kindByCode[err.Code]; ok {
		err.Kind = kind
	} else if kind, ok := kindByStatus[status]; ok {
		err.Kind = kind
	} else if status >= 500 {
		err.Kind = KindServer
	}
	return err
}
