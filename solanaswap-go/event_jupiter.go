package solanaswapgo

import (
	"encoding/json"
	"fmt"

	ag_binary "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
)

type JupiterSwapEvent struct {
	Amm          solana.PublicKey
	InputMint    solana.PublicKey
	InputAmount  uint64
	OutputMint   solana.PublicKey
	OutputAmount uint64
}

type JupiterSwapEventData struct {
	JupiterSwapEvent
	InputMintDecimals  uint8
	OutputMintDecimals uint8
}

// Anchor event discriminator for Jupiter RouteV2 event
var JupiterRouteEventDiscriminator = [16]byte{228, 69, 165, 46, 81, 203, 154, 29, 64, 198, 205, 232, 38, 8, 113, 226}

// processJupiterSwaps tries 2 paths:
//  1. Parse the dedicated Jupiter Route event (preferred).
//  2. Fallback: if the event isn't present, scan the *same inner-instruction set*
//     and delegate to AMM decoders (Raydium/Orca/Meteora/Pump.fun) like a router.
func (p *Parser) processJupiterSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData
	foundRouteEvent := false

	// Try to capture the explicit RouteV2 event first.
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index != uint16(instructionIndex) {
			continue
		}
		for _, innerInstruction := range innerInstructionSet.Instructions {
			inst := p.convertRPCToSolanaInstruction(innerInstruction)
			if p.isJupiterRouteEventInstruction(inst) {
				eventData, err := p.parseJupiterRouteEventInstruction(inst)
				if err != nil {
					p.Log.Errorf("error processing Jupiter route event: %s", err)
					continue
				}
				if eventData != nil {
					foundRouteEvent = true
					swaps = append(swaps, SwapData{Type: JUPITER, Data: eventData})
				}
			}
		}
	}

	if foundRouteEvent {
		return swaps
	}

	// Fallback router behavior:
	// Jupiter routed swaps often execute as CPIs to the underlying AMMs.
	// Reuse our router logic to extract those legs from the same inner set.
	routerLegs := p.processRouterSwaps(instructionIndex)
	if len(routerLegs) > 0 {
		return routerLegs
	}

	// As a last resort, harvest TokenProgram transfers right under this route.
	for _, innerInstructionSet := range p.txMeta.InnerInstructions {
		if innerInstructionSet.Index != uint16(instructionIndex) {
			continue
		}
		for _, innerInstruction := range innerInstructionSet.Instructions {
			inst := p.convertRPCToSolanaInstruction(innerInstruction)
			switch {
			case p.isTransferCheck(inst):
				if tr := p.processTransferCheck(inst); tr != nil {
					swaps = append(swaps, SwapData{Type: UNKNOWN, Data: tr})
				}
			case p.isTransfer(inst):
				if tr := p.processTransfer(inst); tr != nil {
					swaps = append(swaps, SwapData{Type: UNKNOWN, Data: tr})
				}
			}
		}
	}
	return swaps
}

// containsDCAProgram checks if the transaction contains the Jupiter DCA program.
func (p *Parser) containsDCAProgram() bool {
	for _, accountKey := range p.allAccountKeys {
		if accountKey.Equals(JUPITER_DCA_PROGRAM_ID) {
			return true
		}
	}
	return false
}

func (p *Parser) parseJupiterRouteEventInstruction(instruction solana.CompiledInstruction) (*JupiterSwapEventData, error) {
	decodedBytes, err := base58.Decode(instruction.Data.String())
	if err != nil {
		return nil, fmt.Errorf("error decoding instruction data: %s", err)
	}
	if len(decodedBytes) < 16 {
		return nil, fmt.Errorf("jupiter event data too short: %d", len(decodedBytes))
	}

	decoder := ag_binary.NewBorshDecoder(decodedBytes[16:])
	jupSwapEvent, err := handleJupiterRouteEvent(decoder)
	if err != nil {
		return nil, fmt.Errorf("error decoding jupiter swap event: %s", err)
	}

	inputMintDecimals, exists := p.splDecimalsMap[jupSwapEvent.InputMint.String()]
	if !exists {
		inputMintDecimals = 0
	}
	outputMintDecimals, exists := p.splDecimalsMap[jupSwapEvent.OutputMint.String()]
	if !exists {
		outputMintDecimals = 0
	}

	return &JupiterSwapEventData{
		JupiterSwapEvent:   *jupSwapEvent,
		InputMintDecimals:  inputMintDecimals,
		OutputMintDecimals: outputMintDecimals,
	}, nil
}

func handleJupiterRouteEvent(decoder *ag_binary.Decoder) (*JupiterSwapEvent, error) {
	var event JupiterSwapEvent
	if err := decoder.Decode(&event); err != nil {
		return nil, fmt.Errorf("error unmarshaling JupiterSwapEvent: %s", err)
	}
	return &event, nil
}

func (p *Parser) extractSPLDecimals() error {
	mintToDecimals := make(map[string]uint8)

	for _, accountInfo := range p.txMeta.PostTokenBalances {
		if !accountInfo.Mint.IsZero() {
			mintAddress := accountInfo.Mint.String()
			mintToDecimals[mintAddress] = uint8(accountInfo.UiTokenAmount.Decimals)
		}
	}

	processInstruction := func(instr solana.CompiledInstruction) {
		if !p.allAccountKeys[instr.ProgramIDIndex].Equals(solana.TokenProgramID) {
			return
		}
		if len(instr.Data) == 0 || (instr.Data[0] != 3 && instr.Data[0] != 12) {
			return
		}
		if len(instr.Accounts) < 3 {
			return
		}
		mint := p.allAccountKeys[instr.Accounts[1]].String()
		if _, exists := mintToDecimals[mint]; !exists {
			mintToDecimals[mint] = 0
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

	// Native SOL (9 decimals)
	if _, exists := mintToDecimals[NATIVE_SOL_MINT_PROGRAM_ID.String()]; !exists {
		mintToDecimals[NATIVE_SOL_MINT_PROGRAM_ID.String()] = 9
	}

	p.splDecimalsMap = mintToDecimals
	return nil
}

// parseJupiterEvents aggregates all legs from Jupiter events into one SwapInfo.
func parseJupiterEvents(events []SwapData) (*SwapInfo, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("no events provided")
	}

	var (
		have              bool
		totalIn, totalOut uint64
		inMint, outMint   solana.PublicKey
		inDec, outDec     uint8
	)

	for _, event := range events {
		if event.Type != JUPITER {
			continue
		}

		var leg JupiterSwapEventData
		raw, err := json.Marshal(event.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal event data: %v", err)
		}
		if err := json.Unmarshal(raw, &leg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal Jupiter event data: %v", err)
		}

		if !have {
			inMint, outMint = leg.InputMint, leg.OutputMint
			inDec, outDec = leg.InputMintDecimals, leg.OutputMintDecimals
			have = true
		}

		// In practice, all legs share the same route-level input/output mints.
		// If a leg differs (edge case), we only sum amounts that match the
		// route-level mints we captured first.
		if leg.InputMint.Equals(inMint) {
			totalIn += leg.InputAmount
		}
		if leg.OutputMint.Equals(outMint) {
			totalOut += leg.OutputAmount
		}
	}

	if !have {
		return nil, fmt.Errorf("no valid Jupiter swaps found")
	}

	swapInfo := &SwapInfo{
		AMMs:             []string{string(JUPITER)},
		TokenInMint:      inMint,
		TokenInAmount:    totalIn,
		TokenInDecimals:  inDec,
		TokenOutMint:     outMint,
		TokenOutAmount:   totalOut,
		TokenOutDecimals: outDec,
	}
	return swapInfo, nil
}
