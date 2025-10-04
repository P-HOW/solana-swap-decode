package price

import (
	"context"
	"testing"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

func TestPricesAtSlot_371161375_DBONK(t *testing.T) {
	// Reuse .env loader and RPC picker from filter_test.go
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

	t.Logf("slot=%d mint=%s -> %d price point(s)", slot, mint, len(points))
	for i, pp := range points {
		// Pretty one-liner with signature & time
		t.Logf("[%d] %s", i, PrettyPrice(pp))

		// Explicit price prints:
		t.Logf("    price (SOL per %s): %.12f  (exact=%s)",
			mint.String(), pp.PriceFloat, pp.PriceSOLPerToken.RatString())

		// If you also want TOKEN per SOL (inverse), uncomment:
		// inv := new(big.Rat).Inv(pp.PriceSOLPerToken)
		// f, _ := inv.Float64()
		// t.Logf("    price (TOK per 1 SOL): %.6f (exact=%s)", f, inv.RatString())
	}

	// Optional assertion if you expect ≥1 price point:
	// if len(points) == 0 { t.Fatal("expected >=1 price point") }
}
