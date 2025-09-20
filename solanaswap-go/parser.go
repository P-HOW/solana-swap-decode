package solanaswapgo

import (
	"fmt"
	"sort"
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

func (p *Parser) ParseTransaction() ([]SwapData, error) {
	var parsedSwaps []SwapData

	skip := false
	for i, outerInstruction := range p.txInfo.Message.Instructions {
		progID := p.allAccountKeys[outerInstruction.ProgramIDIndex]
		switch {
		case progID.Equals(JUPITER_PROGRAM_ID):
			skip = true
			parsedSwaps = append(parsedSwaps, p.processJupiterSwaps(i)...)
		case progID.Equals(MOONSHOT_PROGRAM_ID):
			skip = true
			parsedSwaps = append(parsedSwaps, p.processMoonshotSwaps()...)
		case progID.Equals(BANANA_GUN_PROGRAM_ID) ||
			progID.Equals(MINTECH_PROGRAM_ID) ||
			progID.Equals(BLOOM_PROGRAM_ID) ||
			progID.Equals(NOVA_PROGRAM_ID) ||
			progID.Equals(MAESTRO_PROGRAM_ID):
			if innerSwaps := p.processRouterSwaps(i); len(innerSwaps) > 0 {
				parsedSwaps = append(parsedSwaps, innerSwaps...)
			}
		case progID.Equals(OKX_DEX_ROUTER_PROGRAM_ID):
			skip = true
			parsedSwaps = append(parsedSwaps, p.processOKXSwaps(i)...)
		}
	}
	if skip {
		return parsedSwaps, nil
	}

	for i, outerInstruction := range p.txInfo.Message.Instructions {
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
		case progID.Equals(METEORA_PROGRAM_ID) || progID.Equals(METEORA_POOLS_PROGRAM_ID) || progID.Equals(METEORA_DLMM_PROGRAM_ID):
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

	swapInfo := &SwapInfo{
		Signatures: p.txInfo.Signatures,
	}

	// Identify signer (fee payer by default; DCA uses index 2 in your code)
	if p.containsDCAProgram() {
		swapInfo.Signers = []solana.PublicKey{p.allAccountKeys[2]}
	} else {
		swapInfo.Signers = []solana.PublicKey{p.allAccountKeys[0]}
	}
	signer := swapInfo.Signers[0].String()

	// Partition by source
	jupiterSwaps := make([]SwapData, 0)
	pumpfunSwaps := make([]SwapData, 0)
	otherSwaps := make([]SwapData, 0)

	for _, sd := range swapDatas {
		switch sd.Type {
		case JUPITER:
			jupiterSwaps = append(jupiterSwaps, sd)
		case PUMP_FUN:
			pumpfunSwaps = append(pumpfunSwaps, sd)
		default:
			otherSwaps = append(otherSwaps, sd)
		}
	}

	// 1) Jupiter: already authoritative
	if len(jupiterSwaps) > 0 {
		jupiterInfo, err := parseJupiterEvents(jupiterSwaps)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Jupiter events: %w", err)
		}
		swapInfo.TokenInMint = jupiterInfo.TokenInMint
		swapInfo.TokenInAmount = jupiterInfo.TokenInAmount
		swapInfo.TokenInDecimals = jupiterInfo.TokenInDecimals
		swapInfo.TokenOutMint = jupiterInfo.TokenOutMint
		swapInfo.TokenOutAmount = jupiterInfo.TokenOutAmount
		swapInfo.TokenOutDecimals = jupiterInfo.TokenOutDecimals
		swapInfo.AMMs = jupiterInfo.AMMs
		return swapInfo, nil
	}

	// 2) Pump.fun route event has full details
	if len(pumpfunSwaps) > 0 {
		switch data := pumpfunSwaps[0].Data.(type) {
		case *PumpfunTradeEvent:
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
			swapInfo.AMMs = append(swapInfo.AMMs, string(pumpfunSwaps[0].Type))
			swapInfo.Timestamp = time.Unix(int64(data.Timestamp), 0)
			return swapInfo, nil
		default:
			otherSwaps = append(otherSwaps, pumpfunSwaps...)
		}
	}

	// 3) Generic AMMs (Meteora/Raydium/Orca/OKX-routed, etc.)
	if len(otherSwaps) > 0 {
		// Build set of user-owned SPL token accounts from pre+post balances.
		userTokenAccounts := make(map[string]bool)
		for _, b := range p.txMeta.PreTokenBalances {
			if b.Owner.String() == signer {
				userTokenAccounts[p.allAccountKeys[b.AccountIndex].String()] = true
			}
		}
		for _, b := range p.txMeta.PostTokenBalances {
			if b.Owner.String() == signer {
				userTokenAccounts[p.allAccountKeys[b.AccountIndex].String()] = true
			}
		}

		type move struct {
			mint        string
			amount      uint64
			decimals    uint8
			source      string
			destination string
			authority   string
		}

		var moves []move
		// Normalize all transfers
		for _, sd := range otherSwaps {
			switch d := sd.Data.(type) {
			case *TransferData:
				m := move{
					mint:        d.Mint,
					amount:      d.Info.Amount,
					decimals:    d.Decimals,
					source:      d.Info.Source,
					destination: d.Info.Destination,
					authority:   d.Info.Authority,
				}
				moves = append(moves, m)
			case *TransferCheck:
				amt, err := strconv.ParseUint(d.Info.TokenAmount.Amount, 10, 64)
				if err != nil {
					continue
				}
				m := move{
					mint:        d.Info.Mint,
					amount:      amt,
					decimals:    d.Info.TokenAmount.Decimals,
					source:      d.Info.Source,
					destination: d.Info.Destination,
					authority:   d.Info.Authority,
				}
				moves = append(moves, m)
			}
		}

		// Inputs:
		//   - Prefer transfers AUTHORIZED by signer
		//   - Also include transfers whose SOURCE is a signer-owned token account
		inputTotals := make(map[string]uint64)
		inputDecimals := make(map[string]uint8)
		for _, m := range moves {
			if m.mint == "" {
				continue
			}
			if m.authority == signer || userTokenAccounts[m.source] {
				inputTotals[m.mint] += m.amount
				if _, ok := inputDecimals[m.mint]; !ok {
					inputDecimals[m.mint] = m.decimals
				}
			}
		}

		// Outputs: transfers CREDITED to signer-owned token accounts
		outputTotals := make(map[string]uint64)
		outputDecimals := make(map[string]uint8)
		for _, m := range moves {
			if m.mint == "" {
				continue
			}
			if userTokenAccounts[m.destination] {
				outputTotals[m.mint] += m.amount
				if _, ok := outputDecimals[m.mint]; !ok {
					outputDecimals[m.mint] = m.decimals
				}
			}
		}

		// Choose dominant input/output mints (largest volume)
		var inputMint string
		var inputAmt uint64
		for mint, amt := range inputTotals {
			if amt > inputAmt {
				inputAmt = amt
				inputMint = mint
			}
		}
		var outputMint string
		var outputAmt uint64
		for mint, amt := range outputTotals {
			if amt > outputAmt {
				outputAmt = amt
				outputMint = mint
			}
		}

		// Fallback to old heuristic if needed
		if inputMint == "" || outputMint == "" {
			var uniqueTokens []TokenTransfer
			seenTokens := make(map[string]bool)
			for _, sd := range otherSwaps {
				tt := getTransferFromSwapData(sd)
				if tt != nil && !seenTokens[tt.mint] {
					uniqueTokens = append(uniqueTokens, *tt)
					seenTokens[tt.mint] = true
				}
			}
			if len(uniqueTokens) >= 2 {
				inputMint = uniqueTokens[0].mint
				for _, mv := range moves {
					if mv.mint == inputMint {
						inputAmt += mv.amount
					}
				}
				outputMint = uniqueTokens[len(uniqueTokens)-1].mint
				for _, mv := range moves {
					if mv.mint == outputMint {
						outputAmt += mv.amount
					}
				}
				if _, ok := inputDecimals[inputMint]; !ok {
					inputDecimals[inputMint] = p.splDecimalsMap[inputMint]
				}
				if _, ok := outputDecimals[outputMint]; !ok {
					outputDecimals[outputMint] = p.splDecimalsMap[outputMint]
				}
			}
		}

		// If still missing, pick any remaining totals
		if inputMint == "" {
			type kv struct {
				k string
				v uint64
			}
			var arr []kv
			for k, v := range inputTotals {
				arr = append(arr, kv{k, v})
			}
			sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
			if len(arr) > 0 {
				inputMint, inputAmt = arr[0].k, arr[0].v
			}
		}
		if outputMint == "" {
			type kv struct {
				k string
				v uint64
			}
			var arr []kv
			for k, v := range outputTotals {
				arr = append(arr, kv{k, v})
			}
			sort.Slice(arr, func(i, j int) bool { return arr[i].v > arr[j].v })
			if len(arr) > 0 {
				outputMint, outputAmt = arr[0].k, arr[0].v
			}
		}

		// >>> NEW: correct input amount using signer balance delta for SPL tokens
		if inputMint != "" && inputMint != NATIVE_SOL_MINT_PROGRAM_ID.String() {
			if d, err := p.getTokenBalanceChanges(solana.MustPublicKeyFromBase58(inputMint)); err == nil {
				// use absolute delta; for input it should be negative (spent)
				inputAmt = uint64(abs(d))
			}
		}
		// <<<

		if inputMint != "" && outputMint != "" {
			swapInfo.TokenInMint = solana.MustPublicKeyFromBase58(inputMint)
			swapInfo.TokenInAmount = inputAmt
			swapInfo.TokenInDecimals = inputDecimals[inputMint]
			swapInfo.TokenOutMint = solana.MustPublicKeyFromBase58(outputMint)
			swapInfo.TokenOutAmount = outputAmt
			swapInfo.TokenOutDecimals = outputDecimals[outputMint]

			seenAMMs := make(map[string]bool)
			for _, sd := range otherSwaps {
				if !seenAMMs[string(sd.Type)] {
					swapInfo.AMMs = append(swapInfo.AMMs, string(sd.Type))
					seenAMMs[string(sd.Type)] = true
				}
			}
			swapInfo.Timestamp = time.Now()
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
			progID.Equals(METEORA_DLMM_PROGRAM_ID)) && !processedProtocols[PROTOCOL_METEORA]:
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
