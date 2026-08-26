// This is the entry point of the program — every Go executable needs exactly one
// package named "main" with exactly one func main() inside it. This file does almost
// no real work itself anymore; it just wires together the pieces built in internal/.
package main

import (
	"context"
	"log"      // simple logging to the terminal
	"net/http" // Go's standard library HTTP server
	"os"       // lets us read environment variables
	"time"     // lets us express the retry backoff delay

	"github.com/go-chi/chi/v5" // our HTTP router (matches routes to handlers, enforces verbs)
	"github.com/joho/godotenv" // loads variables from a .env file into the environment

	// Our own packages, referenced by their full module path
	// (module name "ledgersignal/ingestion", from go.mod, plus the folder path).
	"ledgersignal/ingestion/internal/api"
	kafkaproducer "ledgersignal/ingestion/internal/kafka"
	"ledgersignal/ingestion/internal/plaidclient"
	"ledgersignal/ingestion/internal/storage"
	"ledgersignal/ingestion/internal/worker"
)

// How many goroutines process background sync jobs concurrently, how many
// jobs can queue up before Enqueue starts blocking, and the retry behavior
// for jobs that fail. Small, deliberately conservative numbers for now —
// easy to tune once there's real load to measure.
const (
	syncWorkerCount = 3
	syncQueueSize   = 10
	syncMaxAttempts = 3               // total tries per job, including the first
	syncBaseDelay   = 2 * time.Second // doubles each retry: 2s, 4s, ...
)

func main() {
	// godotenv.Load() reads the .env file in the current directory and sets each
	// line as a real environment variable, so os.Getenv(...) below can find them.
	// If no .env file exists (e.g. in production, where real env vars are set another way),
	// this just logs a message and continues — it's not treated as fatal.
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on real env vars")
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

	// Build the background worker pool. The handler function passed in is a
	// closure — an inline function that "closes over" (captures) plaidClient,
	// db, and producer from this surrounding scope, so every worker can call
	// the real sync logic without needing a Server instance at all.
	pool := worker.NewPool(syncWorkerCount, syncQueueSize, syncMaxAttempts, syncBaseDelay, func(ctx context.Context, job worker.Job) error {
		_, _, err := api.SyncItemTransactions(ctx, plaidClient, db, producer, job.ItemID)
		return err
	})

	// Bundle every dependency into one Server, which every handler method will use.
	srv := api.NewServer(plaidClient, db, pool, producer)

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
	r.Post("/webhooks/plaid", srv.HandleWebhook)
	r.Post("/dev/set-webhook", srv.HandleSetWebhook)
	r.Post("/dev/fire-webhook", srv.HandleFireWebhook)

	log.Println("listening on :8080")
	// http.ListenAndServe starts the actual web server on port 8080, using our
	// router `r` to handle every request. This call blocks forever (the program
	// just sits here serving requests) unless something goes wrong, in which case
	// log.Fatal prints the error and exits.
	log.Fatal(http.ListenAndServe(":8080", r))
}
