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
