#!/bin/sh
set -e

ollama serve &
SERVE_PID=$!

# Wait for the server to accept requests before pulling.
until ollama list >/dev/null 2>&1; do
	sleep 1
done

# Idempotent: no-op if already present on the mounted volume (survives
# machine stop/start under scale-to-zero; only re-downloads after a fresh
# volume or a model change).
ollama pull "${AI_EMBED_MODEL:-snowflake-arctic-embed:m}"

wait "$SERVE_PID"
