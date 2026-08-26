"""SQLAlchemy engine/session setup — the one place this service configures its
database connection. Everything else imports SessionLocal from here."""
from sqlalchemy import create_engine
from sqlalchemy.orm import declarative_base, sessionmaker

from . import config

# pool_pre_ping checks a connection is still alive before handing it out —
# cheap insurance against using a connection Postgres has silently dropped.
engine = create_engine(config.DATABASE_URL, pool_pre_ping=True)

SessionLocal = sessionmaker(bind=engine, autoflush=False, autocommit=False)

Base = declarative_base()
