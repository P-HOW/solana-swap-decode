package holder

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Env var name (exact string).
const EnvRPCForCounter = "SOLANA_RPC_URL_FOR_COUNTER"

// SPL program IDs.
var (
	ProgramToken      = solana.MustPublicKeyFromBase58("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	ProgramToken2022  = solana.MustPublicKeyFromBase58("TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb")
	tokenAcctDataSize = uint64(165) // Token account layout size
)

type Result struct {
	Holders       int
	TotalAccounts int
	ProgramUsed   solana.PublicKey
}

// CountHoldersForMint reproduces the TS "auto" behavior and obeys:
// 1) keep retrying up to 60 minutes on *rate-limit* errors;
// 2) return immediately on any other error.
func CountHoldersForMint(_ context.Context, mintBase58 string) (*Result, error) {
	mint, err := solana.PublicKeyFromBase58(mintBase58)
	if err != nil {
		return nil, fmt.Errorf("invalid mint: %w", err)
	}
	rpcURL := os.Getenv(EnvRPCForCounter)
	if rpcURL == "" {
		return nil, fmt.Errorf("%s is not set", EnvRPCForCounter)
	}
	client := rpc.New(rpcURL)

	// Try Token first.
	if r, err := countForProgram(mint, client, ProgramToken); err == nil && r.TotalAccounts > 0 {
		r.ProgramUsed = ProgramToken
		return &r, nil
	} else if err != nil {
		return nil, fmt.Errorf("token scan error: %w", err)
	}

	// Then Token-2022.
	if r, err := countForProgram(mint, client, ProgramToken2022); err == nil && r.TotalAccounts > 0 {
		r.ProgramUsed = ProgramToken2022
		return &r, nil
	} else if err != nil {
		return nil, fmt.Errorf("token2022 scan error: %w", err)
	}

	// If neither program produced accounts and no non-rate-limit error occurred,
	// return empty result.
	return &Result{Holders: 0, TotalAccounts: 0}, nil
}

// countForProgram performs filtered getProgramAccounts and parses JSON
// like the TS script. It ignores the caller context and enforces its
// own 60-minute retry window for *rate-limit* errors only.
func countForProgram(mint solana.PublicKey, client *rpc.Client, programID solana.PublicKey) (Result, error) {
	// Hard deadline: 60 minutes regardless of caller context.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	var (
		out rpc.GetProgramAccountsResult
		err error
	)

	// Retry on *rate limit* only, up to 60 minutes.
	backoff := 250 * time.Millisecond
	maxBackoff := 30 * time.Second
	start := time.Now()

	for {
		out, err = client.GetProgramAccountsWithOpts(
			ctx,
			programID,
			&rpc.GetProgramAccountsOpts{
				Filters: []rpc.RPCFilter{
					{DataSize: tokenAcctDataSize}, // 165-byte token account
					{Memcmp: &rpc.RPCFilterMemcmp{
						Offset: 0,            // mint field offset in token account
						Bytes:  mint.Bytes(), // first 32 bytes = mint
					}},
				},
				Encoding:   solana.EncodingJSONParsed, // same as TS parsed path
				Commitment: rpc.CommitmentConfirmed,
			},
		)

		if err == nil {
			break
		}

		// Only *rate-limit* errors are retried; anything else returns immediately.
		if !isRateLimited(err) {
			return Result{}, err
		}

		// 60-minute hard cap.
		if time.Since(start) >= 60*time.Minute {
			return Result{}, fmt.Errorf("rate-limited for 60m (timeout): %w", err)
		}

		// Backoff with jitter.
		jitter := time.Duration(rand.Int63n(int64(backoff / 3)))
		sleep := backoff + jitter
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			// Should only happen at the 60-minute boundary.
			return Result{}, fmt.Errorf("context done while rate-limited: %w", ctx.Err())
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}

	type parsedAccount struct {
		Program string `json:"program"`
		Parsed  struct {
			Info struct {
				TokenAmount struct {
					Amount string `json:"amount"`
				} `json:"tokenAmount"`
				Owner string `json:"owner"`
			} `json:"info"`
			Type string `json:"type"`
		} `json:"parsed"`
		Space int `json:"space"`
	}

	owners := make(map[string]struct{})
	total := 0

	for _, ka := range out {
		total++
		raw := ka.Account.Data.GetRawJSON()
		if len(raw) == 0 {
			continue
		}
		var p parsedAccount
		if jsonErr := json.Unmarshal(raw, &p); jsonErr != nil {
			continue
		}
		amt := p.Parsed.Info.TokenAmount.Amount
		own := p.Parsed.Info.Owner
		if own != "" && amt != "" && amt != "0" {
			owners[own] = struct{}{}
		}
	}

	return Result{
		Holders:       len(owners),
		TotalAccounts: total,
		ProgramUsed:   programID,
	}, nil
}

// ---- error helpers (string contains, case-insensitive) ----

func isRateLimited(err error) bool {
	return containsAny(err, "rate limit", "rate-limited", "429", "too many requests", "retry later")
}

func containsAny(err error, subs ...string) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	// lowercase without importing strings
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	ls := string(b)
	for _, sub := range subs {
		sb := []byte(sub)
		for i := range sb {
			if sb[i] >= 'A' && sb[i] <= 'Z' {
				sb[i] += 'a' - 'A'
			}
		}
		if indexOf(ls, string(sb)) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(haystack, needle string) int {
	nh := len(haystack)
	nn := len(needle)
	if nn == 0 {
		return 0
	}
	for i := 0; i+nn <= nh; i++ {
		if haystack[i:i+nn] == needle {
			return i
		}
	}
	return -1
}
