package price

import (
	"os"

	"github.com/gagliardetto/solana-go"
)

// Reads stablecoin mints from environment (no hardcoding).
// Expected keys in .env:
//
//	SOLANA_USDC_CONTRACT_ADDRESS
//	SOLANA_USDT_CONTRACT_ADDRESS
//
// If a value is missing or invalid, the corresponding key is returned as zero PublicKey.
func mustStableMintsFromEnv() (usdc solana.PublicKey, usdt solana.PublicKey) {
	usdcEnv := os.Getenv("SOLANA_USDC_CONTRACT_ADDRESS")
	usdtEnv := os.Getenv("SOLANA_USDT_CONTRACT_ADDRESS")

	if pk, err := solana.PublicKeyFromBase58(usdcEnv); err == nil {
		usdc = pk
	}
	if pk, err := solana.PublicKeyFromBase58(usdtEnv); err == nil {
		usdt = pk
	}
	return
}
