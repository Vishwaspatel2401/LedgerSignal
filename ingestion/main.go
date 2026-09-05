// This is the entry point of the program — every Go executable needs exactly one
// package named "main" with exactly one func main() inside it. This file does almost
// no real work itself; it just wires together the pieces built in internal/.
package main

import (
	"context"
	"embed"    // lets us bundle the migrations/*.sql files into the compiled binary
	"errors"   // lets us compare against migrate.ErrNoChange
	"log"      // simple logging to the terminal
	"net/http" // Go's standard library HTTP server
	"os"       // lets us read environment variables
	"time"     // lets us express the retry backoff delay

	"github.com/go-chi/chi/v5" // our HTTP router (matches routes to handlers, enforces verbs)
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the "postgres"/"postgresql" URL scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"         // lets migrate read migrations from an embed.FS
	"github.com/joho/godotenv"                                 // loads variables from a .env file into the environment

	// Our own packages, referenced by their full module path
	// (module name "ledgersignal/ingestion", from go.mod, plus the folder path).
	"ledgersignal/ingestion/internal/api"
	"ledgersignal/ingestion/internal/audit"
	kafkaproducer "ledgersignal/ingestion/internal/kafka"
	"ledgersignal/ingestion/internal/plaidclient"
	"ledgersignal/ingestion/internal/ratelimit"
	"ledgersignal/ingestion/internal/storage"
	"ledgersignal/ingestion/internal/worker"
)

// Embedding the migrations directory into the binary means a deployed build
// carries its own schema definitions — no separate copy of migrations/ has to
// be shipped alongside it, and there's no dependency on the process's working
// directory to find them (relevant for Phase 9's move off a local machine).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// runMigrations applies every not-yet-applied migration in migrations/ against
// databaseURL, in order, on startup — the automated replacement for manually
// running `migrate` by hand once per phase. migrate.ErrNoChange just means the
// schema was already up to date; that's success, not a failure to report.
func runMigrations(databaseURL string) error {
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, databaseURL)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// How many goroutines process background sync jobs concurrently, how many
// jobs can queue up before Enqueue starts blocking, and the retry behavior
// for jobs that fail. Small, deliberately conservative numbers for now —
// easy to tune once there's real load to measure.
const (
	syncWorkerCount = 3
	syncQueueSize   = 10
	syncMaxAttempts = 3               // total tries per job, including the first
	syncBaseDelay   = 2 * time.Second // doubles each retry: 2s, 4s, ...

	// Protects the webhook endpoint (see docs/caveats.md — its signature
	// isn't verified yet) from a burst of fake deliveries filling the
	// worker pool's queue and blocking HandleWebhook itself. Generous
	// enough that real Plaid webhook bursts are never affected: 10 allowed
	// immediately, refilling at 2 more per second after that.
	webhookRateLimitBurst     = 10
	webhookRateLimitPerSecond = 2
)

func main() {
	// godotenv.Load() reads the .env file in the current directory and sets each
	// line as a real environment variable, so os.Getenv(...) below can find them.
	// If no .env file exists (e.g. in production, where real env vars are set another way),
	// this just logs a message and continues — it's not treated as fatal.
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on real env vars")
	}

	// Apply any pending schema migrations before anything else touches Postgres —
	// a fresh database (or one a phase behind) gets brought up to date automatically
	// instead of requiring someone to run `migrate` by hand first.
	if err := runMigrations(os.Getenv("DATABASE_URL")); err != nil {
		log.Fatal("failed to apply migrations: ", err)
	}

	// Build our Plaid client once, using credentials read from the environment.
	plaidClient := plaidclient.NewClient(os.Getenv("PLAID_CLIENT_ID"), os.Getenv("PLAID_SECRET"))

	// Build our Postgres connection pool once. context.Background() is the base,
	// "no special conditions" context — used here because this isn't part of
	// handling any specific incoming request, just one-time startup.
	db, err := storage.NewPool(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		// log.Fatal prints the message AND immediately exits the program — used here
		// because a server with no working database connection can't do anything useful.
		log.Fatal("failed to connect to postgres: ", err)
	}
	// `defer` schedules this call to run automatically right before main() returns —
	// in practice here, that means "close the DB pool cleanly when the program shuts down."
	defer db.Close()

	// Build the Kafka producer once. KAFKA_BROKERS/KAFKA_TOPIC are plain
	// config (not secrets), read the same way as everything else in .env.
	producer := kafkaproducer.NewProducer(os.Getenv("KAFKA_BROKERS"), os.Getenv("KAFKA_TOPIC"))
	defer producer.Close()

	// Persistent, queryable log of security-relevant events — same pool as
	// everything else, no separate connection needed.
	auditLogger := audit.NewLogger(db)

	// Build the background worker pool. The handler function passed in is a
	// closure — an inline function that "closes over" (captures) plaidClient,
	// db, and producer from this surrounding scope, so every worker can call
	// the real sync logic without needing a Server instance at all.
	pool := worker.NewPool(syncWorkerCount, syncQueueSize, syncMaxAttempts, syncBaseDelay, func(ctx context.Context, job worker.Job) error {
		_, _, err := api.SyncItemTransactions(ctx, plaidClient, db, producer, job.ItemID)
		return err
	})

	// Bundle every dependency into one Server, which every handler method will use.
	srv := api.NewServer(plaidClient, db, pool, producer, auditLogger)

	// Create a new chi router — this is what matches an incoming request's
	// method + path to the correct handler function.
	r := chi.NewRouter()

	// Each line registers one route: HTTP method, URL path, and which Server method
	// should handle it. `srv.HandleCreateLinkToken` (no parentheses) passes the method
	// itself as a value, to be called later by chi whenever a matching request arrives —
	// we're not calling it right now, just pointing at it.
	r.Post("/link/token", srv.HandleCreateLinkToken)
	r.Post("/link/exchange", srv.HandleExchangePublicToken)
	r.Post("/dev/sandbox-link", srv.HandleSandboxLink)
	r.Post("/dev/sync-transactions", srv.HandleSyncTransactions)
	// r.With(...) scopes the rate limiter to just this one route — every
	// other handler is untouched, since none of them are reachable by
	// anyone but you. The rejection hook records a burst of throttled
	// requests to the same audit trail as accepted/rejected webhooks — a
	// flood of 429s is exactly the kind of thing worth being able to find
	// later, not just something that disappears once the burst is over.
	webhookLimiter := ratelimit.NewBucket(webhookRateLimitBurst, webhookRateLimitPerSecond)
	rateLimitMiddleware := ratelimit.Middleware(webhookLimiter, func(req *http.Request) {
		auditLogger.Log(req.Context(), "webhook_rate_limited", "", map[string]any{
			"remote_addr": req.RemoteAddr,
		})
	})
	r.With(rateLimitMiddleware).Post("/webhooks/plaid", srv.HandleWebhook)
	r.Post("/dev/set-webhook", srv.HandleSetWebhook)
	r.Post("/dev/fire-webhook", srv.HandleFireWebhook)

	log.Println("listening on :8080")
	// http.ListenAndServe starts the actual web server on port 8080, using our
	// router `r` to handle every request. This call blocks forever (the program
	// just sits here serving requests) unless something goes wrong, in which case
	// log.Fatal prints the error and exits.
	log.Fatal(http.ListenAndServe(":8080", r))
}
