# Deployment Guide

## Local Docker Build (requires Docker installed)

```bash
cd /c/Users/LACI/scientific-consensus-web

# Build the image (multi-stage: builds web server on Linux + uses vendored CLI)
docker build -t scientific-consensus-web:latest .

# Run locally
docker run -p 8090:8090 scientific-consensus-web:latest

# Test
curl http://127.0.0.1:8090/
curl http://127.0.0.1:8090/api/consensus -X POST \
  -H "Content-Type: application/json" \
  -d '{"claim":"vitamin D reduces infections","limit":10}'
```

> The runtime image copies the pre-built `bin/scientific-consensus-pp-cli-linux`.
> Regenerate it with `./vendor-cli.sh` whenever the CLI source changes.

## Render Deployment

### Prerequisites
- GitHub repo pushed: https://github.com/laci141/scientific-consensus-web
- Render account: https://render.com (free tier OK)

### Step 1: Create Render Web Service

1. Sign in to https://render.com
2. Click "New +" → "Web Service"
3. Connect GitHub → Select `scientific-consensus-web`
4. Name: `scientific-consensus-web` (or custom)
5. Environment: Docker
6. Build command: `docker build -t app .` (leave blank for auto-detect)
7. Start command: (leave blank — Dockerfile CMD runs)
8. Port: `8090`
9. Plan: Free (512 MB RAM, spins down after 15 min) or Starter ($7/mo, always on)
10. No environment variables needed (BYOK via headers)
11. Click "Create Web Service"

### Step 2: Wait for Render Deploy

Render clones, builds (`docker build`), and deploys. Watch the "Deploy" tab for logs.

URL format: `https://scientific-consensus-web-<random>.onrender.com`

### Step 3: Verify on Render

```bash
RENDER_URL="https://scientific-consensus-web-<your-random>.onrender.com"

curl $RENDER_URL/
curl -X POST $RENDER_URL/api/consensus \
  -H "Content-Type: application/json" \
  -d '{"claim":"coffee improves alertness","limit":15}'
```

### Step 4: Share

1. Open `$RENDER_URL` in browser
2. Paste claim + your LLM key (any provider below)
3. Click a button → results in modal

## BYOK LLM Providers

The CLI itself is keyless/heuristic. When you supply a key (`X-LLM-Key` header,
never stored or logged), the web layer makes ONE chat call to your chosen
provider to synthesize the CLI output into a structured verdict
(`llm_synthesis`: stance / confidence / reasoning / key evidence). If the LLM
call fails you still get the full heuristic result plus a redacted `llm_error`.

| provider | base_url | default model | get a key |
|---|---|---|---|
| `anthropic` | api.anthropic.com/v1 (native Messages API) | claude-haiku-4-5 | console.anthropic.com |
| `openai` | api.openai.com/v1 | gpt-5-mini | platform.openai.com |
| `gemini` | generativelanguage.googleapis.com/v1beta/openai | gemini-2.5-flash | aistudio.google.com/apikey |
| `groq` | api.groq.com/openai/v1 | llama-3.3-70b-versatile | console.groq.com |
| `mistral` | api.mistral.ai/v1 | mistral-small-latest | console.mistral.ai |
| `deepseek` | api.deepseek.com | deepseek-chat | platform.deepseek.com |
| `zai` | api.z.ai/api/paas/v4 | glm-5 | z.ai/model-api |
| `moonshot` | api.moonshot.ai/v1 | kimi-k2.6 | platform.moonshot.ai |
| `qwen` | dashscope-intl.aliyuncs.com/compatible-mode/v1 | qwen3-max | Alibaba Cloud Model Studio |
| `minimax` | api.minimax.io/v1 | MiniMax-M2.7 | platform.minimax.io |
| `xai` | api.x.ai/v1 | grok-4-fast | console.x.ai |
| `openrouter` | openrouter.ai/api/v1 | deepseek/deepseek-chat | openrouter.ai/keys |

All providers except `anthropic` speak the OpenAI chat-completions format.
`openrouter` is a meta-provider: the optional `model` body field selects any
hosted model (including `:free` ones), so always set it there. `model` is an
opaque token: trimmed, max 128 chars, no whitespace/control characters.

```bash
# 1. Heuristic (no key) — CLI result only
curl -X POST $RENDER_URL/api/consensus \
  -H "Content-Type: application/json" \
  -d '{"claim":"vitamin D reduces infections","limit":20}'

# 2. DeepSeek synthesis (default model deepseek-chat)
curl -X POST $RENDER_URL/api/consensus \
  -H "Content-Type: application/json" \
  -H "X-LLM-Key: sk-your-deepseek-key" \
  -d '{"claim":"vitamin D reduces infections","provider":"deepseek","limit":20}'

# 3. OpenRouter with an explicit (free) model
curl -X POST $RENDER_URL/api/consensus \
  -H "Content-Type: application/json" \
  -H "X-LLM-Key: sk-or-your-openrouter-key" \
  -d '{"claim":"vitamin D reduces infections","provider":"openrouter","model":"deepseek/deepseek-chat-v3-0324:free","limit":20}'
```

