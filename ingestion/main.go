package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"

	"ledgersignal/ingestion/internal/api"
	"ledgersignal/ingestion/internal/plaidclient"
	"ledgersignal/ingestion/internal/storage"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on real env vars")
	}

	plaidClient := plaidclient.NewClient(os.Getenv("PLAID_CLIENT_ID"), os.Getenv("PLAID_SECRET"))

	db, err := storage.NewPool(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("failed to connect to postgres: ", err)
	}
	defer db.Close()

	srv := api.NewServer(plaidClient, db)

	r := chi.NewRouter()
	r.Post("/link/token", srv.HandleCreateLinkToken)
	r.Post("/link/exchange", srv.HandleExchangePublicToken)
	r.Post("/dev/sandbox-link", srv.HandleSandboxLink)
	r.Post("/dev/sync-transactions", srv.HandleSyncTransactions)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}