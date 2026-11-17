# agy-wrapper-service

A generic HTTP wrapper for the Google Antigravity CLI (`agy`), written in Go, with an optional `ask` command-line client. It accepts prompts and uploaded files without bundling domain-specific workflows.

## Requirements

- Docker and Docker Compose
- Go 1.27.3+ to run the API outside Docker
- Node.js 24+ only if the `companion-for-agy` fallback is needed
- Antigravity CLI installed and authenticated, or a mounted `~/.gemini` session when using Docker

## Configure and run

Copy `.env.example` to `.env`, adjust the defaults, and start the service:

```bash
cp .env.example .env
docker compose -f docker/docker-compose.yml up -d --build
```

The API listens on port `3000` by default. On Linux, the Compose service runs with the host user's UID/GID and mounts that user's D-Bus session socket so `agy` can access the unlocked Secret Service keyring. Set `AGY_UID`, `AGY_GID`, and `AGY_DBUS_SOCKET` in `.env` to match the host. The service also mounts `~/.gemini` for Antigravity configuration and session metadata. The Go server is compiled into the image. Node.js is retained only to run the optional `companion-for-agy` fallback when the native `agy` command cannot be started. The `ask` client defaults to `http://localhost:3000`; use `ask set-url <url>` to point it elsewhere.

To run the API directly on a machine with Go and `agy` installed, run `go run .` from the repository root. Run `npm ci` only if you also want the optional Node.js fallback.

## API

- `POST /ask` — run a prompt; accepts `query`, optional `model`, `level`, `agent`, `skills`, `conversation_id`, `title`, `stream`, and output options. The response returns the conversation ID in `X-Conversation-Id` and JSON responses.
- `POST /resume` — find a conversation by exact title or UUID; when given `query`, continue it. Duplicate titles return `409` with the candidate UUIDs.
- `GET|POST /ask/stream` — stream a prompt response with the same model, agent, skill, and conversation options.
- `GET /conversations` — list conversations in the mounted Antigravity profile, optionally filtered by title or ID with `q`, and limited with `limit` (1–200; default 50). First-prompt previews are excluded unless `include_preview=true` is explicitly requested.
- `PUT /conversations/{conversation_id}/name` — rename an existing Antigravity conversation in the mounted profile.
- `POST /ask-file` — analyze an uploaded file or a file path available inside the container.
- `GET /models` — list available models.
- `GET /agents` — list the Antigravity agents available in the mounted profile.
- `GET /skills` — list skills found in the mounted profile and enabled plugins.
- `GET /plugins` — list installed plugins and their enabled state, skills, and agents.
- `GET /health` — service health and defaults.
- `GET /` — service status and endpoint index.

## Conversation sessions and streaming

The API keeps a separate Antigravity CLI process warm for each conversation ID. Reuse the returned `X-Conversation-Id` as `conversation_id` on the next request to avoid starting a new CLI process and to preserve that conversation's context. The included `ask` client saves the most recent ID for its configured API URL; use `ask -c` to continue it or `ask --conversation <id>` to resume a specific conversation.

List conversations with `GET /conversations`, then resume one by passing its ID as `conversation_id` to `POST /ask`. For unnamed conversations, request `include_preview=true` to expose a short excerpt of the first prompt; this can contain sensitive content, so keep the API private. Set a conversation name when starting or continuing a conversation by including `title` in `POST /ask`. Rename an existing conversation with `PUT /conversations/{conversation_id}/name`. Names are saved to the Antigravity annotation metadata in the mounted `~/.gemini` profile, so they appear in Antigravity's `/resume` picker. The API only renames conversations present in that shared profile; conversation IDs are UUIDs and names are limited to 120 characters.

To resume by title instead of looking up the UUID yourself, send `POST /resume` with `title` and `query`. If multiple conversations have the same title, the endpoint returns `409` and their UUIDs; retry with the selected `conversation_id` and the same `query`. Omitting `query` resolves the conversation and returns its UUID without sending a new turn.

