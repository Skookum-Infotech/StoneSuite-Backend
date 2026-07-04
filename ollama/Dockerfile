FROM ollama/ollama:latest

# Model is pulled by entrypoint.sh (not here) so it lands on the persistent
# volume at /root/.ollama instead of baked into the image layer — keeps the
# image small and lets the model survive redeploys without re-downloading.
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV OLLAMA_HOST=0.0.0.0:11434

EXPOSE 11434

ENTRYPOINT ["/entrypoint.sh"]
