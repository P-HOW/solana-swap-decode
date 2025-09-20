package solanaswapgo

import (
	"encoding/binary"

	"github.com/gagliardetto/solana-go"
)

type TransferInfo struct {
	Amount      uint64 `json:"amount"`
	Authority   string `json:"authority"`
	Destination string `json:"destination"`
	Source      string `json:"source"`
}

type TransferData struct {
	Info     TransferInfo `json:"info"`
	Type     string       `json:"type"`
	Mint     string       `json:"mint"`
	Decimals uint8        `json:"decimals"`
}

type TokenInfo struct {
	Mint     string
	Decimals uint8
}

func (p *Parser) processRaydSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index == uint16(instructionIndex) {
			for _, innerInstruction := range innerInstructionSet.Instructions {
				switch {
				case p.isTransfer(p.convertRPCToSolanaInstruction(innerInstruction)):
					transfer := p.processTransfer(p.convertRPCToSolanaInstruction(innerInstruction))
					if transfer != nil {
						swaps = append(swaps, SwapData{Type: RAYDIUM, Data: transfer})
					}
				case p.isTransferCheck(p.convertRPCToSolanaInstruction(innerInstruction)):
					transfer := p.processTransferCheck(p.convertRPCToSolanaInstruction(innerInstruction))
					if transfer != nil {
						swaps = append(swaps, SwapData{Type: RAYDIUM, Data: transfer})
					}
				}
			}
		}
	}
	return swaps
}

func (p *Parser) processOrcaSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index == uint16(instructionIndex) {
			for _, innerInstruction := range innerInstructionSet.Instructions {
				if p.isTransfer(p.convertRPCToSolanaInstruction(innerInstruction)) {
					transfer := p.processTransfer(p.convertRPCToSolanaInstruction(innerInstruction))
					if transfer != nil {
						swaps = append(swaps, SwapData{Type: ORCA, Data: transfer})
					}
				}
			}
		}
	}
	return swaps
}

func (p *Parser) processTransfer(instr solana.CompiledInstruction) *TransferData {
	amount := binary.LittleEndian.Uint64(instr.Data[1:9])

	srcKey := p.allAccountKeys[instr.Accounts[0]].String()
	dstKey := p.allAccountKeys[instr.Accounts[1]].String()

	// Prefer destination mint (usual case), else fall back to source mint.
	mint := p.splTokenInfoMap[dstKey].Mint
	if mint == "" {
		mint = p.splTokenInfoMap[srcKey].Mint
	}

	td := &TransferData{
		Info: TransferInfo{
			Amount:      amount,
			Source:      srcKey,
			Destination: dstKey,
			Authority:   p.allAccountKeys[instr.Accounts[2]].String(),
		},
		Type:     "transfer",
		Mint:     mint,
		Decimals: 0,
	}

	// Fill decimals from the authoritative mint→decimals map when we know the mint.
	if td.Mint != "" {
		if d, ok := p.splDecimalsMap[td.Mint]; ok {
			td.Decimals = d
		}
	}

	if td.Mint == "" {
		td.Mint = "Unknown"
	}

	return td
}

// extractSPLTokenInfo builds token-account → (mint,decimals) using both PRE and POST
// balances, and also propagates mint on plain Transfer(3) when one side is known.
func (p *Parser) extractSPLTokenInfo() error {
	splTokenAddresses := make(map[string]TokenInfo)

	// Seed from PRE balances
	for _, accountInfo := range p.txMeta.PreTokenBalances {
		if !accountInfo.Mint.IsZero() {
			accountKey := p.allAccountKeys[accountInfo.AccountIndex].String()
			splTokenAddresses[accountKey] = TokenInfo{
				Mint:     accountInfo.Mint.String(),
				Decimals: accountInfo.UiTokenAmount.Decimals,
			}
		}
	}
	// Seed from POST balances
	for _, accountInfo := range p.txMeta.PostTokenBalances {
		if !accountInfo.Mint.IsZero() {
			accountKey := p.allAccountKeys[accountInfo.AccountIndex].String()
			splTokenAddresses[accountKey] = TokenInfo{
				Mint:     accountInfo.Mint.String(),
				Decimals: accountInfo.UiTokenAmount.Decimals,
			}
		}
	}

	processInstruction := func(instr solana.CompiledInstruction) {
		if !p.isTokenProgram(p.allAccountKeys[instr.ProgramIDIndex]) {
			return
		}
		if len(instr.Data) == 0 {
			return
		}

		op := instr.Data[0]

		// Ensure map entries exist
		if len(instr.Accounts) >= 2 {
			source := p.allAccountKeys[instr.Accounts[0]].String()
			destination := p.allAccountKeys[instr.Accounts[1]].String()
			if _, exists := splTokenAddresses[source]; !exists {
				splTokenAddresses[source] = TokenInfo{Mint: "", Decimals: 0}
			}
			if _, exists := splTokenAddresses[destination]; !exists {
				splTokenAddresses[destination] = TokenInfo{Mint: "", Decimals: 0}
			}

			// Backfill for TransferChecked(12): accounts=[src, mint, dst, ...]
			if op == 12 && len(instr.Accounts) >= 3 {
				mint := p.allAccountKeys[instr.Accounts[1]].String()
				if ti := splTokenAddresses[source]; ti.Mint == "" {
					splTokenAddresses[source] = TokenInfo{Mint: mint, Decimals: ti.Decimals}
				}
				if ti := splTokenAddresses[destination]; ti.Mint == "" {
					splTokenAddresses[destination] = TokenInfo{Mint: mint, Decimals: ti.Decimals}
				}
			}

			// NEW: Backfill for Transfer(3): both sides must be same mint; if
			// one side already known from pre/post, propagate to the other.
			if op == 3 {
				sInfo := splTokenAddresses[source]
				dInfo := splTokenAddresses[destination]
				switch {
				case sInfo.Mint != "" && dInfo.Mint == "":
					splTokenAddresses[destination] = TokenInfo{Mint: sInfo.Mint, Decimals: dInfo.Decimals}
				case dInfo.Mint != "" && sInfo.Mint == "":
					splTokenAddresses[source] = TokenInfo{Mint: dInfo.Mint, Decimals: sInfo.Decimals}
				}
			}
		}
	}

	for _, instr := range p.txInfo.Message.Instructions {
		processInstruction(instr)
	}
	for _, innerSet := range p.txMeta.InnerInstructions {
		for _, instr := range innerSet.Instructions {
			processInstruction(p.convertRPCToSolanaInstruction(instr))
		}
	}

	p.splTokenInfoMap = splTokenAddresses
	return nil
}
