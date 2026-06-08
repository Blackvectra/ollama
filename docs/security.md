# Securing your Ollama server

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
