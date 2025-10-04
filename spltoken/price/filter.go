package price

import (
	"context"
	"fmt"
	"math/big"

	"github.com/AlekSi/pointer"
	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// FilteredTx is the minimal payload your swap-decoder needs next.
// (We keep it lean to avoid SDK-type pitfalls.)
type FilteredTx struct {
	Slot            uint64
	BlockTime       int64               // unix seconds
	PerAccountDelta map[uint64]*big.Int // accountIndex -> (post - pre) for target mint
	TotalDelta      *big.Int            // sum of all deltas (sanity check)
}

// FilterTxsByMint scans a block at `slot` and returns only transactions
// that involve `targetMint` (SPL mint). It also computes per-account deltas.
func FilterTxsByMint(
	ctx context.Context,
	client *rpc.Client,
	slot uint64,
	targetMint solana.PublicKey,
) ([]*FilteredTx, error) {

	blk, err := client.GetBlockWithOpts(ctx, slot, &rpc.GetBlockOpts{
		// Encoding:   solana.EncodingBase64Zstd, // optional
		Commitment:         rpc.CommitmentFinalized,
		TransactionDetails: rpc.TransactionDetailsFull,
		Rewards:            pointer.ToBool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("getBlock(%d): %w", slot, err)
	}
	if blk == nil {
		return nil, nil
	}

	var out []*FilteredTx

	for _, txw := range blk.Transactions {
		meta := txw.Meta
		if meta == nil {
			continue
		}

		pre := make(map[uint64]*big.Int)
		post := make(map[uint64]*big.Int)

		parseAmt := func(s string) *big.Int {
			n := new(big.Int)
			n, _ = n.SetString(s, 10)
			if n == nil {
				return big.NewInt(0)
			}
			return n
		}

		// NOTE: In gagliardetto v1.14+, b.Mint is solana.PublicKey.
		for _, b := range meta.PreTokenBalances {
			if b.Mint.Equals(targetMint) {
				pre[uint64(b.AccountIndex)] = parseAmt(b.UiTokenAmount.Amount)
			}
		}
		for _, b := range meta.PostTokenBalances {
			if b.Mint.Equals(targetMint) {
				post[uint64(b.AccountIndex)] = parseAmt(b.UiTokenAmount.Amount)
			}
		}

		// Skip tx if it didn't touch the mint at all.
		if len(pre) == 0 && len(post) == 0 {
			continue
		}

		// Compute per-account deltas (post - pre) for target mint.
		perAcct := make(map[uint64]*big.Int)
		total := big.NewInt(0)
		seen := make(map[uint64]struct{})

		for idx := range pre {
			seen[idx] = struct{}{}
		}
		for idx := range post {
			seen[idx] = struct{}{}
		}
		for idx := range seen {
			preAmt := big.NewInt(0)
			if v, ok := pre[idx]; ok {
				preAmt = v
			}
			postAmt := big.NewInt(0)
			if v, ok := post[idx]; ok {
				postAmt = v
			}
			delta := new(big.Int).Sub(postAmt, preAmt)
			perAcct[idx] = delta
			total.Add(total, delta)
		}

		var blockTime int64
		if blk.BlockTime != nil {
			blockTime = int64(*blk.BlockTime) // cast from solana.UnixTimeSeconds
		}

		out = append(out, &FilteredTx{
			Slot:            slot,      // use the slot we queried
			BlockTime:       blockTime, // unix seconds
			PerAccountDelta: perAcct,
			TotalDelta:      total,
		})
	}

	return out, nil
}
