package plaidclient

import (
	"context"

	"github.com/plaid/plaid-go/v46/plaid"
)

func NewClient(clientID, secret string) *plaid.APIClient {
	configuration := plaid.NewConfiguration()
	configuration.AddDefaultHeader("PLAID-CLIENT-ID", clientID)
	configuration.AddDefaultHeader("PLAID-SECRET", secret)
	configuration.UseEnvironment(plaid.Sandbox)
	return plaid.NewAPIClient(configuration)
}

func CreateLinkToken(ctx context.Context, client *plaid.APIClient) (string, error) {
	user := plaid.LinkTokenCreateRequestUser{
		ClientUserId: "dev-user-1",
	}
	request := plaid.NewLinkTokenCreateRequest(
		"LedgerSignal",
		"en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US},
	)
	request.SetUser(user)
	request.SetProducts([]plaid.Products{plaid.PRODUCTS_TRANSACTIONS})

	resp, _, err := client.PlaidApi.LinkTokenCreate(ctx).
		LinkTokenCreateRequest(*request).
		Execute()
	if err != nil {
		return "", err
	}
	return resp.GetLinkToken(), nil
}

func ExchangePublicToken(ctx context.Context, client *plaid.APIClient, publicToken string) (accessToken, itemID string, err error) {
	resp, _, err := client.PlaidApi.ItemPublicTokenExchange(ctx).
		ItemPublicTokenExchangeRequest(
			*plaid.NewItemPublicTokenExchangeRequest(publicToken),
		).Execute()
	if err != nil {
		return "", "", err
	}
	return resp.GetAccessToken(), resp.GetItemId(), nil
}

func CreateSandboxPublicToken(ctx context.Context, client *plaid.APIClient) (string, error) {
	resp, _, err := client.PlaidApi.SandboxPublicTokenCreate(ctx).
		SandboxPublicTokenCreateRequest(
			*plaid.NewSandboxPublicTokenCreateRequest(
				"ins_109508", // Plaid's canonical Sandbox test bank
				[]plaid.Products{plaid.PRODUCTS_TRANSACTIONS},
			),
		).Execute()
	if err != nil {
		return "", err
	}
	return resp.GetPublicToken(), nil
}

func SyncTransactions(ctx context.Context, client *plaid.APIClient, accessToken string) (added []plaid.Transaction, hasMore bool, err error) {
	request := plaid.NewTransactionsSyncRequest(accessToken)
	resp, _, err := client.PlaidApi.TransactionsSync(ctx).
		TransactionsSyncRequest(*request).
		Execute()
	if err != nil {
		return nil, false, err
	}
	return resp.GetAdded(), resp.GetHasMore(), nil
}