Choose an agent with `--agent <name>` or in an API request with `"agent"`. Invoke installed skills with repeatable `--skill <name>` options or the API's `"skills"` array. Skills are sent as Antigravity slash commands. Plugins enabled in the mounted Antigravity profile are available to the CLI automatically; `ask plugins`, `ask agents`, and `ask skills` show the available catalog. A plugin's provided agents can be selected with `--agent`, and its skills with `--skill`. Manage plugin installation and enablement in the Antigravity profile mounted at `~/.gemini`; these settings are shared by all wrapper conversations.

For the Docker deployment, manage plugins in the same persistent profile used by the wrapper:

```bash
docker exec -it agy-api-wrapper agy plugin list
docker exec -it agy-api-wrapper agy plugin enable <plugin-name>
docker exec -it agy-api-wrapper agy plugin disable <plugin-name>
```

Conversation IDs identify the context and must be treated as private. The wrapper does not authenticate API users, so anyone who obtains an ID can continue that conversation; restrict API access and do not share IDs between users.

Each conversation processes one request at a time. A concurrent request for the same conversation receives HTTP `409`; retry it after the current turn finishes. The API keeps up to `AGY_MAX_SESSIONS` warm sessions (default `32`) and closes sessions after `AGY_SESSION_IDLE_TIMEOUT` without use (default `15m`). If the server restarts or evicts a session, the next request resumes the saved conversation ID in a new CLI process.

Set `stream: true` or request `text/event-stream` to receive response chunks as Antigravity produces them. Streaming improves time to first visible text; it does not change the model's inference speed. The wrapper only falls back to `companion-for-agy` when the `agy` executable cannot be started; model, authentication, timeout, and request errors are returned directly.

Example:

```bash
curl -sS -X POST http://localhost:3000/ask \
  -H 'Content-Type: application/json' \
  -d '{"query":"Summarize the pending changes","agent":"firestore-rules-author","skills":["firestore-rules-creation"]}'
```

Resume the returned conversation in a later request by passing its `conversation_id`:

```bash
curl -sS -X POST http://localhost:3000/ask \
  -H 'Content-Type: application/json' \
  -d '{"query":"Continue with the next step","conversation_id":"<conversation-id>"}'
```

Start a named conversation:

```bash
curl -sS -X POST http://localhost:3000/ask \
  -H 'Content-Type: application/json' \
  -d '{"query":"Review the deployment plan","title":"Deployment review","json":true}'
```

Rename a conversation returned by `/ask`:

```bash
curl -sS -X PUT http://localhost:3000/conversations/<conversation-id>/name \
  -H 'Content-Type: application/json' \
  -d '{"name":"Deployment review - follow-up"}'
```

List recent conversations, optionally filtering by a name or ID:

```bash
curl -sS 'http://localhost:3000/conversations?limit=30&q=deployment'
```

To include the first-message excerpt for unnamed conversations, add `include_preview=true`:

```bash
curl -sS 'http://localhost:3000/conversations?limit=30&include_preview=true'
```

Resume one of the listed conversations with a new turn:

```bash
curl -sS -X POST http://localhost:3000/ask \
  -H 'Content-Type: application/json' \
  -d '{"query":"Continue the deployment review","conversation_id":"<conversation-id>"}'
```

Resume by title:

```bash
curl -sS -X POST http://localhost:3000/resume \
  -H 'Content-Type: application/json' \
  -d '{"title":"Deployment review","query":"Continue where we left off"}'
```

## Security

The API has no built-in request authentication and runs Antigravity with automatic tool approval. Keep it on a trusted private network; do not publish the port directly to the internet. Only mount directories the service needs.

Local `.env` files, Antigravity credentials, uploads, and scratch data are excluded from Git and the Docker build context. Use `.env.example` as the safe configuration template.

## Terms and Conditions

This is a non-profit community project, from the community for the community. Read the [Terms and Conditions](TERMS_AND_CONDITIONS.md) before using or operating an instance. Third-party services and self-hosted deployments may have separate costs and requirements.
