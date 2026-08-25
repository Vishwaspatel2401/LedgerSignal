
## POST /link/token 

Calls Plaid to generate `link token` - this is what the frontend would use to open Plaid link and start the bank connection flow 
`fun handleCreateLinkToken`
Needs - PLAID_CLIENT_ID and PLAID_SECRET from .env 
returns {'link_token' = "...."}

## POST /link/exchange 