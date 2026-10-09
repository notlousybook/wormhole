# Wormhole

A free, self-hosted gateway for language models — one OpenAI-compatible API
in front of every provider you configure. Like OpenRouter, but yours:
config-driven, with a built-in dashboard, usage tracking and API key management.

Model ids use the format `provider/modelid`, e.g. `groq/llama-3.3-70b`.

```
go build -o wormhole.exe .
./wormhole.exe
```

Open **http://localhost:8080** — works immediately with keyless models
(Pollinations, LLM7, Kilo, OVH): no signup, no API key, no internet config.

## How it works

- **config.yaml** — declare providers and models. Well-known providers
  (groq, openai, ollama, openrouter, deepseek, ...) only need a name;
  any other OpenAI-compatible host just needs a `base_url`.
- **.env** — holds real API keys, referenced from config by variable name.
  Keys never appear in config or code. `.env` is gitignored.
- **Dashboard** — vanilla HTML/CSS/JS served by the binary:
  - `/` model catalog with search, filters and live status
  - `/model/...` per-model pages with pricing, latency and curl snippets
  - `/playground` chat UI with streaming, temperature and system prompt
  - `/keys` create/revoke API keys (hashed at rest, monthly spend caps)
  - `/usage` token, latency and spend analytics with charts
  - `/docs` quick start and full config reference
- **`/v1/*` API** — OpenAI-compatible. Point any OpenAI SDK at
  `http://localhost:8080/v1` and use your own model names.

## Quick start

```bash
copy config.free.yaml config.yaml   # free setup: keyless + keyed free models
go build -o wormhole.exe .
./wormhole.exe
```

Optional: `copy .env.example .env` and add free provider keys (Groq, Cerebras,
Gemini, Mistral, OpenRouter, NVIDIA) to unlock the keyed models too.

## wh — CLI helper

A tiny client for any Wormhole server (local or shared):

```bash
go build -o wh.exe ./cmd/wh
./wh models                                # list models
./wh chat "Hello"                          # streams, keyless, no key needed
./wh chat "Hi" -model kilo/nemotron-3-nano # pick a model (flags go anywhere)
./wh chat "Hi" -key sk-wormhole-...             # use your own key
./wh keys create my-app                    # create a key (local server)
./wh usage                                 # 7-day totals
```

Point at a shared server with `WH_SERVER` (or `-server`):

```bash
set WH_SERVER=https://your-server.example.com
./wh chat "Hello from afar"
```

## Keyless shared pool

Requests without an API key are allowed through a per-IP rate-limited
anonymous pool (30 req/min/IP; localhost is unlimited). This exists because
keyless providers rate-limit per IP — a shared server pools those limits.

Call a model:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-wormhole-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"demo","messages":[{"role":"user","content":"Hi"}]}'
```

Keys are created in the dashboard (`/keys`). Local requests (the dashboard,
curl on the same machine) work without a key; external apps need one.

## Security model

- Provider keys live in `.env` only; the dashboard never displays them.
- User API keys are stored as SHA-256 hashes in `data/keys.json`.
- Management API (`/api/*`) is restricted to localhost.
- Per-key monthly spend limits are enforced before forwarding.

## Dev

`dev/mock` is a tiny OpenAI-compatible upstream for testing without real keys:

```bash
go run ./dev/mock              # listens on :18080
```

Then add to config.yaml:

```yaml
- name: mock
  base_url: http://127.0.0.1:18080/v1
```
