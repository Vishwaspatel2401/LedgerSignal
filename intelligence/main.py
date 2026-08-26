"""FastAPI entry point. Run with:

    venv/bin/uvicorn intelligence.main:app --reload

from the repo root — the Kafka consumer starts automatically as a background
task when the app starts, and stops cleanly when it shuts down.
"""
import asyncio
import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI

from .consumer import consume_forever

logging.basicConfig(level=logging.INFO)


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Launches the consumer loop as a background asyncio task alongside the
    # web server, rather than as a separate process — simplest option for a
    # single-instance dev setup; a real deployment might run the consumer as
    # its own process/container instead, scaled independently of the API.
    consumer_task = asyncio.create_task(consume_forever())
    yield
    consumer_task.cancel()
    try:
        await consumer_task
    except asyncio.CancelledError:
        pass


app = FastAPI(title="LedgerSignal Intelligence Service", lifespan=lifespan)


@app.get("/health")
async def health():
    return {"status": "ok"}
