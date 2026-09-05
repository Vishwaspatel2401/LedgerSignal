"""FastAPI entry point. Run with:

    intelligence/venv/bin/uvicorn intelligence.main:app --reload

from the repo root (the venv lives inside intelligence/, but the command
still runs from the root so Python resolves the `intelligence` package
correctly) — the Kafka consumer starts automatically as a background task
when the app starts, and stops cleanly when it shuts down.
"""
import asyncio
import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI
from pydantic import BaseModel

from .consumer import consume_forever
from .db import SessionLocal
from .nl_query import answer_question

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


class QueryRequest(BaseModel):
    question: str


class QueryResponse(BaseModel):
    answer: str


@app.post("/query", response_model=QueryResponse)
async def query(request: QueryRequest):
    """The NL query interface — ask a plain-English question about risk
    signals or spending, get a plain-English answer back. Runs the blocking
    Claude + DB work on a separate thread via asyncio.to_thread, same reason
    consumer.py does it for _process_event: this is a synchronous call
    stack (anthropic's SDK, SQLAlchemy) inside an async server, and running
    it inline would block every other request this server is handling."""
    def _run() -> str:
        with SessionLocal() as session:
            return answer_question(session, request.question)

    answer = await asyncio.to_thread(_run)
    return QueryResponse(answer=answer)
