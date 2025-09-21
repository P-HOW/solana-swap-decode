package solanaswapgo

import (
	"bytes"
	"fmt"
	"strconv"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
	"github.com/sirupsen/logrus"
)

const (
	PROTOCOL_RAYDIUM = "raydium"
	PROTOCOL_ORCA    = "orca"
	PROTOCOL_METEORA = "meteora"
	PROTOCOL_PUMPFUN = "pumpfun"
)

type TokenTransfer struct {
	mint     string
	amount   uint64
	decimals uint8
}

type Parser struct {
	txMeta          *rpc.TransactionMeta
	txInfo          *solana.Transaction
	allAccountKeys  solana.PublicKeySlice
	splTokenInfoMap map[string]TokenInfo
	splDecimalsMap  map[string]uint8
	Log             *logrus.Logger
}

func NewTransactionParser(tx *rpc.GetTransactionResult) (*Parser, error) {
	txInfo, err := tx.Transaction.GetTransaction()
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}
	return NewTransactionParserFromTransaction(txInfo, tx.Meta)
}

func NewTransactionParserFromTransaction(tx *solana.Transaction, txMeta *rpc.TransactionMeta) (*Parser, error) {
	allAccountKeys := append(tx.Message.AccountKeys, txMeta.LoadedAddresses.Writable...)
	allAccountKeys = append(allAccountKeys, txMeta.LoadedAddresses.ReadOnly...)

	log := logrus.New()
	log.SetFormatter(&logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	})

	parser := &Parser{
		txMeta:         txMeta,
		txInfo:         tx,
		allAccountKeys: allAccountKeys,
		Log:            log,
	}

	if err := parser.extractSPLTokenInfo(); err != nil {
		return nil, fmt.Errorf("failed to extract SPL Token Addresses: %w", err)
	}
	if err := parser.extractSPLDecimals(); err != nil {
		return nil, fmt.Errorf("failed to extract SPL decimals: %w", err)
	}
	return parser, nil
}

type SwapData struct {
	Type SwapType
	Data interface{}
}

// Jupiter is treated like a router only if we actually parse something under it.
func (p *Parser) ParseTransaction() ([]SwapData, error) {
	var parsedSwaps []SwapData

	skip := false
	for i := range p.txInfo.Message.Instructions {
		outerInstruction := p.txInfo.Message.Instructions[i]
		progID := p.allAccountKeys[outerInstruction.ProgramIDIndex]
		switch {
		case progID.Equals(JUPITER_PROGRAM_ID):
			jup := p.processJupiterSwaps(i)
			if len(jup) > 0 {
				parsedSwaps = append(parsedSwaps, jup...)
				skip = true // only skip if something was parsed under Jupiter
			}
		case progID.Equals(MOONSHOT_PROGRAM_ID):
			ms := p.processMoonshotSwaps()
			if len(ms) > 0 {
				parsedSwaps = append(parsedSwaps, ms...)
				skip = true
			}
		case progID.Equals(BANANA_GUN_PROGRAM_ID) ||
			progID.Equals(MINTECH_PROGRAM_ID) ||
			progID.Equals(BLOOM_PROGRAM_ID) ||
			progID.Equals(NOVA_PROGRAM_ID) ||
			progID.Equals(MAESTRO_PROGRAM_ID):
			if innerSwaps := p.processRouterSwaps(i); len(innerSwaps) > 0 {
				parsedSwaps = append(parsedSwaps, innerSwaps...)
			}
		case progID.Equals(OKX_DEX_ROUTER_PROGRAM_ID):
			okx := p.processOKXSwaps(i) // includes aggregate + legs
			if len(okx) > 0 {
				parsedSwaps = append(parsedSwaps, okx...)
				skip = true
			}
		}
	}
	if skip {
		return parsedSwaps, nil
	}

	// Fallback second pass: direct AMM outer instructions
	for i := range p.txInfo.Message.Instructions {
		outerInstruction := p.txInfo.Message.Instructions[i]
		progID := p.allAccountKeys[outerInstruction.ProgramIDIndex]
		switch {
		case progID.Equals(RAYDIUM_V4_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_CPMM_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_AMM_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_CONCENTRATED_LIQUIDITY_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_LAUNCHLAB_PROGRAM_ID) ||
			progID.Equals(solana.MustPublicKeyFromBase58("AP51WLiiqTdbZfgyRMs35PsZpdmLuPDdHYmrB23pEtMU")):
			parsedSwaps = append(parsedSwaps, p.processRaydSwaps(i)...)
		case progID.Equals(ORCA_PROGRAM_ID):
			parsedSwaps = append(parsedSwaps, p.processOrcaSwaps(i)...)
		case progID.Equals(METEORA_PROGRAM_ID) ||
			progID.Equals(METEORA_POOLS_PROGRAM_ID) ||
			progID.Equals(METEORA_DLMM_PROGRAM_ID) ||
			progID.Equals(METEORA_DBC_PROGRAM_ID) ||
			progID.Equals(METEORA_DAMM_V2_PROGRAM_ID): // include DAMM v2
			parsedSwaps = append(parsedSwaps, p.processMeteoraSwaps(i)...)
		case progID.Equals(PUMPFUN_AMM_PROGRAM_ID):
			parsedSwaps = append(parsedSwaps, p.processPumpfunAMMSwaps(i)...)
		case progID.Equals(PUMP_FUN_PROGRAM_ID) ||
			progID.Equals(solana.MustPublicKeyFromBase58("BSfD6SHZigAfDWSjzD5Q41jw8LmKwtmjskPH9XW1mrRW")):
			parsedSwaps = append(parsedSwaps, p.processPumpfunSwaps(i)...)
		}
	}

	return parsedSwaps, nil
}