## Redis cache

The web layer caches the child CLI's JSON payload in Redis (`cache.go`). It is a
strict soft dependency: with no Redis, an unreachable Redis, or a wrong password,
every request behaves exactly as it did before the cache existed — only slower.
Nothing below can break a deploy; it can only make one slow.

### Where Redis runs

One shared instance for the whole bundle, not one per app:

| property | value |
|---|---|
| compose project | `/opt/pubvera/redis/` |
| network | `pubvera-shared` (external Docker network) |
| published ports | **none** — reachable only from containers on `pubvera-shared` |
| memory | `maxmemory 512MB`, `maxmemory-policy allkeys-lru` |
| persistence | off — `save ""`, `appendonly no` |
| auto-update | excluded from Watchtower (`com.centurylinklabs.watchtower.enable=false`) |

Persistence is off deliberately: every entry is recomputable by re-running the
CLI, so a restart costs one cold fetch per query and nothing else. `allkeys-lru`
means a full instance evicts its coldest keys instead of returning errors. The
port stays unpublished because there is no reason for anything outside the shared
network to reach it, and Watchtower is disabled so an unattended image bump can
never restart the cache under a running app.

### REDIS_PASSWORD lives in TWO files

The same password is written in two places, and they must match:

- `/opt/pubvera/redis/.env` — what the Redis container starts up with
- `/opt/pubvera/pubvera-corpova/.env` — what this app authenticates with

**Rotating it means editing both, plus the `.env` of every other app attached to
`pubvera-shared`.** Changing one and not the others is the single most likely
future cause of `auth failed` in the logs: the app keeps serving correct results
(a failed AUTH degrades to a cache miss), it just quietly runs uncached while the
log fills with cache failure lines. If you see that, check the other `.env`
first — the code is almost certainly fine.

### Attaching another app to the cache

In the new app's compose file, list **both** networks on the service and declare
the shared one as external:

```yaml
services:
  my-app:
    networks:
      - default        # MANDATORY — do not omit
      - pubvera-shared

networks:
  pubvera-shared:
    external: true
```

Writing `default` is not optional. The moment a service names any network,
Compose stops attaching it to the project's implicit `default` network — so
listing only `pubvera-shared` gains the app the cache and loses it its own
network, breaking service-name DNS to its own siblings.

The app also needs `REDIS_ADDR` (the shared container's `host:port` on
`pubvera-shared`) and `REDIS_PASSWORD` in its environment. An empty `REDIS_ADDR`,
or `CACHE_DISABLED=1`, runs everything uncached by design.

### Cache keys and when to bump the version

    sc:<engine>:<clihash>:<paramhash>

Two independent invalidation handles, because a verdict can move in two
independent ways:

- **`clihash` — automatic.** The first 12 hex digits of the sha256 of the CLI
  binary the process actually shells out to. Replacing
  `bin/scientific-consensus-pp-cli-linux` re-keys the whole cache by itself, with
  no human step and no deploy note.
- **`engine` — manual** (`cacheEngineVersion` in `cache.go`). Bump it when the
  **web** layer's scoring logic changes — divergence rules, compaction, the
  synthesis prompt — because none of that touches the CLI binary, so no hash
  moves on its own. A needless bump costs one cold fetch per query; a missing one
  serves verdicts already known to be wrong for the full 7-day TTL.

Rule of thumb: CLI binary swap → nothing to do. Web-layer scoring change → bump
`cacheEngineVersion` in the same commit.

### Verify after deploy

```bash
docker logs corpova | grep -i cache
```

Expect a `cache: redis ... ready` line, and check that its `cli=` value matches
the hash of the binary actually in the image:

```bash
sha256sum bin/scientific-consensus-pp-cli-linux | cut -c1-12
```

Currently `b659fff65cb9`. A `cli=nohash` instead means the binary could not be
read — the cache still works, but a binary swap will no longer invalidate it, so
fix the binary rather than living with it.

Measured in production on 2026-07-30: **cold 2.25 s, cached hit 0.018 s** (~125x).

## Troubleshooting

**"Cannot find CLI binary"**
- Ensure `bin/scientific-consensus-pp-cli-linux` exists before `docker build`.
- If missing, re-run vendoring + cross-compile: `./vendor-cli.sh`.

**"Port 8090 not accessible"**
- Render assigns a `PORT` env var (not necessarily 8090).
- The server reads `PORT` and binds `0.0.0.0:$PORT` automatically.
- Test: `curl $RENDER_URL/healthz` (should return `ok`).

**"Free tier spins down after 15 min idle"**
- That's normal. Render wakes it on next request (~30s cold start).
- Upgrade to Starter ($7/mo) for always-on.
