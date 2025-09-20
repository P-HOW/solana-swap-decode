package solanaswapgo

import (
	"bytes"

	"github.com/gagliardetto/solana-go"
	"github.com/mr-tron/base58"
)

// isTokenProgram returns true for the original SPL Token program and Token-2022.
func (p *Parser) isTokenProgram(pk solana.PublicKey) bool {
	return pk.Equals(solana.TokenProgramID) || pk.Equals(solana.Token2022ProgramID)
}

// isTransfer checks if the instruction is a token transfer (Token v1).
func (p *Parser) isTransfer(instr solana.CompiledInstruction) bool {
	progID := p.allAccountKeys[instr.ProgramIDIndex]
	if !progID.Equals(solana.TokenProgramID) {
		return false
	}
	if len(instr.Accounts) < 3 || len(instr.Data) < 9 {
		return false
	}
	// Token Program "Transfer" (u8 = 3)
	if instr.Data[0] != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if int(instr.Accounts[i]) >= len(p.allAccountKeys) {
			return false
		}
	}
	return true
}

// isTransferCheck checks if the instruction is a token transfer check (Token v1 or Token-2022).
func (p *Parser) isTransferCheck(instr solana.CompiledInstruction) bool {
	progID := p.allAccountKeys[instr.ProgramIDIndex]
	if !p.isTokenProgram(progID) {
		return false
	}
	if len(instr.Accounts) < 4 || len(instr.Data) < 9 {
		return false
	}
	// Token Program "TransferChecked" (u8 = 12)
	if instr.Data[0] != 12 {
		return false
	}
	for i := 0; i < 4; i++ {
		if int(instr.Accounts[i]) >= len(p.allAccountKeys) {
			return false
		}
	}
	return true
}

func (p *Parser) isPumpFunTradeEventInstruction(inst solana.CompiledInstruction) bool {
	if !p.allAccountKeys[inst.ProgramIDIndex].Equals(PUMP_FUN_PROGRAM_ID) || len(inst.Data) == 0 {
		return false
	}
	enc := inst.Data.String()
	if len(enc) == 0 {
		return false
	}
	decodedBytes, err := base58.Decode(enc)
	if err != nil || len(decodedBytes) < 16 {
		return false
	}
	return bytes.Equal(decodedBytes[:16], PumpfunTradeEventDiscriminator[:])
}

func (p *Parser) isJupiterRouteEventInstruction(inst solana.CompiledInstruction) bool {
	if !p.allAccountKeys[inst.ProgramIDIndex].Equals(JUPITER_PROGRAM_ID) || len(inst.Data) == 0 {
		return false
	}
	enc := inst.Data.String()
	if len(enc) == 0 {
		return false
	}
	decodedBytes, err := base58.Decode(enc)
	if err != nil || len(decodedBytes) < 16 {
		return false
	}
	return bytes.Equal(decodedBytes[:16], JupiterRouteEventDiscriminator[:])
}
