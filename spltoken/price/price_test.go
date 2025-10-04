package price

import (
	"context"
	"testing"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

func TestPricesAtSlot_371161375_DBONK(t *testing.T) {
	// Reuse helpers defined in filter_test.go
	loadDotEnvNearRepoRoot(t)

	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Fatalf("no RPC found in env (expected SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	slot := uint64(371161375)
	mint := solana.MustPublicKeyFromBase58("83kGGSggYGP2ZEEyvX54SkZR1kFn84RgGCDyptbDbonk")

	ctx := context.Background()
	points, err := GetPricesAtSlot(ctx, client, slot, mint)
	if err != nil {
		t.Fatalf("GetPricesAtSlot error: %v", err)
	}

	if len(points) == 0 {
		t.Logf("slot=%d mint=%s -> 0 price points (likely no SOL pair or swapInfo was nil for all candidates)", slot, mint)
		return
	}

	t.Logf("slot=%d mint=%s -> %d price point(s)", slot, mint, len(points))
	for i, pp := range points {
		t.Logf("[%d] %s", i, PrettyPrice(pp))
		t.Logf("    price (SOL per %s): %.12f  (exact=%s)",
			mint.String(), pp.PriceFloat, pp.PriceSOLPerToken.RatString())
	}
}
