# Securing your Ollama server

> **On "maximum"/"government-grade" security:** real high-assurance security
> (e.g. the FBI's CJIS Security Policy, NIST 800-53) is mostly about the
> *deployment environment*, not application code — network isolation, hardened
> OS, TLS/mTLS, audited access, HSM-backed key management, continuous
> monitoring, and physical security. The controls below harden everything that
> lives in this software; the **Hardened deployment checklist** at the end
> covers the environment work that no application change can do for you.


By default Ollama is **private** — it runs on your machine and only listens on
`localhost` (`127.0.0.1:11434`), so nothing leaves your computer and nothing on
your network can reach it. This page covers how to keep it that way, and how to
lock it down further if you ever expose it.

## 1. Keep it on localhost (the default)

The single most important rule: **do not bind the server to a public address
unless you have put authentication in front of it.**

- Default (safe): `OLLAMA_HOST=127.0.0.1:11434`
- Exposes to your whole network: `OLLAMA_HOST=0.0.0.0:11434` ← only do this with an access key set (below)

## 2. Require an access key (`OLLAMA_API_KEY`)

Ollama has no login by default — anyone who can reach the port can use your
models. Set `OLLAMA_API_KEY` to require a Bearer token on every API request:

```sh
export OLLAMA_API_KEY="choose-a-long-random-string"
ollama serve
```

With the key set, API calls must include it:

```sh
curl http://localhost:11434/v1/models \
  -H "Authorization: Bearer choose-a-long-random-string"
```

Notes:

- When `OLLAMA_API_KEY` is empty (the default), the server stays open —
  existing behavior is unchanged.
- The key value is never printed by `ollama serve`'s environment listing.
- A few paths stay open so things still work: `/` and `/api/version` (health
  checks) and `/chat` (the static web UI page — a browser navigation cannot
  send an `Authorization` header). The chat page itself prompts you for the key
  and stores it in your browser, then sends it on its API calls. The actual
  inference and model APIs (`/api/*`, `/v1/*`) all require the key.

## 3. Lock down browser origins (`OLLAMA_ORIGINS`)

The built-in chat UI and any browser client are subject to CORS. Restrict which
web origins may call the API:

```sh
export OLLAMA_ORIGINS="http://localhost:11434"
```

Avoid `OLLAMA_ORIGINS=*` on an exposed server.

## 4. If you must expose it to the internet

Put a reverse proxy (nginx, Caddy, Traefik) in front and let it handle:

- **TLS** (HTTPS) so traffic is encrypted in transit.
- **Authentication** (the `OLLAMA_API_KEY` above, or proxy-level auth).
- **Rate limiting** to blunt abuse.

Never expose the raw port directly.

## Privacy vs. cloud models

Everything above keeps your data on your machine. Note that *cloud* models
(remote inference) and web search send data off your machine by design — set
`OLLAMA_NO_CLOUD=1` if you want to disable those and stay fully local.

### Optional Claude (Anthropic) passthrough

Setting `ANTHROPIC_API_KEY` makes Claude models (e.g. `claude-opus-4-8`) appear
in the model list alongside your local models, so you can use a frontier model
on demand for hard problems while keeping local models as the private default.

```sh
export ANTHROPIC_API_KEY="sk-ant-..."
ollama serve
```

Privacy tradeoff, stated plainly:

- **Local models stay fully private** — those requests never leave your machine.
- **Only messages you send to a Claude model** go to Anthropic's servers, exactly
  like using the Claude app or API directly. This is unavoidable: frontier
  models like Opus only run on Anthropic's servers.
- The key stays **server-side** — it is read from the environment, never sent to
  the browser, and never printed in the `ollama serve` environment listing.

Leave `ANTHROPIC_API_KEY` unset to keep the server local-only.

> ⚠️ **The passthrough spends real money.** If `ANTHROPIC_API_KEY` is set but
> `OLLAMA_API_KEY` is not, the Claude proxy is unauthenticated — anyone who can
> reach the server can run up your Anthropic bill. The localhost default
> protects you; if you ever bind to a non-loopback address, **set
> `OLLAMA_API_KEY` first.** The server logs a warning at startup when this
> combination is detected.

### Notes on the built-in chat UI

- The UI stores your access key in the browser's `localStorage` (so you don't
  retype it). It is scoped to this origin and never embedded in the served HTML.
  Treat the machine's browser profile accordingly.
- Model output is HTML-escaped before rendering, and the page is served under a
  strict **Content-Security-Policy** with a per-request script **nonce** (no
  `'unsafe-inline'` for scripts) and `connect-src 'self'`, so a model cannot
  inject scripts or exfiltrate data to another origin.
- The chat proxy caps request bodies (1 MiB) and bounds the size of upstream
  error messages it reflects back.

## Built-in hardening controls

These are on by default or one env var away:

| Control | Behavior |
|---|---|
| **Loopback by default** | Binds `127.0.0.1` unless you change `OLLAMA_HOST`. |
| **Fail-closed binding** | Refuses to start when bound to a non-loopback address without `OLLAMA_API_KEY`. Override only with `OLLAMA_ALLOW_INSECURE=1`. |
| **Bearer auth** | `OLLAMA_API_KEY` requires `Authorization: Bearer <key>` on all API routes (constant-time compare). |
| **Auth audit log** | Every rejected request logs client IP + method + path (never the attempted key). |
| **Rate limiting** | `OLLAMA_RATE_LIMIT=<req/min>` throttles per client IP (brute-force protection). `0` disables (default). |
| **Security headers** | `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy`, and a strict CSP. |
| **Secrets redaction** | `OLLAMA_API_KEY` / `ANTHROPIC_API_KEY` values never print in the env listing or logs. |

Example "locked down" launch:

```sh
export OLLAMA_HOST=127.0.0.1:11434
export OLLAMA_API_KEY="$(openssl rand -hex 32)"
export OLLAMA_ORIGINS="http://localhost:11434"
export OLLAMA_RATE_LIMIT=120
ollama serve
```

## Hardened deployment checklist

Application controls are necessary but not sufficient. For a high-assurance
deployment, also do the following — none of it can be done by app code:

- [ ] **Network isolation** — run on an isolated/segmented network or VPN; never
      expose the raw port to the internet.
- [ ] **TLS/mTLS** — terminate HTTPS at a reverse proxy (nginx/Caddy/Traefik);
      require client certificates for mutual auth where feasible.
- [ ] **Strong, rotated key** — generate `OLLAMA_API_KEY` from a CSPRNG, store it
      in a secrets manager/HSM, and rotate on a schedule and on any exposure.
- [ ] **Least privilege** — run the server as a non-root user with a read-only
      root filesystem and dropped capabilities; restrict the models directory.
- [ ] **OS hardening** — patched host, host firewall default-deny, disk
      encryption at rest, minimal installed packages.
- [ ] **Monitoring & audit** — ship the auth/rate-limit logs to a SIEM, alert on
      repeated `rejected unauthenticated API request` and `rate limit exceeded`.
- [ ] **Data policy** — set `OLLAMA_NO_CLOUD=1` and leave `ANTHROPIC_API_KEY`
      unset if data must never leave the environment.
- [ ] **Physical/access control** — restrict who can reach the host and its
      browser profiles (where the chat UI caches the access key).

> Following this checklist does **not** constitute CJIS/FedRAMP/etc.
> certification — those require formal assessment and organizational controls
> well beyond any single tool.
