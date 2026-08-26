# Bugs Log

A running log of real bugs hit while building LedgerSignal, and how they were fixed. Kept separate from `architecture.md`/`transactions.md` since these are about what went wrong and why, not what the system currently looks like.

---

## Redpanda rejected `--advertise-pandaproxy-addr` / `--advertise-schema-registry-addr`

**Phase:** 4 (Kafka producer) — Step 1, adding Redpanda to `docker-compose.yml`

**What happened:** The first `docker-compose.yml` config for Redpanda was based on a widely-shared example, including flags to configure and advertise the HTTP Proxy (Pandaproxy) and Schema Registry alongside the Kafka API. The container started, then immediately exited (`Exited (1)`).

**Root cause:** `docker logs linkvault-redpanda` showed the actual error clearly:
```
ERROR main - cli_parser.cc:45 - Argument parse error: unrecognised option '--advertise-schema-registry-addr=internal://redpanda:8081,external://localhost:18081'
```
The `redpanda` binary in this image version didn't accept that flag in the form it was given. Rather than dig into the exact correct syntax for two services (Pandaproxy, Schema Registry) that LedgerSignal was never actually going to use — no Avro schemas, no REST-based produce/consume — the flags were simply removed.

**Fix:** Simplified the Redpanda `command:` block down to only what's needed for plain Kafka-protocol produce/consume: `--kafka-addr`, `--advertise-kafka-addr`, `--rpc-addr`, `--advertise-rpc-addr`, plus the resource-limiting flags (`--smp`, `--memory`, `--overprovisioned`). Removed the corresponding `18081`/`18082` port mappings too, since nothing listens on them anymore.

**Lesson:** When a copied config fails, check the actual container logs before trying to patch the exact flag syntax — the fix might be "delete the thing you don't need" rather than "fix how you're asking for it." A smaller, working config beats a complete one that's fighting you.

---

## `pydantic-core` failed to build against Python 3.14

**Phase:** 5 (Python intelligence service) — installing dependencies

**What happened:** `pip install -r requirements.txt` with version-pinned `pydantic==2.10.4` failed while building `pydantic-core` from source, with a Rust/`maturin` compile error.

**Root cause:** The pinned `pydantic`/`pydantic-core` version predates this machine's Python (3.14) — no prebuilt wheel existed for it yet, so `pip` fell back to compiling `pydantic-core`'s Rust extension from source, and the pinned version's `pyo3` dependency explicitly didn't support Python 3.14 (`PyO3's maximum supported version (3.13)`).

**Fix:** Removed the version pins from `requirements.txt` entirely and let `pip` resolve the latest compatible versions, which included a prebuilt `pydantic-core` wheel targeting `cp314` — no compilation needed, installed cleanly.

**Lesson:** Pinning exact versions is good practice for reproducibility, but a pin chosen without checking it against the actual local Python version can turn "reproducible" into "broken on this machine." Worth revisiting these pins deliberately later (e.g. once this is containerized for deployment with a fixed Python version) rather than leaving requirements.txt unpinned indefinitely.
