package price

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
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
	PriceFloat       float64 // convenience (SOL per token)

	// USD price per 1 token (computed when possible; 0 if not available)
	PriceUSD float64

	// What we priced
	TargetMint solana.PublicKey
	SOLSideIn  bool // true if SOL was input side (only meaningful for SOL pairs)

	// Base/counter leg information (the asset paired with the target token)
	BaseMint       solana.PublicKey
	BaseIsSOL      bool
	BaseIsStable   bool    // USDC/USDT
	BaseAmountRaw  uint64  // raw base units (lamports for SOL, base units for stable)
	BaseDecimals   int     // usually 9 for SOL, 6 for stables (but not hardcoded)
	TargetQtyFloat float64 // target token quantity in UI units used for pricing

	// Debug crumbs
	TokenAmountBase uint64 // token raw base units (legacy; kept for compatibility)
	SOLAmountBase   uint64 // lamports (legacy; kept for compatibility)
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

// small cache for SOL/USD minute-close lookups during a GetPricesAtSlot call
type solUSDCacher struct {
	mu sync.Mutex
	m  map[int64]float64 // key = minuteStartUnix (sec), value = price
}

func (c *solUSDCacher) getAtUnix(ctx context.Context, tUnix int64) (float64, error) {
	minute := time.Unix(tUnix, 0).UTC().Truncate(time.Minute).Unix()
	c.mu.Lock()
	if c.m == nil {
		c.m = make(map[int64]float64)
	}
	if v, ok := c.m[minute]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	px, err := GetSOLPriceAtTime(ctx, time.Unix(minute, 0).UTC())
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	c.m[minute] = px
	c.mu.Unlock()
	return px, nil
}

