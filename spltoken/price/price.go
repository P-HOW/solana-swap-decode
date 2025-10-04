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

// Wrapped SOL mint (classic SPL)
const WrappedSOL = "So11111111111111111111111111111111111111112"

// PricePoint is a single price observation derived from one swap.
type PricePoint struct {
	// Identity
	Signature string
	Slot      uint64
	BlockTime int64 // unix seconds, if available

	// Price in SOL per 1 token (normalized by decimals)
	PriceSOLPerToken *big.Rat
	PriceFloat       float64 // convenience

	// What we priced
	TargetMint solana.PublicKey
	SOLSideIn  bool // true if SOL was input side

	// Debug crumbs
	TokenAmountBase uint64 // token raw base units
	SOLAmountBase   uint64 // lamports
	TokenDecimals   int
	Note            string
}

// Minimal shape we need from parser's swapInfo.
type swapSummary struct {
	Signatures       []string `json:"Signatures"`
	TokenInMint      string   `json:"TokenInMint"`
	TokenInAmount    uint64   `json:"TokenInAmount"`
	TokenInDecimals  int      `json:"TokenInDecimals"`
	TokenOutMint     string   `json:"TokenOutMint"`
	TokenOutAmount   uint64   `json:"TokenOutAmount"`
	TokenOutDecimals int      `json:"TokenOutDecimals"`
}

// GetPricesAtSlot returns price points (SOL per token) for swaps in `slot`
// that touch `targetMint`. It safely handles cases where swapInfo == nil.
func GetPricesAtSlot(
	ctx context.Context,
	client *rpc.Client,
	slot uint64,
	targetMint solana.PublicKey,
) ([]PricePoint, error) {

	// 1) Pre-filter candidates in that block that change target-mint balances.
	filtered, err := FilterTxsByMint(ctx, client, slot, targetMint)
	if err != nil {
		return nil, fmt.Errorf("FilterTxsByMint: %w", err)
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	points := make([]PricePoint, 0, len(filtered))
	var maxTxVer uint64 = 0

	for _, ft := range filtered {
		if ft.Signature == nil {
			// Can't fetch the transaction without a signature; skip.
			continue
		}

		tx, err := client.GetTransaction(ctx, *ft.Signature, &rpc.GetTransactionOpts{
			Commitment:                     rpc.CommitmentConfirmed,
			MaxSupportedTransactionVersion: &maxTxVer,
		})
		if err != nil || tx == nil {
			// RPC error or missing; skip this candidate.
			continue
		}

		parser, err := solanaswapgo.NewTransactionParser(tx)
		if err != nil {
			// Not a parsable swap; skip.
			continue
		}

		txData, err := parser.ParseTransaction()
		if err != nil {
			continue
		}

		swapInfo, err := parser.ProcessSwapData(txData)
		if err != nil || swapInfo == nil {
			// IMPORTANT: nil swapInfo is expected sometimes; just skip quietly.
			continue
		}

		// Be defensive: marshal/unmarshal into our local struct.
		js, err := json.Marshal(swapInfo)
		if err != nil {
			continue
		}
		var sum swapSummary
		if err := json.Unmarshal(js, &sum); err != nil {
			continue
		}

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
			solBase = sum.TokenInAmount
			tokBase = sum.TokenOutAmount
			tokDec = sum.TokenOutDecimals
			solInSide = true
			okPair = true
		case hasTargetIn && hasSOLOut:
			solBase = sum.TokenOutAmount
			tokBase = sum.TokenInAmount
			tokDec = sum.TokenInDecimals
			solInSide = false
			okPair = true
		default:
			okPair = false
		}
		if !okPair || tokBase == 0 {
			continue
		}

		// Price (SOL per 1 token) = (solBase * 10^tokDec) / (tokBase * 1e9)
		num := new(big.Int).Mul(
			new(big.Int).SetUint64(solBase),
			new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(tokDec)), nil),
		)
		den := new(big.Int).Mul(
			new(big.Int).SetUint64(tokBase),
			big.NewInt(1_000_000_000), // lamports per SOL
		)
		if den.Sign() == 0 {
			continue
		}
		r := new(big.Rat).SetFrac(num, den)

		f64, _ := new(big.Rat).Set(r).Float64()
		if math.IsInf(f64, 0) || math.IsNaN(f64) {
			f64 = 0
		}

		bt := ft.BlockTime
		if tx.BlockTime != nil {
			bt = int64(*tx.BlockTime)
		}

		points = append(points, PricePoint{
			Signature:        ft.Signature.String(),
			Slot:             slot,
			BlockTime:        bt,
			PriceSOLPerToken: r,
			PriceFloat:       f64,
			TargetMint:       targetMint,
			SOLSideIn:        solInSide,
			TokenAmountBase:  tokBase,
			SOLAmountBase:    solBase,
			TokenDecimals:    tokDec,
			Note:             "derived from swapInfo; nil-safe",
		})
	}

	return points, nil
}

// PrettyPrice formats a PricePoint for logs.
func PrettyPrice(pp PricePoint) string {
	ts := time.Unix(pp.BlockTime, 0).UTC().Format(time.RFC3339)
	return fmt.Sprintf("sig=%s slot=%d time=%s price=%.10f SOL/%s (delta: SOL=%d lamports, TOK=%d base, dec=%d, solIn=%t)",
		pp.Signature, pp.Slot, ts, pp.PriceFloat, pp.TargetMint.String(),
		pp.SOLAmountBase, pp.TokenAmountBase, pp.TokenDecimals, pp.SOLSideIn)
}
