package price

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// This test turns on the debug logger and prints why points are (not) produced.
// You can steer it with env vars:
//
//	SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC  → RPC
//	PRICE_DEBUG_MINT  → base58 mint (default: BONK mainnet; change to your token)
//	PRICE_DEBUG_UNIX  → unix seconds to evaluate (defaults: now-10m)
func Test_DebugPricingPath(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	mintStr := os.Getenv("PRICE_DEBUG_MINT")
	if mintStr == "" {
		// BONK as a sane default; change in env for your runs
		mintStr = "DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263"
	}
	mint := solana.MustPublicKeyFromBase58(mintStr)

	var targetUnix int64
	if v := os.Getenv("PRICE_DEBUG_UNIX"); v != "" {
		tu, err := time.Parse(time.RFC3339, v)
		if err == nil {
			targetUnix = tu.UTC().Unix()
		}
	}
	if targetUnix == 0 {
		targetUnix = time.Now().UTC().Add(-10 * time.Minute).Unix()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	ctx = WithPriceDebug(ctx, t.Logf)

	v, kept, sumW, ok, err := GetTokenUSDPriceAtUnix(ctx, client, mint, targetUnix, 0, 1.5, 1e-6)
	if err != nil {
		t.Logf("[debug] final ERROR: %v (ok=%v kept=%d sumW=%.6f)", err, ok, kept, sumW)
		return
	}
	t.Logf("[debug] final PRICE: %.10f USD (kept=%d sumW=%.6f ok=%v)", v, kept, sumW, ok)
}
