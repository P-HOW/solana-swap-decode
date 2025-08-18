package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	solanaswapgo "github.com/franco-bianco/solanaswap-go/solanaswap-go"
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

type apiError struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	// Hardcoded Helius RPC URL with API key (per your request)
	rpcURL := "https://mainnet.helius-rpc.com/?api-key=f7aa96fd-2bb1-49ce-8468-894bcbb22551"
	// Per-request RPC timeout
	const rpcTimeout = 10 * time.Second

	// Max transaction version (same as your original)
	var maxTxVersionU64 uint64 = 0

	// Build the Solana RPC client once; it's safe for concurrent use.
	client := rpc.New(rpcURL)

	// Health endpoint
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Parse endpoint
	http.HandleFunc("/parse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method_not_allowed"})
			return
		}

		var req parseReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "invalid JSON body"})
			return
		}
		if req.Signature == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "signature is required"})
			return
		}

		// Validate base58 sig without panicking
		var sig solana.Signature
		var sigErr error
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					sigErr = errors.New("invalid signature format")
				}
			}()
			sig = solana.MustSignatureFromBase58(req.Signature)
		}()
		if sigErr != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "bad_request", Details: "invalid signature (base58)"})
			return
		}

		// Enforce 10s per request RPC timeout
		ctx, cancel := context.WithTimeout(r.Context(), rpcTimeout)
		defer cancel()

		tx, err := client.GetTransaction(ctx, sig, &rpc.GetTransactionOpts{
			Commitment:                     rpc.CommitmentConfirmed,
			MaxSupportedTransactionVersion: &maxTxVersionU64,
		})

		// If timeout or deadline exceeded -> return {null, null} with 200 OK
		if err != nil {
			// Detect timeouts robustly (SDKs often wrap errors)
			if errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(strings.ToLower(err.Error()), "deadline") ||
				strings.Contains(strings.ToLower(err.Error()), "timeout") {
				writeJSON(w, http.StatusOK, parseResp{Transaction: nil, SwapInfo: nil})
				return
			}
			// Non-timeout error
			writeJSON(w, http.StatusBadGateway, apiError{Error: "rpc_error", Details: err.Error()})
			return
		}

		// If RPC returns no transaction (not found), treat as a normal 404
		if tx == nil {
			writeJSON(w, http.StatusNotFound, apiError{Error: "not_found", Details: "transaction not found"})
			return
		}

		parser, err := solanaswapgo.NewTransactionParser(tx)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "parse_init_error", Details: err.Error()})
			return
		}

		transactionData, err := parser.ParseTransaction()
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "parse_tx_error", Details: err.Error()})
			return
		}

		swapInfo, err := parser.ProcessSwapData(transactionData)
		if err != nil {
			// Not all txs are swaps — OK to proceed with nil SwapInfo.
			log.Printf("swap processing warning: %v", err)
		}

		writeJSON(w, http.StatusOK, parseResp{
			Transaction: transactionData,
			SwapInfo:    swapInfo, // may be nil
		})
	})

	// Hardened HTTP server settings
	addr := ":8080"
	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second, // enough to serialize JSON
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("listening on http://%s (rpc=%s, per-request timeout=%ss)",
		addr, rpcURL, strconv.Itoa(int(rpcTimeout/time.Second)))

	// Start server (fatal on bind error)
	log.Fatal(srv.ListenAndServe())
}