// GetPricesAtSlot returns price points for swaps in `slot` that touch `targetMint`.
// It supports pricing from pairs with SOL, USDC, or USDT as the counter asset.
// PriceSOLPerToken and PriceUSD will be filled when derivable; otherwise PriceUSD=0.
func GetPricesAtSlot(
	ctx context.Context,
	client *rpc.Client,
	slot uint64,
	targetMint solana.PublicKey,
) ([]PricePoint, error) {

	// 1) Pre-filter candidates that change target-mint balances.
	filtered, err := FilterTxsByMint(ctx, client, slot, targetMint)
	if err != nil {
		return nil, fmt.Errorf("FilterTxsByMint: %w", err)
	}
	if len(filtered) == 0 {
		return nil, nil
	}

	usdcMint, usdtMint := mustStableMintsFromEnv()
	points := make([]PricePoint, 0, len(filtered))
	var maxTxVer uint64 = 0
	cache := &solUSDCacher{}

	for _, ft := range filtered {
		if ft.Signature == nil {
			continue
		}

		tx, err := client.GetTransaction(ctx, *ft.Signature, &rpc.GetTransactionOpts{
			Commitment:                     rpc.CommitmentConfirmed,
			MaxSupportedTransactionVersion: &maxTxVer,
		})
		if err != nil || tx == nil {
			continue
		}

		parser, err := solanaswapgo.NewTransactionParser(tx)
		if err != nil {
			continue
		}

		txData, err := parser.ParseTransaction()
		if err != nil {
			continue
		}

		swapInfo, err := parser.ProcessSwapData(txData)
		if err != nil || swapInfo == nil {
			continue
		}

		js, err := json.Marshal(swapInfo)
		if err != nil {
			continue
		}
		var sum swapSummary
		if err := json.Unmarshal(js, &sum); err != nil {
			continue
		}

		bt := ft.BlockTime
		if tx.BlockTime != nil {
			bt = int64(*tx.BlockTime)
		}

		// Normalize mints
		targetStr := targetMint.String()
		inMint := sum.TokenInMint
		outMint := sum.TokenOutMint

		// Identify which leg is the target and which is the counter/base
		type leg struct {
			mint     string
			amount   uint64
			decimals int
		}
		var target leg
		var counter leg
		switch {
		case strings.EqualFold(inMint, targetStr):
			target = leg{mint: inMint, amount: sum.TokenInAmount, decimals: sum.TokenInDecimals}
			counter = leg{mint: outMint, amount: sum.TokenOutAmount, decimals: sum.TokenOutDecimals}
		case strings.EqualFold(outMint, targetStr):
			target = leg{mint: outMint, amount: sum.TokenOutAmount, decimals: sum.TokenOutDecimals}
			counter = leg{mint: inMint, amount: sum.TokenInAmount, decimals: sum.TokenInDecimals}
		default:
			// Not a direct pair touching the target as in/out (shouldn't happen due to filter); skip defensively.
			continue
		}

		// Determine counter class (SOL vs stable vs other)
		isSOL := strings.EqualFold(counter.mint, WrappedSOL)
		isUSDC := usdcMint.String() != "" && strings.EqualFold(counter.mint, usdcMint.String())
		isUSDT := usdtMint.String() != "" && strings.EqualFold(counter.mint, usdtMint.String())
		isStable := isUSDC || isUSDT

		// Compute token qty (UI units)
		tokQty := new(big.Rat).SetFrac(
			new(big.Int).SetUint64(target.amount),
			new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(target.decimals)), nil),
		)
		tokQtyF, _ := new(big.Rat).Set(tokQty).Float64()
		if tokQtyF <= 0 {
			continue
		}

		// Compute SOL-per-token when counter is SOL (for backward compatibility fields)
		var priceSOL *big.Rat
		var priceSOLFloat float64
		var solBase uint64
		if isSOL {
			// (SOL per token) = (counter.lamports / 1e9) / tokQty
			lamports := new(big.Rat).SetFrac(
				new(big.Int).SetUint64(counter.amount),
				big.NewInt(1_000_000_000),
			)
			priceSOL = new(big.Rat).Quo(lamports, tokQty)
			priceSOLFloat, _ = new(big.Rat).Set(priceSOL).Float64()
			solBase = counter.amount
		}

		// Compute USD price per token (supports SOL or stable counter only)
		var priceUSD float64
		switch {
		case isStable:
			// USD per token = (counter.amount / 10^counter.decimals) / tokQty
			counterF := new(big.Rat).SetFrac(
				new(big.Int).SetUint64(counter.amount),
				new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(counter.decimals)), nil),
			)
			tmp := new(big.Rat).Quo(counterF, tokQty)
			priceUSD, _ = tmp.Float64()
		case isSOL:
			// need SOL/USD for that minute
			solUSD, err := cache.getAtUnix(ctx, bt)
			if err != nil || solUSD <= 0 {
				// cannot compute USD price; keep as zero
				break
			}
			// USD per token = (SOL per token) * (SOL/USD)
			if priceSOL == nil {
				lamports := new(big.Rat).SetFrac(
					new(big.Int).SetUint64(counter.amount),
					big.NewInt(1_000_000_000),
				)
				priceSOL = new(big.Rat).Quo(lamports, tokQty)
			}
			ps, _ := new(big.Rat).Set(priceSOL).Float64()
			priceUSD = ps * solUSD
		default:
			// unsupported counter (neither SOL nor stable); skip pricing
			continue
		}

		// Derive SOL-only legacy fields (set to zero for non-SOL pairs)
		var priceSOLRat *big.Rat
		var priceSOLF float64
		if isSOL && priceSOL != nil {
			priceSOLRat = priceSOL
			priceSOLF = priceSOLFloat
		} else {
			priceSOLRat = new(big.Rat).SetInt64(0)
			priceSOLF = 0
		}

		points = append(points, PricePoint{
			Signature:        ft.Signature.String(),
			Slot:             slot,
			BlockTime:        bt,
			PriceSOLPerToken: priceSOLRat,
			PriceFloat:       priceSOLF,
			PriceUSD:         priceUSD,

			TargetMint: targetMint,
			SOLSideIn:  strings.EqualFold(sum.TokenInMint, WrappedSOL), // best-effort

			BaseMint:       mustPubkey(counter.mint),
			BaseIsSOL:      isSOL,
			BaseIsStable:   isStable,
			BaseAmountRaw:  counter.amount,
			BaseDecimals:   counter.decimals,
			TargetQtyFloat: tokQtyF,

			// legacy crumbs
			TokenAmountBase: target.amount,
			SOLAmountBase:   solBase,
			TokenDecimals:   target.decimals,
			Note:            "derived from swapInfo; supports SOL/USDC/USDT counters; USD computed",
		})
	}

	return points, nil
}

func mustPubkey(s string) solana.PublicKey {
	pk, err := solana.PublicKeyFromBase58(s)
	if err != nil {
		return solana.PublicKey{}
	}
	return pk
}

// PrettyPrice formats a PricePoint for logs.
func PrettyPrice(pp PricePoint) string {
	ts := time.Unix(pp.BlockTime, 0).UTC().Format(time.RFC3339)
	return fmt.Sprintf("sig=%s slot=%d time=%s priceSOL=%.10f SOL/%s priceUSD=%.10f USD/%s (base: %s amtRaw=%d dec=%d)",
		pp.Signature, pp.Slot, ts, pp.PriceFloat, pp.TargetMint.String(), pp.PriceUSD, pp.TargetMint.String(),
		pp.BaseMint.String(), pp.BaseAmountRaw, pp.BaseDecimals)
}
