// This is the entry point of the program — every Go executable needs exactly one
// package named "main" with exactly one func main() inside it. This file does almost
// no real work itself anymore; it just wires together the pieces built in internal/.
package main

import (
	"context"
	"log"      // simple logging to the terminal
	"net/http" // Go's standard library HTTP server
	"os"       // lets us read environment variables

	"github.com/go-chi/chi/v5" // our HTTP router (matches routes to handlers, enforces verbs)
	"github.com/joho/godotenv" // loads variables from a .env file into the environment

	// Our own packages, referenced by their full module path
	// (module name "ledgersignal/ingestion", from go.mod, plus the folder path).
	"ledgersignal/ingestion/internal/api"
	"ledgersignal/ingestion/internal/plaidclient"
	"ledgersignal/ingestion/internal/storage"
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

	// Bundle both dependencies into one Server, which every handler method will use.
	srv := api.NewServer(plaidClient, db)

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

	log.Println("listening on :8080")
	// http.ListenAndServe starts the actual web server on port 8080, using our
	// router `r` to handle every request. This call blocks forever (the program
	// just sits here serving requests) unless something goes wrong, in which case
	// log.Fatal prints the error and exits.
	log.Fatal(http.ListenAndServe(":8080", r))
}
