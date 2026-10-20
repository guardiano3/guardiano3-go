# Changelog

All notable changes to this SDK. Versions follow [semver](https://semver.org/);
while the version is 0.x, a minor release may contain breaking changes.

## 0.1.0 (2026-10-20)

- First release, as one module, `github.com/guardiano3/guardiano3-go`:
  generated models and operations for the GuardianO3 `/v1` API (`generated`),
  a hand-written `http.RoundTripper` for authentication, idempotency keys and
  retries with jitter, typed errors and pagination (`client`), and live
  sessions (`live`).
- Requires Go 1.23+. Defaults to the production API, `https://api.guardiano3.com`.