type SwapInfo struct {
	Signers    []solana.PublicKey
	Signatures []solana.Signature
	AMMs       []string
	Timestamp  time.Time

	TokenInMint     solana.PublicKey
	TokenInAmount   uint64
	TokenInDecimals uint8

	TokenOutMint     solana.PublicKey
	TokenOutAmount   uint64
	TokenOutDecimals uint8
}

func (p *Parser) ProcessSwapData(swapDatas []SwapData) (*SwapInfo, error) {
	if len(swapDatas) == 0 {
		return nil, fmt.Errorf("no swap data provided")
	}

	swapInfo := &SwapInfo{Signatures: p.txInfo.Signatures}

	if p.containsDCAProgram() {
		swapInfo.Signers = []solana.PublicKey{p.allAccountKeys[2]}
	} else {
		swapInfo.Signers = []solana.PublicKey{p.allAccountKeys[0]}
	}
	signer := swapInfo.Signers[0]

	// Priorities: Jupiter events → OKX aggregate → Pumpfun events/discriminators → aggregate legs
	jupiterSwaps := make([]SwapData, 0)
	var okxAgg *OKXSwapEventData
	pumpfunSwaps := make([]SwapData, 0)
	otherSwaps := make([]SwapData, 0)

	for _, sd := range swapDatas {
		switch sd.Type {
		case JUPITER:
			jupiterSwaps = append(jupiterSwaps, sd)
		case OKX:
			if okxAgg == nil {
				if v, ok := sd.Data.(*OKXSwapEventData); ok {
					okxAgg = v
				}
			}
		case PUMP_FUN:
			pumpfunSwaps = append(pumpfunSwaps, sd)
		default:
			otherSwaps = append(otherSwaps, sd)
		}
	}

	// Preferred: Jupiter events
	if len(jupiterSwaps) > 0 {
		jupiterInfo, err := parseJupiterEvents(jupiterSwaps)
		if err == nil {
			swapInfo.TokenInMint = jupiterInfo.TokenInMint
			swapInfo.TokenInAmount = jupiterInfo.TokenInAmount
			swapInfo.TokenInDecimals = jupiterInfo.TokenInDecimals
			swapInfo.TokenOutMint = jupiterInfo.TokenOutMint
			swapInfo.TokenOutAmount = jupiterInfo.TokenOutAmount
			swapInfo.TokenOutDecimals = jupiterInfo.TokenOutDecimals
			swapInfo.AMMs = jupiterInfo.AMMs
			p.adjustOrderBySolDelta(swapInfo) // final sanity based on signer lamport delta
			return swapInfo, nil
		}
		// If Jupiter events failed to decode, fall back to aggregating legs below.
	}

	// OKX aggregate (authoritative router totals)
	if okxAgg != nil {
		swapInfo.TokenInMint = okxAgg.InputMint
		swapInfo.TokenInAmount = okxAgg.InputAmount
		swapInfo.TokenInDecimals = okxAgg.InputDecimals
		swapInfo.TokenOutMint = okxAgg.OutputMint
		swapInfo.TokenOutAmount = okxAgg.OutputAmount
		swapInfo.TokenOutDecimals = okxAgg.OutputDecimals
		swapInfo.AMMs = append(swapInfo.AMMs, string(OKX))
		swapInfo.Timestamp = time.Now()
		p.adjustOrderBySolDelta(swapInfo)
		return swapInfo, nil
	}

	// Pump.fun native event OR discriminator-based fallback
	if len(pumpfunSwaps) > 0 {
		// 1) Event path (unchanged)
		for _, sd := range pumpfunSwaps {
			if data, ok := sd.Data.(*PumpfunTradeEvent); ok && data != nil {
				if data.IsBuy {
					swapInfo.TokenInMint = NATIVE_SOL_MINT_PROGRAM_ID
					swapInfo.TokenInAmount = data.SolAmount
					swapInfo.TokenInDecimals = 9
					swapInfo.TokenOutMint = data.Mint
					swapInfo.TokenOutAmount = data.TokenAmount
					swapInfo.TokenOutDecimals = p.splDecimalsMap[data.Mint.String()]
				} else {
					swapInfo.TokenInMint = data.Mint
					swapInfo.TokenInAmount = data.TokenAmount
					swapInfo.TokenInDecimals = p.splDecimalsMap[data.Mint.String()]
					swapInfo.TokenOutMint = NATIVE_SOL_MINT_PROGRAM_ID
					swapInfo.TokenOutAmount = data.SolAmount
					swapInfo.TokenOutDecimals = 9
				}
				swapInfo.AMMs = append(swapInfo.AMMs, string(PUMP_FUN))
				swapInfo.Timestamp = time.Unix(int64(data.Timestamp), 0)
				p.adjustOrderBySolDelta(swapInfo)
				return swapInfo, nil
			}
		}

		// 2) Discriminator-based direction detection (backward-safe)
		if has, isBuy := p.detectPumpfunBuySell(); has {
			// Scan ALL inner transferChecked instructions in the TX.
			solMint := NATIVE_SOL_MINT_PROGRAM_ID.String()

			totalsAnyAuth := make(map[string]uint64)  // largest observed per mint (any authority)
			totalsBySigner := make(map[string]uint64) // sum where authority == signer (what signer *sent*)

			if p.txMeta != nil && p.txMeta.InnerInstructions != nil {
				for _, set := range p.txMeta.InnerInstructions {
					for _, ri := range set.Instructions {
						inst := p.convertRPCToSolanaInstruction(ri)
						if !p.isTransferCheck(inst) {
							continue
						}
						tc := p.processTransferCheck(inst)
						if tc == nil {
							continue
						}
						amt, err := strconv.ParseUint(tc.Info.TokenAmount.Amount, 10, 64)
						if err != nil {
							continue
						}
						m := tc.Info.Mint
						// Track largest seen per mint (main leg vs dust)
						if amt > totalsAnyAuth[m] {
							totalsAnyAuth[m] = amt
						}
						// What the signer authorized to move out
						if tc.Info.Authority == signer.String() {
							totalsBySigner[m] += amt
						}
					}
				}
			}

			if isBuy {
				// BUY: input = SOL debit from signer; output = largest non-SOL credit
				inAmt := totalsBySigner[solMint]
				var outMint string
				var outAmt uint64
				for m, a := range totalsAnyAuth {
					if m == solMint {
						continue
					}
					if a > outAmt {
						outAmt = a
						outMint = m
					}
				}
				if inAmt > 0 && outMint != "" && outAmt > 0 {
					swapInfo.TokenInMint = NATIVE_SOL_MINT_PROGRAM_ID
					swapInfo.TokenInAmount = inAmt
					swapInfo.TokenInDecimals = 9
					swapInfo.TokenOutMint = solana.MustPublicKeyFromBase58(outMint)
					swapInfo.TokenOutAmount = outAmt
					swapInfo.TokenOutDecimals = p.splDecimalsMap[outMint]
					swapInfo.AMMs = append(swapInfo.AMMs, string(PUMP_FUN))
					swapInfo.Timestamp = time.Now()
					p.adjustOrderBySolDelta(swapInfo)
					return swapInfo, nil
				}
			} else {
				// SELL: input = largest non-SOL the signer sent; output = largest SOL received
				var inMint string
				var inAmt uint64
				for m, a := range totalsBySigner {
					if m == solMint {
						continue
					}
					if a > inAmt {
						inAmt = a
						inMint = m
					}
				}
				outAmt := uint64(0)
				if any, ok := totalsAnyAuth[solMint]; ok {
					outAmt = any
				}
				if inMint != "" && inAmt > 0 && outAmt > 0 {
					swapInfo.TokenInMint = solana.MustPublicKeyFromBase58(inMint)
					swapInfo.TokenInAmount = inAmt
					swapInfo.TokenInDecimals = p.splDecimalsMap[inMint]
					swapInfo.TokenOutMint = NATIVE_SOL_MINT_PROGRAM_ID
					swapInfo.TokenOutAmount = outAmt
					swapInfo.TokenOutDecimals = 9
					swapInfo.AMMs = append(swapInfo.AMMs, string(PUMP_FUN))
					swapInfo.Timestamp = time.Now()
					p.adjustOrderBySolDelta(swapInfo)
					return swapInfo, nil
				}
			}
			// If we couldn't resolve, keep falling through to generic aggregation.
		}

		// If neither event nor discriminator path resolved, include Pump.fun legs (if any) into the generic path.
		otherSwaps = append(otherSwaps, pumpfunSwaps...)
	}

	// Aggregate legs (Raydium/Orca/Meteora/Pump.fun AMM etc.)
	if len(otherSwaps) > 0 {
		var uniqueTokens []TokenTransfer
		seenTokens := make(map[string]bool)

		for _, sd := range otherSwaps {
			if tr := getTransferFromSwapData(sd); tr != nil && !seenTokens[tr.mint] {
				uniqueTokens = append(uniqueTokens, *tr)
				seenTokens[tr.mint] = true
			}
		}

		if len(uniqueTokens) >= 2 {
			inputTransfer := uniqueTokens[0]
			outputTransfer := uniqueTokens[len(uniqueTokens)-1]

			seenInputs := make(map[string]bool)
			seenOutputs := make(map[string]bool)
			var totalInputAmount uint64
			var totalOutputAmount uint64

			for _, sd := range otherSwaps {
				tr := getTransferFromSwapData(sd)
				if tr == nil {
					continue
				}
				key := fmt.Sprintf("%d-%s", tr.amount, tr.mint)
				if tr.mint == inputTransfer.mint && !seenInputs[key] {
					totalInputAmount += tr.amount
					seenInputs[key] = true
				}
				if tr.mint == outputTransfer.mint && !seenOutputs[key] {
					totalOutputAmount += tr.amount
					seenOutputs[key] = true
				}
			}

			swapInfo.TokenInMint = solana.MustPublicKeyFromBase58(inputTransfer.mint)
			swapInfo.TokenInAmount = totalInputAmount
			swapInfo.TokenInDecimals = inputTransfer.decimals
			swapInfo.TokenOutMint = solana.MustPublicKeyFromBase58(outputTransfer.mint)
			swapInfo.TokenOutAmount = totalOutputAmount
			swapInfo.TokenOutDecimals = outputTransfer.decimals

			seenAMMs := make(map[string]bool)
			for _, sd := range otherSwaps {
				if !seenAMMs[string(sd.Type)] {
					swapInfo.AMMs = append(swapInfo.AMMs, string(sd.Type))
					seenAMMs[string(sd.Type)] = true
				}
			}

			swapInfo.Timestamp = time.Now()
			p.adjustOrderBySolDelta(swapInfo)
			return swapInfo, nil
		}
	}

	return nil, fmt.Errorf("no valid swaps found")
}

