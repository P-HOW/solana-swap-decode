package price

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Auto-generated: timestamps where |observed-ground_truth|/ground_truth > 3%.
func Test_DebugPricingPath_Outliers(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env (SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}
	client := rpc.New(rpcURL)

	mintStr := os.Getenv("PRICE_DEBUG_MINT")
	if mintStr == "" {
		mintStr = "DezXAZ8z7PnrnRJjz3wXBoRgixCa6xjnB7YaB1pPB263" // BONK mainnet
	}
	mint := solana.MustPublicKeyFromBase58(mintStr)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	ctx = WithPriceDebug(ctx, t.Logf)

	samples := []int64{
		1757507160, // idx=9  obs=0.0001154381247188164 gt=0.0000231040498444210 diff=399.64%
		1757633340, // idx=11 obs=0.0000122568167890728 gt=0.0000245358498869126 diff=-50.05%
		1758415680, // idx=24 obs=0.0000463486843671906 gt=0.0000231683792035664 diff=100.05%
		1759062600, // idx=40 obs=13197.1453390098430 gt=0.0000185988814251862 diff=70956661418.03%
		1759317540, // idx=47 obs=0.0013364489336696532 gt=0.0000200765021101081 diff=6556.78%
		1759855320, // idx=55 obs=0.0000399028814363191 gt=0.0000199192710833862 diff=100.32%
		1759944060, // idx=57 obs=0.0000405526949984199 gt=0.0000202523898949093 diff=100.24%
		1760982240, // idx=75 obs=0.0000297605253175910 gt=0.0000148742919215737 diff=100.08%
		1761031560, // idx=76 obs=0.0000320658709130223 gt=0.0000144149263709142 diff=122.45%
		1761453420, // idx=85 obs=0.0000293498383961924 gt=0.0000147175324330384 diff=99.42%
		1761581460, // idx=87 obs=0.0000303041641197339 gt=0.0000151387009707842 diff=100.18%
		1761586560, // idx=88 obs=0.0007676312245247442 gt=0.0000153688077523508 diff=4894.74%
		1761871980, // idx=94 obs=0.0000266560542209114 gt=0.0000133325528459846 diff=99.93%
		1762108680, // idx=97 obs=0.0000276629921229437 gt=0.0000138197907487169 diff=100.17%
	}

	okCount, failCount := 0, 0
	for i, ts := range samples {
		t.Logf("[replay %d/%d] t=%d (%s)", i+1, len(samples), ts, time.Unix(ts, 0).UTC().Format(time.RFC3339))
		v, kept, sumW, ok, err := GetTokenUSDPriceAtUnix(ctx, client, mint, ts, 0, 1.5, 1e-6)
		if err != nil {
			t.Logf("[result] t=%d  price=0  ok=%v  kept=%d  sumW=%.6f  err=%v", ts, ok, kept, sumW, err)
			failCount++
			continue
		}
		t.Logf("[result] t=%d  price=%.16f  ok=%v  kept=%d  sumW=%.6f", ts, v, ok, kept, sumW)
		if ok {
			okCount++
		} else {
			failCount++
		}
	}
	t.Logf("[summary] samples=%d ok=%d fail=%d", len(samples), okCount, failCount)
}
