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

The API listens on port `3000` by default. The Compose service mounts the host's `~/.gemini` directory so `agy` can use its existing session. The Go server is compiled into the image. Node.js is retained only to run the optional `companion-for-agy` fallback when the native `agy` command fails. The `ask` client defaults to `http://localhost:3000`; use `ask set-url <url>` to point it elsewhere.

To run the API directly on a machine with Go and `agy` installed, run `go run .` from the repository root. Run `npm ci` only if you also want the optional Node.js fallback.

## API

- `POST /ask` — run a prompt; accepts `query`, optional `model`, `level`, `stream`, `continue`, `conversation_id`, and output options.
- `GET|POST /ask/stream` — stream a prompt response.
- `POST /ask-file` — analyze an uploaded file or a file path available inside the container.
- `GET /models` — list available models.
- `GET /health` — service health and defaults.
- `GET /` — service status and endpoint index.

Example:

```bash
curl -sS -X POST http://localhost:3000/ask \
  -H 'Content-Type: application/json' \
  -d '{"query":"Summarize the pending changes"}'
```

## Security

The API has no built-in request authentication and runs Antigravity with automatic tool approval. Keep it on a trusted private network; do not publish the port directly to the internet. Only mount directories the service needs.

Local `.env` files, Antigravity credentials, uploads, and scratch data are excluded from Git and the Docker build context. Use `.env.example` as the safe configuration template.