func getTransferFromSwapData(swapData SwapData) *TokenTransfer {
	switch data := swapData.Data.(type) {
	case *TransferData:
		return &TokenTransfer{
			mint:     data.Mint,
			amount:   data.Info.Amount,
			decimals: data.Decimals,
		}
	case *TransferCheck:
		amt, err := strconv.ParseUint(data.Info.TokenAmount.Amount, 10, 64)
		if err != nil {
			return nil
		}
		return &TokenTransfer{
			mint:     data.Info.Mint,
			amount:   amt,
			decimals: data.Info.TokenAmount.Decimals,
		}
	case *JupiterSwapEventData:
		return &TokenTransfer{
			mint:     data.InputMint.String(),
			amount:   data.InputAmount,
			decimals: data.InputMintDecimals,
		}
	case *OKXSwapEventData:
		return &TokenTransfer{
			mint:     data.InputMint.String(),
			amount:   data.InputAmount,
			decimals: data.InputDecimals,
		}
	}
	return nil
}

func (p *Parser) processRouterSwaps(instructionIndex int) []SwapData {
	var swaps []SwapData

	innerInstructions := p.getInnerInstructions(instructionIndex)
	if len(innerInstructions) == 0 {
		return swaps
	}

	processedProtocols := make(map[string]bool)

	for _, inner := range innerInstructions {
		progID := p.allAccountKeys[inner.ProgramIDIndex]

		switch {
		case (progID.Equals(RAYDIUM_V4_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_CPMM_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_AMM_PROGRAM_ID) ||
			progID.Equals(RAYDIUM_CONCENTRATED_LIQUIDITY_PROGRAM_ID)) && !processedProtocols[PROTOCOL_RAYDIUM]:
			processedProtocols[PROTOCOL_RAYDIUM] = true
			if raydSwaps := p.processRaydSwaps(instructionIndex); len(raydSwaps) > 0 {
				swaps = append(swaps, raydSwaps...)
			}

		case progID.Equals(ORCA_PROGRAM_ID) && !processedProtocols[PROTOCOL_ORCA]:
			processedProtocols[PROTOCOL_ORCA] = true
			if orcaSwaps := p.processOrcaSwaps(instructionIndex); len(orcaSwaps) > 0 {
				swaps = append(swaps, orcaSwaps...)
			}

		case (progID.Equals(METEORA_PROGRAM_ID) ||
			progID.Equals(METEORA_POOLS_PROGRAM_ID) ||
			progID.Equals(METEORA_DLMM_PROGRAM_ID) ||
			progID.Equals(METEORA_DBC_PROGRAM_ID) ||
			progID.Equals(METEORA_DAMM_V2_PROGRAM_ID)) && !processedProtocols[PROTOCOL_METEORA]:
			processedProtocols[PROTOCOL_METEORA] = true
			if meteoraSwaps := p.processMeteoraSwaps(instructionIndex); len(meteoraSwaps) > 0 {
				swaps = append(swaps, meteoraSwaps...)
			}

		case progID.Equals(PUMPFUN_AMM_PROGRAM_ID) && !processedProtocols[PROTOCOL_PUMPFUN]:
			processedProtocols[PROTOCOL_PUMPFUN] = true
			if pumpfunAMMSwaps := p.processPumpfunAMMSwaps(instructionIndex); len(pumpfunAMMSwaps) > 0 {
				swaps = append(swaps, pumpfunAMMSwaps...)
			}

		case (progID.Equals(PUMP_FUN_PROGRAM_ID) ||
			progID.Equals(solana.MustPublicKeyFromBase58("BSfD6SHZigAfDWSjzD5Q41jw8LmKwtmjskPH9XW1mrRW"))) && !processedProtocols[PROTOCOL_PUMPFUN]:
			processedProtocols[PROTOCOL_PUMPFUN] = true
			if pumpfunSwaps := p.processPumpfunSwaps(instructionIndex); len(pumpfunSwaps) > 0 {
				swaps = append(swaps, pumpfunSwaps...)
			}
		}
	}

	return swaps
}

