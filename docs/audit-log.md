# Audit Log — Phase 8

`internal/audit` writes a small, persistent, queryable record of security-relevant events to a new `audit_log` table (migration `000005`) — replacing what used to only exist as ephemeral `log.Printf` lines that vanish on restart and can't be searched.

## Why this exists

Before this, the *only* record that someone tried to forge a webhook, or that an account got linked, was whatever was still sitting in stdout. `internal/webhookverify` and `internal/ratelimit` (both added this session) already decide what to reject — this is what makes those decisions look-up-able later, instead of only visible in real time.

## What gets logged

| Event | When | Detail |
|---|---|---|
| `item_linked` | An account finishes linking (real or Sandbox shortcut) | `via: "exchange_public_token"` or `"sandbox_shortcut"` |
| `webhook_accepted` | A webhook passes signature verification | `webhook_type`, `webhook_code` |
| `webhook_rejected` | A webhook fails signature verification | `reason`, `remote_addr` |
| `webhook_rate_limited` | A webhook is throttled before verification even runs | `remote_addr` |

## Querying it

```sql
-- everything from the last hour
SELECT event_type, item_id, detail, created_at
FROM audit_log
WHERE created_at > now() - interval '1 hour'
ORDER BY created_at DESC;

-- a burst of rejected/throttled webhooks (what an attempted attack looks like)
SELECT event_type, count(*)
FROM audit_log
WHERE event_type IN ('webhook_rejected', 'webhook_rate_limited')
GROUP BY event_type;
```

## Design notes

- **Failures never block the real request.** `Logger.Log` logs its own errors (via `log.Printf`) rather than returning them — a broken audit write should never be the reason a webhook or a link flow fails.
- **`internal/ratelimit` still knows nothing about Postgres.** `Middleware` takes an optional `onRejected func(*http.Request)` callback instead of an audit dependency directly — `main.go` is the only place that wires the two together, keeping `ratelimit` reusable and independently testable.
- **Rejection reasons are logged in full here, even though they're never sent back in the HTTP response.** Hiding the reason from a caller (so a forger can't use it to refine their attempt) and hiding it from *us* are different things — the audit record is for investigating later, not for whoever sent the request.
