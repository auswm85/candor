# Security Policy

candor is a local reverse proxy that sits between your coding harness and an LLM
provider. Because it handles live API traffic, its security posture matters — so
here is exactly what it does and does not do, and how to report a problem.

## Trust model

- **Your API key is forwarded untouched.** candor reads the `Authorization` (and
  provider-specific auth) headers your harness already sends and passes them
  straight to the upstream provider. It never stores them, never writes them to
  disk, and never sends them anywhere except the provider you configured.
- **No cloud, no telemetry, no account.** Everything runs on your machine. The
  only outbound network calls are (1) the proxied requests to your provider and
  (2) a periodic fetch of OpenRouter's public model-pricing catalog (no auth, no
  personal data). Nothing about your usage leaves the machine.
- **Loopback-only by default.** The proxy binds `127.0.0.1` and refuses to bind a
  non-loopback address unless you explicitly set `proxy.allow_nonloopback: true`.
  Exposing a key-forwarding proxy to your network is opt-in and your
  responsibility.
- **The local database holds usage data, not secrets** — token counts, costs, and
  timestamps. It is created with `0600` permissions.
- **Fail-open.** Usage tapping runs only after your request/response bytes have
  been forwarded, and is panic-isolated, so a bug in candor cannot corrupt or
  block your actual LLM call.

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

- Preferred: open a private report via GitHub Security Advisories
  ("Report a vulnerability" on the repository's **Security** tab).

You'll get an acknowledgement as soon as practical. Since this is a
single-maintainer project there is no formal SLA, but security reports are
prioritized over everything else.

## Scope

In scope: anything that could leak an API key, expose the proxy or database
beyond the local machine, break the fail-open guarantee, or cause candor to send
data anywhere other than the configured provider and the pricing catalog.

Out of scope: vulnerabilities in the upstream providers themselves, in your
coding harness, or issues that require an attacker who already has local
filesystem/root access to the machine running candor.
