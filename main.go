package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	solanaswapgo "github.com/franco-bianco/solanaswap-go/solanaswap-go"
	holder "github.com/franco-bianco/solanaswap-go/spltoken/holder"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

type parseReq struct {
	Signature string `json:"signature"`
}

type parseResp struct {
	Transaction interface{} `json:"transaction"`
	SwapInfo    interface{} `json:"swapInfo"`
}

type holdersReq struct {
	Mint string `json:"mint"`
}

type holdersResp struct {
	Mint          string `json:"mint"`
	Holders       int    `json:"holders"`
	TotalAccounts int    `json:"totalAccounts"`
	ProgramUsed   string `json:"programUsed,omitempty"`
}

type apiError struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func writeJSONMaybePretty(w http.ResponseWriter, status int, v interface{}, pretty bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(v)
}

func main() {
	// Load RPC URL from environment (fallback keeps old behavior)
	defaultRPC := "https://mainnet.helius-rpc.com/?api-key=f7aa96fd-2bb1-49ce-8468-894bcbb22551"
	rpcURL := strings.TrimSpace(os.Getenv("SOLANA_RPC_URL"))
	if rpcURL == "" {
		rpcURL = defaultRPC
	}

	const rpcTimeout = 10 * time.Second

	// Max transaction version
	var maxTxVersionU64 uint64 = 0

	// Shared Solana RPC client (safe for concurrent use)
	client := rpc.New(rpcURL)

	// Health endpoint
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Simple HTML form for browser use (GET)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
<!doctype html>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Solana Tx Parser</title>
<div style="font: 16px system-ui; max-width: 900px; margin: 40px auto; line-height:1.5;">
  <h1 style="margin:0 0 16px;">Solana Swap Decode (browser)</h1>
  <form action="/parse" method="get">
    <label>Signature<br>
      <input name="signature" style="width: 100%; padding: 8px;" placeholder="Paste a transaction signature" autofocus>
    </label>
    <div style="margin: 12px 0;">
      <label><input type="checkbox" name="pretty" value="1" checked> pretty</label>
    </div>
    <button type="submit" style="padding: 8px 14px;">Parse</button>
  </form>

  <h2 style="margin:32px 0 8px;">Holder Count</h2>
  <form action="/holders" method="get">
    <label>Mint Address<br>
      <input name="mint" style="width: 100%; padding: 8px;" placeholder="Enter mint address">
    </label>
    <div style="margin: 12px 0;">
      <label><input type="checkbox" name="pretty" value="1" checked> pretty</label>
    </div>
    <button type="submit" style="padding: 8px 14px;">Count Holders</button>
  </form>

  <p style="margin-top: 24px; color:#666;">This page issues GETs to <code>/parse?signature=...&pretty=1</code> and <code>/holders?mint=...&pretty=1</code>.</p>
</div>
`))
	})

	// Parse endpoint: supports POST (JSON) and GET (?signature=...&pretty=1)
	http.HandleFunc("/parse", func(w http.ResponseWriter, r *http.Request) {
		pretty := r.URL.Query().Get("pretty") == "1" || r.URL.Query().Get("pretty") == "true"

		// Accept POST with JSON body or GET with query param
		var sigStr string
		switch r.Method {
		case http.MethodPost:
			var req parseReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONMaybePretty(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "invalid JSON body"}, pretty)
				return
			}
			sigStr = req.Signature
		case http.MethodGet:
			sigStr = r.URL.Query().Get("signature")
		default:
			writeJSONMaybePretty(w, http.StatusMethodNotAllowed, apiError{Error: "method_not_allowed"}, pretty)
			return
		}

		if sigStr == "" {
			writeJSONMaybePretty(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "signature is required"}, pretty)
			return
		}

		// Validate base58 signature without panicking
		var sig solana.Signature
		var sigErr error
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					sigErr = errors.New("invalid signature format")
				}
			}()
			sig = solana.MustSignatureFromBase58(sigStr)
		}()
		if sigErr != nil {
			writeJSONMaybePretty(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "invalid signature (base58)"}, pretty)
			return
		}

		// Per-request RPC timeout
		ctx, cancel := context.WithTimeout(r.Context(), rpcTimeout)
		defer cancel()

		tx, err := client.GetTransaction(ctx, sig, &rpc.GetTransactionOpts{
			Commitment:                     rpc.CommitmentConfirmed,
			MaxSupportedTransactionVersion: &maxTxVersionU64,
		})
		if err != nil {
			low := strings.ToLower(err.Error())
			if errors.Is(err, context.DeadlineExceeded) || strings.Contains(low, "deadline") || strings.Contains(low, "timeout") {
				// Graceful timeout: return 200 with nulls
				writeJSONMaybePretty(w, http.StatusOK, parseResp{Transaction: nil, SwapInfo: nil}, pretty)
				return
			}
			writeJSONMaybePretty(w, http.StatusBadGateway, apiError{Error: "rpc_error", Details: err.Error()}, pretty)
			return
		}
		if tx == nil {
			writeJSONMaybePretty(w, http.StatusNotFound, apiError{Error: "not_found", Details: "transaction not found"}, pretty)
			return
		}

		parser, err := solanaswapgo.NewTransactionParser(tx)
		if err != nil {
			writeJSONMaybePretty(w, http.StatusUnprocessableEntity, apiError{Error: "parse_init_error", Details: err.Error()}, pretty)
			return
		}

		transactionData, err := parser.ParseTransaction()
		if err != nil {
			writeJSONMaybePretty(w, http.StatusUnprocessableEntity, apiError{Error: "parse_tx_error", Details: err.Error()}, pretty)
			return
		}

		swapInfo, err := parser.ProcessSwapData(transactionData)
		if err != nil {
			// Not all transactions are swaps; keep going with nil SwapInfo
			log.Printf("swap processing warning: %v", err)
		}

		writeJSONMaybePretty(w, http.StatusOK, parseResp{
			Transaction: transactionData,
			SwapInfo:    swapInfo, // may be nil
		}, pretty)
	})

	// Holder count endpoint (GET ?mint=... or POST {"mint": "..."}; supports &pretty=1)
	http.HandleFunc("/holders", func(w http.ResponseWriter, r *http.Request) {
		pretty := r.URL.Query().Get("pretty") == "1" || r.URL.Query().Get("pretty") == "true"

		var mint string
		switch r.Method {
		case http.MethodPost:
			var req holdersReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSONMaybePretty(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "invalid JSON body"}, pretty)
				return
			}
			mint = strings.TrimSpace(req.Mint)
		case http.MethodGet:
			mint = strings.TrimSpace(r.URL.Query().Get("mint"))
		default:
			writeJSONMaybePretty(w, http.StatusMethodNotAllowed, apiError{Error: "method_not_allowed"}, pretty)
			return
		}

		if mint == "" {
			writeJSONMaybePretty(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "mint is required"}, pretty)
			return
		}

		// Call the long-running counter (it manages its own 60m retry window on rate limits).
		res, err := holder.CountHoldersForMint(context.Background(), mint)
		if err != nil {
			writeJSONMaybePretty(w, http.StatusBadGateway, apiError{Error: "holder_count_error", Details: err.Error()}, pretty)
			return
		}

		resp := holdersResp{
			Mint:          mint,
			Holders:       res.Holders,
			TotalAccounts: res.TotalAccounts,
		}
		if (res.ProgramUsed != solana.PublicKey{}) {
			resp.ProgramUsed = res.ProgramUsed.String()
		}
		writeJSONMaybePretty(w, http.StatusOK, resp, pretty)
	})

	// HTTP server settings
	addr := ":8080"
	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// Holder count can run up to 60 minutes; give some headroom:
		WriteTimeout: 65 * time.Minute,
		IdleTimeout:  65 * time.Minute,
	}

	log.Printf("listening on http://%s (tx rpc=%s, per-request tx timeout=%ss; holders use %s)",
		addr, rpcURL, strconv.Itoa(int(rpcTimeout/time.Second)), holder.EnvRPCForCounter)
	log.Fatal(srv.ListenAndServe())
}