func (p *Parser) getInnerInstructions(index int) []solana.CompiledInstruction {
	if p.txMeta == nil || p.txMeta.InnerInstructions == nil {
		return nil
	}

	for _, inner := range p.txMeta.InnerInstructions {
		if inner.Index == uint16(index) {
			result := make([]solana.CompiledInstruction, len(inner.Instructions))
			for i, inst := range inner.Instructions {
				result[i] = p.convertRPCToSolanaInstruction(inst)
			}
			return result
		}
	}

	return nil
}

// ---- Pump.fun BUY/SELL discriminator detector (from pump.json) ----

var pumpfunBuyDisc = []byte{102, 6, 61, 18, 1, 218, 235, 234}     // "buy"
var pumpfunSellDisc = []byte{51, 230, 133, 164, 1, 127, 131, 173} // "sell"

// detectPumpfunBuySell scans outer instructions for Pump.fun and returns (found, isBuy)
func (p *Parser) detectPumpfunBuySell() (bool, bool) {
	for _, ci := range p.txInfo.Message.Instructions {
		progID := p.allAccountKeys[ci.ProgramIDIndex]
		if !(progID.Equals(PUMP_FUN_PROGRAM_ID) || progID.Equals(solana.MustPublicKeyFromBase58("BSfD6SHZigAfDWSjzD5Q41jw8LmKwtmjskPH9XW1mrRW"))) {
			continue
		}
		data := ci.Data
		if len(data) >= 8 {
			prefix := data[:8]
			if bytes.Equal(prefix, pumpfunBuyDisc) {
				return true, true
			}
			if bytes.Equal(prefix, pumpfunSellDisc) {
				return true, false
			}
		}
	}
	return false, false
}

