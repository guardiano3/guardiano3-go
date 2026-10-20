# GuardianO3 Go SDK

The official Go client for the [GuardianO3](https://guardiano3.com) API:
scam-risk decisions for live customer sessions. Send text, an audio clip or
a live audio stream, and get a risk decision back while the session is still
live.

You need an API key from the developer console at
[guardiano3.com](https://guardiano3.com).

## Install

```bash
go get github.com/guardiano3/guardiano3-go@v0.1.0
```

Go 1.23+. One module, three packages:

| Package | What it is |
|---|---|
| `github.com/guardiano3/guardiano3-go/client` | **Hand-written**: the transport (auth, idempotency, retries), typed errors, pagination |
| `github.com/guardiano3/guardiano3-go/live` | **Hand-written**: live-session WebSocket channels |
| `github.com/guardiano3/guardiano3-go/generated` | **Generated** (package `guardiano3`) from `openapi/v1.json` by `scripts/generate.sh`. Never edited by hand |

**`client` imports nothing but the standard library** — not even `generated`.
Its `Transport` is an `http.RoundTripper` and its `Iterate` is generic over any
page shape, so the two halves compose through `net/http`, and the core works
against a hand-rolled client too.

Pull requests are welcome for the hand-written packages; see
[CONTRIBUTING](https://github.com/guardiano3/.github/blob/main/CONTRIBUTING.md).
If generated code is wrong, open an issue: the fix belongs in the contract.

## A client

<!-- sample:go-client -->
```go
package main

import (
	"net/http"

	"github.com/guardiano3/guardiano3-go/client"
	guardiano3 "github.com/guardiano3/guardiano3-go/generated"
)

func main() {
	// The transport is what turns the generated client into an SDK: it
	// authenticates, adds an idempotency key to unsafe requests, and retries
	// what is safe to retry. One RoundTripper rather than 36 wrapped calls.
	httpClient := &http.Client{Transport: client.NewTransport("go3_live_...")}

	// https://api.guardiano3.com by default; set cfg.Servers to reach another
	// environment.
	cfg := guardiano3.NewConfiguration()
	cfg.HTTPClient = httpClient

	_ = guardiano3.NewAPIClient(cfg)
}
```

An API key and a user access token are both sent as `Authorization: Bearer`;
the API tells them apart by the `go3_` prefix. A header the caller sets is
never overwritten.

## Errors

Go has no exception hierarchy, so the taxonomy the other SDKs express with
classes is expressed here with `errors.As` and predicate methods.

<!-- sample:go-errors -->
```go
package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/guardiano3/guardiano3-go/client"
)

func handle(resp *http.Response) error {
	if apiErr := client.Classify(resp); apiErr != nil {
		switch {
		case apiErr.IsQuota():
			return apiErr // do not retry: waiting will not help
		case apiErr.IsRateLimit():
			fmt.Println("retry after", apiErr.RetryAfter)
		case apiErr.IsNotFound():
			// Also returned when the row exists but belongs to someone else.
			fmt.Println(apiErr.Code, apiErr.RequestID)
		case apiErr.IsValidation():
			for _, detail := range apiErr.Details {
				fmt.Println(detail.Field, detail.Issue)
			}
		}
		var target *client.APIError
		_ = errors.As(error(apiErr), &target)
	}
	return nil
}
```

| Predicate | Kind | Status |
|---|---|---|
| `IsAuthentication()` | `KindAuthentication` | 401 |
| `IsPermission()` | `KindPermission` | 403 |
| `IsNotFound()` | `KindNotFound` | 404 |
| `IsConflict()` | `KindConflict` | 409 |
| `IsValidation()` | `KindValidation` | 400/413/415/422 |
| `IsRateLimit()` | `KindRateLimit` **or** `KindQuota` | 429 |
| `IsQuota()` | `KindQuota` | 429 with `QUOTA_EXCEEDED` |
| `IsServer()` | `KindServer` | 5xx |

`IsRateLimit()` is true for a quota too — a quota *is* a rate limit, just one
that waiting will not clear. Use `IsQuota()` when you must tell them apart; the
retry policy already does.

**An unrecognised code falls back to the kind for its HTTP status**, and the
code still reaches you on `.Code`. New codes are added without a version bump,
so failing on one would make adding a code a breaking change.

`RequestID` is in `Error()` as well as on the struct — a field nobody prints is
a field nobody pastes into a support ticket.

## Retries

| | |
|---|---|
| Retried | 429, 502, 503, 504 |
| **Not** retried | 4xx other than 429, **500**, and any `QUOTA_EXCEEDED` |
| Attempts | 3 |
| Backoff | 0.5s, doubling, capped at 20s, ±25% jitter |
| `Retry-After` | Honoured, and capped |

<!-- sample:go-retry-policy -->
```go
package main

import (
	"time"

	"github.com/guardiano3/guardiano3-go/client"
)

func transport() *client.Transport {
	policy := client.DefaultRetryPolicy()
	policy.MaxAttempts = 5
	policy.MaxBackoff = 30 * time.Second

	return &client.Transport{Credential: "go3_live_...", RetryPolicy: policy}
}
```

`MaxAttempts: 1` disables retrying.

## Idempotency

Every unsafe request carries an `Idempotency-Key`, generated **once per logical
call, outside the retry loop** — inside it, each retry would be a fresh write.
The request body is buffered so a retry replays it byte for byte; without that
the second attempt would send an empty payload and the server would see a
different request. Set the header yourself to control it.

## Pagination

<!-- sample:go-pagination -->
```go
package main

import (
	"fmt"

	"github.com/guardiano3/guardiano3-go/client"
)

type entry struct{ ID string }

func walk(fetch func(cursor string) (client.Page[entry], error)) error {
	return client.Iterate(fetch, func(item entry) bool {
		fmt.Println(item.ID)
		return true // false stops early
	})
}
```

Lazy: a page is fetched only when the previous one has been consumed. Returning
`false` from the callback stops without another request. The iterator stops on
a missing cursor rather than a `hasMore` flag — a page can claim more while
giving no cursor to reach it, and looping on that spins forever.

`client.Collect(fetch, max)` when you want a bounded slice instead.

## WebSocket / Live Sessions

The `live` package manages the two WebSocket channels for real-time session
analysis. It is the only package that uses a third-party library,
`github.com/coder/websocket`; `client` stays standard-library only.

<!-- sample:go-live -->
```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/guardiano3/guardiano3-go/live"
)

func main() {
	ctx := context.Background()

	// 1. Create a session via REST (using the generated API)
	// sessionID := ...

	// 2. Open the live channels
	session := live.NewSession(
		"https://api.guardiano3.com",
		"go3_live_...",
		sessionID,
		live.Callbacks{
			OnReady: func(f live.Frame) {
				fmt.Println("Ready:", f.SessionID)
			},
			OnDecision: func(f live.Frame) {
				fmt.Printf("%s %.1f %s\n", f.RiskLevel, *f.RiskScore, f.Transcript)
			},
			OnDecisionUpdate: func(f live.Frame) {
				fmt.Println("Updated:", f.RiskLevel, f.VerificationStatus)
			},
			OnLagging: func(f live.Frame) {
				log.Printf("Dropped chunks: %d", f.DroppedChunks)
			},
			OnError: func(f live.Frame) {
				log.Printf("Error: %s %s", f.ErrorCode, f.ErrorMessage)
			},
			OnDisconnect: func(code int, reason string) {
				fmt.Println("Disconnected:", code, reason)
			},
		},
	)

	if err := session.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	// 3. Send PCM audio (16-bit LE, 16kHz mono)
	if err := session.SendAudio(ctx, pcmBytes); err != nil {
		log.Fatal(err)
	}
}
```

| Callback | Frame type | When |
|---|---|---|
| `OnReady` | `ready` | Session authenticated, audio accepted |
| `OnDecision` | `decision` | Scored chunk; `pending` verification means an update follows |
| `OnDecisionUpdate` | `decisionUpdate` | Final decision for a chunk first sent as `pending`; replace it in place |
| `OnLagging` | `lagging` | Audio arriving faster than transcription |
| `OnDegraded` | `degraded` | Part of the audio was not analysed; no decision covers it. Not fatal: do not read a quiet stream as a safe session (`DegradedReason`, `UnheardSeconds`, `TotalUnheardSeconds`) |
| `OnError` | `error` | Fatal; server closes after sending |
| `OnDisconnect` | — | Either WebSocket closed or failed |

The `Frame` struct carries parsed fields (`RiskLevel`, `RiskScore`, etc.)
and the full `Raw` map for any field not broken out. The `client` package
stays dependency-free; if you prefer to manage WebSocket connections
yourself, the frame schemas are generated as Go structs (`ReadyFrame`,
`DecisionFrame`, etc.) in the `generated` package.

## What it will never do

**No business logic, no authorization decisions.** Transport, serialisation,
authentication, models, errors, pagination and retries — nothing else. Anything
the SDK decided, an attacker could decide differently with `curl`.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

The licence covers this SDK's source code. Using the GuardianO3 API requires an
API key and acceptance of the [Terms of Service](https://guardiano3.com/terms).

Security issues: see [SECURITY](https://github.com/guardiano3/.github/blob/main/SECURITY.md).
Never put API keys, session audio or transcripts in an issue.
