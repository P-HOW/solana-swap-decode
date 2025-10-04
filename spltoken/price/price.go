package price

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	solanaswapgo "github.com/franco-bianco/solanaswap-go/solanaswap-go"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// Wrapped SOL mint (Token-2022 wrapper is different; for classic SPL this is the one)
const WrappedSOL = "So11111111111111111111111111111111111111112"

// PricePoint is a single price observation derived from one swap.
type PricePoint struct {
	// Identity
	Signature string
	Slot      uint64
	BlockTime int64 // unix seconds, if available

	// Price in SOL per 1 token (normalized by decimals)
	// Example: 0.0123 means 1 TOKEN = 0.0123 SOL
	PriceSOLPerToken *big.Rat
	PriceFloat       float64 // convenience (rounded)

	// What we priced
	TargetMint solana.PublicKey
	SOLSideIn  bool // true if SOL was on input side (otherwise output side)

	// Minimal debugging breadcrumb
	TokenAmountBase uint64 // raw token amount (base units)
	SOLAmountBase   uint64 // raw SOL amount (lamports)
	TokenDecimals   int
	Note            string
}

// swapSummary is the minimal shape we need from your parser's "swapInfo".
type swapSummary struct {
	Signatures       []string `json:"Signatures"`
	TokenInMint      string   `json:"TokenInMint"`
	TokenInAmount    uint64   `json:"TokenInAmount"`
	TokenInDecimals  int      `json:"TokenInDecimals"`
	TokenOutMint     string   `json:"TokenOutMint"`
	TokenOutAmount   uint64   `json:"TokenOutAmount"`
	TokenOutDecimals int      `json:"TokenOutDecimals"`
	// Timestamp etc. are not strictly needed here
}

// GetPricesAtSlot fetches all swaps in `slot` that touched `targetMint` and
// returns price points (SOL per token). It ignores non-SOL pairs.
func GetPricesAtSlot(
	ctx context.Context,
	client *rpc.Client,
	slot uint64,
	targetMint solana.PublicKey,
) ([]PricePoint, error) {

	// 1) Fast pre-filter using token-balance deltas (from filter.go)
	filtered, err := FilterTxsByMint(ctx, client, slot, targetMint)
	if err != nil {
		return nil, fmt.Errorf("FilterTxsByMint: %w", err)
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	// 2) For each candidate tx, fetch and parse swap info; compute price if pair is (wSOL, targetMint)
	points := make([]PricePoint, 0, len(filtered))

	// Max tx version supported (0 for v0 and legacy)
	var maxTxVer uint64 = 0

	for _, ft := range filtered {
		// We need a signature to fetch the tx; if missing, skip.
		if ft.Signature == nil {
			// Best-effort: continue (should be rare since we already decoded once in FilterTxsByMint).
			continue
		}

		tx, err := client.GetTransaction(ctx, *ft.Signature, &rpc.GetTransactionOpts{
			Commitment:                     rpc.CommitmentConfirmed,
			MaxSupportedTransactionVersion: &maxTxVer,
		})
		if err != nil || tx == nil {
			// Not fatal: just skip this candidate
			continue
		}

		parser, err := solanaswapgo.NewTransactionParser(tx)
		if err != nil {
			// Not a parsable swap; skip
			continue
		}

		txData, err := parser.ParseTransaction()
		if err != nil {
			continue
		}

		swapInfo, err := parser.ProcessSwapData(txData)
		if err != nil || swapInfo == nil {
			// Not every tx is a swap
			continue
		}

		// Be robust: marshal/unmarshal to our local summary shape.
		js, err := json.Marshal(swapInfo)
		if err != nil {
			continue
		}
		var sum swapSummary
		dec := json.NewDecoder(strings.NewReader(string(js)))
		dec.UseNumber() // safety, though fields are typed
		if err := json.Unmarshal(js, &sum); err != nil {
			continue
		}

		// Compute if and only if one side is wSOL and the other is the target mint.
		targetStr := targetMint.String()
		hasSOLIn := strings.EqualFold(sum.TokenInMint, WrappedSOL)
		hasSOLOut := strings.EqualFold(sum.TokenOutMint, WrappedSOL)
		hasTargetIn := strings.EqualFold(sum.TokenInMint, targetStr)
		hasTargetOut := strings.EqualFold(sum.TokenOutMint, targetStr)

		var (
			solBase   uint64
			tokBase   uint64
			tokDec    int
			solInSide bool
			okPair    bool
		)

		switch {
		case hasSOLIn && hasTargetOut:
			solBase = sum.TokenInAmount  // lamports
			tokBase = sum.TokenOutAmount // base units
			tokDec = sum.TokenOutDecimals
			solInSide = true
			okPair = true
		case hasTargetIn && hasSOLOut:
			solBase = sum.TokenOutAmount // lamports
			tokBase = sum.TokenInAmount  // base units
			tokDec = sum.TokenInDecimals
			solInSide = false
			okPair = true
		default:
			okPair = false
		}

		if !okPair || tokBase == 0 {
			continue
		}

		// Normalize:
		//   SOL amount in SOL  = solBase / 1e9
		//   TOKEN amount       = tokBase / 10^tokDec
		// Price (SOL per token) = (solBase / 1e9) / (tokBase / 10^tokDec)
		//                       = (solBase * 10^tokDec) / (tokBase * 1e9)
		num := new(big.Int).Mul(new(big.Int).SetUint64(solBase), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tokDec)), nil))
		den := new(big.Int).Mul(new(big.Int).SetUint64(tokBase), big.NewInt(1_000_000_000)) // 1e9 lamports/SOL
		if den.Sign() == 0 {
			continue
		}
		r := new(big.Rat).SetFrac(num, den)

		// Convenience float (bounded; if overflow, fall back to 0)
		priceFloat, _ := new(big.Rat).Set(r).Float64()
		if math.IsInf(priceFloat, 0) || math.IsNaN(priceFloat) {
			priceFloat = 0
		}

		// Prefer per-tx time; else fall back to the block-time we already kept.
		bt := ft.BlockTime
		if tx.BlockTime != nil {
			bt = int64(*tx.BlockTime)
		}

		pp := PricePoint{
			Signature:        ft.Signature.String(),
			Slot:             slot,
			BlockTime:        bt,
			PriceSOLPerToken: r,
			PriceFloat:       priceFloat,
			TargetMint:       targetMint,
			SOLSideIn:        solInSide,
			TokenAmountBase:  tokBase,
			SOLAmountBase:    solBase,
			TokenDecimals:    tokDec,
			Note:             "derived from solanaswap-go swapInfo",
		}
		points = append(points, pp)
	}

	return points, nil
}

// PrettyPrice formats a PricePoint price with time and signature (helper).
func PrettyPrice(pp PricePoint) string {
	ts := time.Unix(pp.BlockTime, 0).UTC().Format(time.RFC3339)
	return fmt.Sprintf("sig=%s slot=%d time=%s price=%.10f SOL/%s (delta: SOL=%d lamports, TOK=%d base, dec=%d, solIn=%t)",
		pp.Signature, pp.Slot, ts, pp.PriceFloat, pp.TargetMint.String(), pp.SOLAmountBase, pp.TokenAmountBase, pp.TokenDecimals, pp.SOLSideIn)
}
