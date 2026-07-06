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

# Chat model pull is conditional (rather than hardcoded) so a box that only
# serves embeddings can leave AI_CHAT_MODEL unset — see ai/ollama_llm.go.
if [ -n "$AI_CHAT_MODEL" ]; then
	ollama pull "$AI_CHAT_MODEL"
fi

wait "$SERVE_PID"
