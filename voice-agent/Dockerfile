FROM ghcr.io/astral-sh/uv:0.13.5 AS uv
FROM python:3.13-slim
ENV PYTHONUNBUFFERED=1 UV_COMPILE_BYTECODE=1
COPY --from=uv /uv /uvx /bin/
RUN useradd --create-home --uid 10001 helia
WORKDIR /app
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev
COPY agent.py bridge.py mixer.py profile_prompt.py context_tools.py work_notices.py transcript.py notes.py ./
USER helia
EXPOSE 8081
HEALTHCHECK --interval=15s --timeout=3s CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8081/', timeout=2)"
CMD ["/app/.venv/bin/python", "agent.py", "start"]
