"""Configuration for the intelligence service, loaded from intelligence/.env.

Deliberately its own .env, not shared with the Go service's — Python only
needs DATABASE_URL and Kafka settings, never the Plaid credentials or
encryption key Go uses. Same "least privilege" reasoning as splitting the two
services into independently deployable units in the first place.
"""
import os
from pathlib import Path

from dotenv import load_dotenv

# Load intelligence/.env specifically (by path relative to this file), rather
# than relying on load_dotenv()'s default "search upward from cwd" behavior —
# that would risk picking up the wrong .env if this service is ever started
# from a different working directory.
load_dotenv(Path(__file__).resolve().parent / ".env")

DATABASE_URL = os.environ["DATABASE_URL"]
KAFKA_BROKERS = os.environ["KAFKA_BROKERS"]
KAFKA_TOPIC = os.environ["KAFKA_TOPIC"]
KAFKA_GROUP_ID = os.environ["KAFKA_GROUP_ID"]