// ---- SOL flow sanity (final direction fix) ----

// lamportDeltaFor returns post-pre lamports for the given pubkey from the tx meta.
// Only covers message account keys (as exposed by Pre/PostBalances).
func (p *Parser) lamportDeltaFor(pub solana.PublicKey) (int64, bool) {
	if p.txMeta == nil {
		return 0, false
	}
	msgKeys := p.txInfo.Message.AccountKeys
	if len(p.txMeta.PreBalances) != len(msgKeys) || len(p.txMeta.PostBalances) != len(msgKeys) {
		return 0, false
	}
	for i, k := range msgKeys {
		if k.Equals(pub) {
			pre := p.txMeta.PreBalances[i]
			post := p.txMeta.PostBalances[i]
			return int64(post) - int64(pre), true
		}
	}
	return 0, false
}

func (p *Parser) adjustOrderBySolDelta(si *SwapInfo) {
	solMint := NATIVE_SOL_MINT_PROGRAM_ID
	// Only act if SOL is one side
	if !(si.TokenInMint.Equals(solMint) || si.TokenOutMint.Equals(solMint)) {
		return
	}
	// Need signer lamport delta
	if len(si.Signers) == 0 {
		return
	}
	delta, ok := p.lamportDeltaFor(si.Signers[0])
	if !ok || delta == 0 {
		// Can't determine or no net change: do nothing
		return
	}
	// delta < 0 means signer paid SOL (BUY) ⇒ SOL must be TokenIn
	// delta > 0 means signer received SOL (SELL) ⇒ SOL must be TokenOut
	if delta < 0 && si.TokenOutMint.Equals(solMint) {
		p.swapInOut(si)
	} else if delta > 0 && si.TokenInMint.Equals(solMint) {
		p.swapInOut(si)
	}
}

func (p *Parser) swapInOut(si *SwapInfo) {
	si.TokenInMint, si.TokenOutMint = si.TokenOutMint, si.TokenInMint
	si.TokenInAmount, si.TokenOutAmount = si.TokenOutAmount, si.TokenInAmount
	si.TokenInDecimals, si.TokenOutDecimals = si.TokenOutDecimals, si.TokenInDecimals
}
