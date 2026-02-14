package builder

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"

	"github.com/gagliardetto/solana-go"
	associatedtokenaccount "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
	container "github.com/thehyperflames/dicontainer-go"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/adapters/blockchain"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/domain"
	"github.com/thehyperflames/hyperion-runtime/internal/aggregator/services/market"
	"github.com/thehyperflames/hyperion-runtime/internal/common"
	"github.com/thehyperflames/hyperion-runtime/internal/config"
	hyperion_ag "github.com/thehyperflames/hyperion_ag_go/generated"
)

var (
	ErrMissingPoolData   = errors.New("pool is missing required data for swap")
	ErrInvalidUserWallet = errors.New("invalid user wallet address")
	ErrBuildFailed       = errors.New("failed to build swap transaction")
	ErrNoPoolFound       = errors.New("pool not found")
	ErrRouteTooLarge     = errors.New("route requires too many accounts for a single transaction")
)

const MaxHops = 4

const BUILDER_SERVICE_NAME = "BuilderService"

type BuilderService struct {
	container.BaseDIInstance

	rpcClient      *rpc.Client
	marketSvc      *market.Service
	blockhashCache *blockchain.BlockhashCacheService
	lutManager     *LUTManager
}

func (svc *BuilderService) ID() string {
	return BUILDER_SERVICE_NAME
}

func (svc *BuilderService) Configure(c container.IContainer) error {
	rpcConfig := c.GetConfig(config.RPC_CONFIG_KEY).(*config.RPCConfig)
	svc.rpcClient = rpc.New(rpcConfig.RPCUrl)
	svc.marketSvc = c.Instance(market.ServiceName).(*market.Service)
	svc.blockhashCache = c.Instance(blockchain.BLOCKHASH_CACHE_SERVICE).(*blockchain.BlockhashCacheService)

	lutConfig := c.GetConfig(config.LUT_CONFIG_KEY).(*config.LUTConfig)
	lutAddresses := make([]solana.PublicKey, 0, len(lutConfig.Addresses))
	for _, addr := range lutConfig.Addresses {
		pk, err := solana.PublicKeyFromBase58(addr)
		if err != nil {
			return fmt.Errorf("invalid LUT address %q: %w", addr, err)
		}
		lutAddresses = append(lutAddresses, pk)
	}
	svc.lutManager = NewLUTManager(svc.rpcClient, lutAddresses, lutConfig.RefreshInterval)
	return nil
}

func (svc *BuilderService) Start() error {
	svc.lutManager.Start(context.Background())
	return nil
}

// buildTransaction creates a V0 transaction with Address Lookup Tables.
// Even when LUTs are empty, this creates a valid v0 transaction.
func (svc *BuilderService) buildTransaction(instructions []solana.Instruction, blockhash solana.Hash, payer solana.PublicKey) (*solana.Transaction, error) {
	addressTables := svc.lutManager.GetAddressTables()
	return solana.NewTransaction(
		instructions,
		blockhash,
		solana.TransactionPayer(payer),
		solana.TransactionAddressTables(addressTables),
	)
}

// GetAddressTables returns the cached Address Lookup Tables for v0 transactions.
// This method allows BuilderService to be used as a LUTManager.
func (svc *BuilderService) GetAddressTables() map[solana.PublicKey]solana.PublicKeySlice {
	return svc.lutManager.GetAddressTables()
}

// TODO: BackBunner
func (svc *BuilderService) BuildAndSignSessionSwapTransaction(
	ctx context.Context,
	req *domain.SwapRequest,
	quote *domain.QuoteResult,
	sessionKey solana.PrivateKey,
	feePayer solana.PrivateKey,
) (*domain.SwapResponse, error) {
	if req.UserWallet.IsZero() {
		return nil, ErrInvalidUserWallet
	}

	exactIn := req.SwapMode == "ExactIn"

	// Use provided quote
	pool := quote.Pool
	if pool == nil {
		return nil, ErrNoPoolFound
	}

	clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || clmmData == nil || clmmData.ParsedVortex == nil {
		return nil, ErrMissingPoolData
	}

	slippageBps := req.SlippageBps
	if slippageBps == 0 {
		slippageBps = 50
	}

	var otherAmountThreshold uint64
	var minAmountOut, maxAmountIn string

	if exactIn {
		threshold := new(big.Int).Mul(quote.AmountOut, big.NewInt(int64(10000-slippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		otherAmountThreshold = threshold.Uint64()
		minAmountOut = threshold.String()
	} else {
		threshold := new(big.Int).Mul(quote.AmountIn, big.NewInt(int64(10000+slippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		otherAmountThreshold = threshold.Uint64()
		maxAmountIn = threshold.String()
	}

	// Derive user's ATAs
	tokenProgramA, err := svc.marketSvc.GetMintTokenProgram(ctx, pool.TokenMintA)
	if err != nil {
		return nil, err
	}
	tokenProgramB, err := svc.marketSvc.GetMintTokenProgram(ctx, pool.TokenMintB)
	if err != nil {
		return nil, err
	}

	userTokenAccountA, _, err := GetATAAddressForMint(req.UserWallet, pool.TokenMintA, tokenProgramA)
	if err != nil {
		return nil, fmt.Errorf("failed to derive ATA A: %w", err)
	}
	userTokenAccountB, _, err := GetATAAddressForMint(req.UserWallet, pool.TokenMintB, tokenProgramB)
	if err != nil {
		return nil, fmt.Errorf("failed to derive ATA B: %w", err)
	}

	// Derive PDAs
	oracle, _, err := GetOraclePDA(pool.Address)
	if err != nil {
		return nil, err
	}

	tickArrayPDAs, err := GetCachedTickArrayPDAs(
		pool.Address,
		clmmData.CurrentTickIndex,
		clmmData.TickSpacing,
		quote.AToB,
	)
	if err != nil {
		return nil, err
	}

	sessionAccountPubkey := sessionKey.PublicKey()

	var amount uint64
	if exactIn {
		amount = quote.AmountIn.Uint64()
	} else {
		amount = quote.AmountOut.Uint64()
	}

	var userSourceATA, userDestATA solana.PublicKey
	if quote.AToB {
		userSourceATA = userTokenAccountA
		userDestATA = userTokenAccountB
	} else {
		userSourceATA = userTokenAccountB
		userDestATA = userTokenAccountA
	}

	remainingAccounts := []*solana.AccountMeta{
		{PublicKey: userSourceATA, IsSigner: false, IsWritable: true},
		{PublicKey: userDestATA, IsSigner: false, IsWritable: true},
		{PublicKey: pool.Address, IsSigner: false, IsWritable: true},
		{PublicKey: pool.TokenVaultA, IsSigner: false, IsWritable: true},
		{PublicKey: pool.TokenVaultB, IsSigner: false, IsWritable: true},
		{PublicKey: tickArrayPDAs[0], IsSigner: false, IsWritable: true},
		{PublicKey: tickArrayPDAs[1], IsSigner: false, IsWritable: true},
		{PublicKey: tickArrayPDAs[2], IsSigner: false, IsWritable: true},
		{PublicKey: oracle, IsSigner: false, IsWritable: false},
	}

	// Add Fee Recipient ATA (if fee collection is enabled)
	var outputMint solana.PublicKey
	var outputTokenProgram solana.PublicKey
	if quote.AToB {
		outputMint = pool.TokenMintB
		outputTokenProgram = tokenProgramB
	} else {
		outputMint = pool.TokenMintA
		outputTokenProgram = tokenProgramA
	}

	feeRecipientATA, _, err := GetATAAddressForMint(FeeRecipient, outputMint, outputTokenProgram)
	if err != nil {
		return nil, fmt.Errorf("failed to derive fee recipient ATA: %w", err)
	}

	remainingAccounts = append(remainingAccounts, &solana.AccountMeta{
		PublicKey:  feeRecipientATA,
		IsSigner:   false,
		IsWritable: true,
	})

	var swapMode hyperion_ag.SwapMode
	if exactIn {
		swapMode = hyperion_ag.SwapModeExactIn
	} else {
		swapMode = hyperion_ag.SwapModeExactOut
	}
	routePlan := []hyperion_ag.RoutePlanStep{
		{
			Swap: &hyperion_ag.Swap{
				Value: hyperion_ag.SwapVortexTuple{
					AToB:     quote.AToB,
					SwapMode: swapMode,
				},
			},
			Percent:     100,
			InputIndex:  0,
			OutputIndex: 1,
		},
	}

	routeWithSessionIx := hyperion_ag.NewRouteWithSessionInstructionBuilder().
		SetNumTokenAccounts(2).
		SetRoutePlan(routePlan).
		SetInAmount(amount).
		SetQuotedOutAmount(otherAmountThreshold).
		SetSlippageBps(slippageBps).
		SetSimulate(false).
		SetTokenProgramAccount(common.TokenProgramID).
		SetSessionAccount(sessionAccountPubkey).
		SetProtocolConfigAccount(protocolConfigPDA).
		SetVortexProgramAccount(VortexProgramID).
		SetProgramSignerAccount(programSignerPDA)

	for _, acc := range remainingAccounts {
		routeWithSessionIx.Append(acc)
	}

	instruction, err := routeWithSessionIx.ValidateAndBuild()
	if err != nil {
		return nil, fmt.Errorf("failed to build route_with_session instruction: %w", err)
	}

	blockhashRes, err := svc.rpcClient.GetLatestBlockhash(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return nil, err
	}

	payer := feePayer.PublicKey()

	// Create Fee Recipient ATA
	createFeeATA := CreateATAInstructionForMint(payer, FeeRecipient, outputMint, outputTokenProgram)

	tx, err := svc.buildTransaction([]solana.Instruction{createFeeATA, instruction}, blockhashRes.Value.Blockhash, payer)
	if err != nil {
		return nil, err
	}

	_, err = tx.Sign(
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(sessionKey.PublicKey()) {
				return &sessionKey
			}
			if key.Equals(feePayer.PublicKey()) {
				return &feePayer
			}
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}

	sig, err := svc.rpcClient.SendTransaction(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to submit transaction: %w", err)
	}

	return &domain.SwapResponse{
		Transaction:          base64.StdEncoding.EncodeToString(txBytes),
		TxSignature:          sig.String(),
		LastValidBlockHeight: blockhashRes.Value.LastValidBlockHeight,
		AmountIn:             quote.AmountIn.String(),
		AmountOut:            quote.AmountOut.String(),
		MinAmountOut:         minAmountOut,
		MaxAmountIn:          maxAmountIn,
		PoolAddress:          pool.Address.String(),
		FeeAmount:            quote.FeeAmount.String(),
	}, nil
}

func (svc *BuilderService) BuildMultiHopSwapTransaction(
	ctx context.Context,
	req *domain.SwapRequest,
	multiQuote *domain.MultiHopQuoteResult,
) (*domain.MultiHopSwapResponse, error) {
	// Direct swap for single-hop Vortex pools
	if len(multiQuote.Hops) == 1 && multiQuote.Hops[0].Pool.Type == domain.PoolTypeVortex {
		return svc.buildDirectVortexSwap(ctx, req, multiQuote)
	}

	if len(multiQuote.Hops) > MaxHops {
		return nil, ErrRouteTooLarge
	}

	exactIn := req.SwapMode == "ExactIn"

	var quotedOutAmount uint64
	var minAmountOut, maxAmountIn string

	if exactIn {
		threshold := new(big.Int).Mul(multiQuote.AmountOut, big.NewInt(int64(10000-req.SlippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		quotedOutAmount = threshold.Uint64()
		minAmountOut = threshold.String()
	} else {
		threshold := new(big.Int).Mul(multiQuote.AmountIn, big.NewInt(int64(10000+req.SlippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		quotedOutAmount = threshold.Uint64()
		maxAmountIn = threshold.String()
	}

	tokenMints := multiQuote.Route

	tokenAccounts := make([]solana.PublicKey, len(tokenMints))

	// Extract token programs from pools (zero RPC calls)
	tokenPrograms := extractTokenProgramsFromHops(multiQuote.Hops, tokenMints)

	for i, mint := range tokenMints {
		ata, _, err := GetATAAddressForMint(req.UserWallet, mint, tokenPrograms[i])
		if err != nil {
			return nil, fmt.Errorf("failed to derive ATA for mint %d: %w", i, err)
		}
		tokenAccounts[i] = ata
	}

	remainingAccounts := make([]*solana.AccountMeta, 0, len(tokenAccounts)+len(multiQuote.Hops)*7)

	for _, ata := range tokenAccounts {
		remainingAccounts = append(remainingAccounts, &solana.AccountMeta{
			PublicKey:  ata,
			IsSigner:   false,
			IsWritable: true,
		})
	}

	poolAddresses := make([]string, 0, len(multiQuote.Hops))

	// Build remaining accounts for each hop based on pool type
	for _, hop := range multiQuote.Hops {
		if hop.Pool == nil {
			return nil, errors.New("hop has nil pool")
		}

		pool := hop.Pool
		poolAddresses = append(poolAddresses, pool.Address.String())

		switch pool.Type {
		case domain.PoolTypeVortex:
			// Vortex CLMM pool
			clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
			if !ok || clmmData == nil || clmmData.ParsedVortex == nil {
				return nil, ErrMissingPoolData
			}

			oracle, _, err := GetOraclePDA(pool.Address)
			if err != nil {
				return nil, err
			}

			tickArrayPDAs, err := GetCachedTickArrayPDAs(
				pool.Address,
				clmmData.CurrentTickIndex,
				clmmData.TickSpacing,
				hop.AToB,
			)
			if err != nil {
				return nil, err
			}

			remainingAccounts = append(remainingAccounts,
				&solana.AccountMeta{PublicKey: pool.Address, IsSigner: false, IsWritable: true},
				&solana.AccountMeta{PublicKey: pool.TokenVaultA, IsSigner: false, IsWritable: true},
				&solana.AccountMeta{PublicKey: pool.TokenVaultB, IsSigner: false, IsWritable: true},
				&solana.AccountMeta{PublicKey: tickArrayPDAs[0], IsSigner: false, IsWritable: true},
				&solana.AccountMeta{PublicKey: tickArrayPDAs[1], IsSigner: false, IsWritable: true},
				&solana.AccountMeta{PublicKey: tickArrayPDAs[2], IsSigner: false, IsWritable: true},
				&solana.AccountMeta{PublicKey: oracle, IsSigner: false, IsWritable: false},
			)

		default:
			return nil, fmt.Errorf("unsupported pool type: %v", pool.Type)
		}
	}

	// Add Fee Recipient ATA to remaining accounts
	// Output mint is the last mint in the route
	outputMint := tokenMints[len(tokenMints)-1]
	outputTokenProgram := tokenPrograms[len(tokenPrograms)-1]

	feeRecipientATA, _, err := GetATAAddressForMint(FeeRecipient, outputMint, outputTokenProgram)
	if err != nil {
		return nil, fmt.Errorf("failed to derive fee recipient ATA: %w", err)
	}

	remainingAccounts = append(remainingAccounts, &solana.AccountMeta{
		PublicKey:  feeRecipientATA,
		IsSigner:   false,
		IsWritable: true,
	})

	var swapMode hyperion_ag.SwapMode
	if exactIn {
		swapMode = hyperion_ag.SwapModeExactIn
	} else {
		swapMode = hyperion_ag.SwapModeExactOut
	}

	routePlan := make([]hyperion_ag.RoutePlanStep, len(multiQuote.Hops))
	for i, hop := range multiQuote.Hops {
		percent := uint8(100)
		if multiQuote.IsSplitRoute && i < len(multiQuote.SplitPercents) {
			percent = multiQuote.SplitPercents[i]
		}

		inputIdx := uint8(i)
		outputIdx := uint8(i + 1)
		if multiQuote.IsSplitRoute {
			inputIdx = 0
			outputIdx = 1
		}

		routePlan[i] = hyperion_ag.RoutePlanStep{
			Swap: &hyperion_ag.Swap{
				Value: hyperion_ag.SwapVortexTuple{
					AToB:     hop.AToB,
					SwapMode: swapMode,
				},
			},
			Percent:     percent,
			InputIndex:  inputIdx,
			OutputIndex: outputIdx,
		}
	}

	var amount uint64
	if exactIn {
		amount = multiQuote.AmountIn.Uint64()
	} else {
		amount = multiQuote.AmountOut.Uint64()
	}

	instruction, err := BuildRouteInstruction(
		common.TokenProgramID,
		req.UserWallet,
		uint8(len(tokenMints)),
		routePlan,
		amount,
		quotedOutAmount,
		req.SlippageBps,
		remainingAccounts,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build route instruction: %w", err)
	}

	blockhash, lastValidBlockHeight, err := svc.blockhashCache.GetBlockhash(ctx)
	if err != nil {
		return nil, err
	}

	// Only create ATAs for input and output mints to reduce TX size.
	// Intermediate token ATAs (e.g. SOL, USDC) are expected to already exist.
	instructions := make([]solana.Instruction, 0, 3)
	instructions = append(instructions,
		CreateATAInstructionForMint(req.UserWallet, req.UserWallet, tokenMints[0], tokenPrograms[0]),
		CreateATAInstructionForMint(req.UserWallet, req.UserWallet, tokenMints[len(tokenMints)-1], tokenPrograms[len(tokenMints)-1]),
		CreateATAInstructionForMint(req.UserWallet, FeeRecipient, tokenMints[len(tokenMints)-1], tokenPrograms[len(tokenPrograms)-1]),
	)
	instructions = append(instructions, instruction)

	tx, err := svc.buildTransaction(instructions, blockhash, req.UserWallet)
	if err != nil {
		return nil, err
	}
	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}

	var simResult *domain.SimulationResult
	var computeUnits uint64

	if !req.SkipSimulation {
		simResult, err = svc.SimulateTransaction(ctx, tx)
		if err != nil {
			simResult = &domain.SimulationResult{
				Success: false,
				Error:   fmt.Sprintf("simulation unavailable: %v", err),
			}
		}
		if simResult.Success {
			computeUnits = simResult.ComputeUnitsConsumed
			buffer := computeUnits * 20 / 100
			computeUnits += buffer
		}
	}

	routeStrings := make([]string, len(multiQuote.Route))
	for i, mint := range multiQuote.Route {
		routeStrings[i] = mint.String()
	}

	return &domain.MultiHopSwapResponse{
		SwapResponse: domain.SwapResponse{
			Transaction:          base64.StdEncoding.EncodeToString(txBytes),
			TxSignature:          "",
			LastValidBlockHeight: lastValidBlockHeight,
			AmountIn:             multiQuote.AmountIn.String(),
			AmountOut:            multiQuote.AmountOut.String(),
			MinAmountOut:         minAmountOut,
			MaxAmountIn:          maxAmountIn,
			PoolAddress:          poolAddresses[0],
			FeeAmount:            multiQuote.TotalFee.String(),
			Simulation:           simResult,
			ComputeUnitsEstimate: computeUnits,
		},
		Route:         routeStrings,
		HopCount:      len(multiQuote.Hops),
		Pools:         poolAddresses,
		IsSplitRoute:  multiQuote.IsSplitRoute,
		SplitPercents: multiQuote.SplitPercents,
	}, nil
}

func (svc *BuilderService) buildDirectVortexSwap(
	ctx context.Context,
	req *domain.SwapRequest,
	multiQuote *domain.MultiHopQuoteResult,
) (*domain.MultiHopSwapResponse, error) {
	if len(multiQuote.Hops) != 1 {
		return nil, errors.New("direct swap requires exactly one hop")
	}

	hop := multiQuote.Hops[0]
	pool := hop.Pool
	if pool == nil {
		return nil, ErrNoPoolFound
	}

	clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
	if !ok || clmmData == nil || clmmData.ParsedVortex == nil {
		return nil, ErrMissingPoolData
	}

	exactIn := req.SwapMode == "ExactIn"
	slippageBps := req.SlippageBps
	if slippageBps == 0 {
		slippageBps = 50
	}

	var quotedOutAmount uint64
	var minAmountOut, maxAmountIn string

	if exactIn {
		threshold := new(big.Int).Mul(multiQuote.AmountOut, big.NewInt(int64(10000-slippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		quotedOutAmount = threshold.Uint64()
		minAmountOut = threshold.String()
	} else {
		threshold := new(big.Int).Mul(multiQuote.AmountIn, big.NewInt(int64(10000+slippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		quotedOutAmount = threshold.Uint64()
		maxAmountIn = threshold.String()
	}

	inputMint := req.InputMint
	outputMint := req.OutputMint
	tokenMints := []solana.PublicKey{inputMint, outputMint}

	// Extract token programs from pool (zero RPC calls)
	tokenPrograms := extractTokenProgramsFromPool(pool, inputMint, outputMint)

	tokenAccounts := make([]solana.PublicKey, len(tokenMints))
	for i, mint := range tokenMints {
		ata, _, err := GetATAAddressForMint(req.UserWallet, mint, tokenPrograms[i])
		if err != nil {
			return nil, fmt.Errorf("failed to derive ATA for mint %d: %w", i, err)
		}
		tokenAccounts[i] = ata
	}

	remainingAccounts := make([]*solana.AccountMeta, 0, 9)
	for _, ata := range tokenAccounts {
		remainingAccounts = append(remainingAccounts, &solana.AccountMeta{
			PublicKey:  ata,
			IsSigner:   false,
			IsWritable: true,
		})
	}

	oracle, _, err := GetOraclePDA(pool.Address)
	if err != nil {
		return nil, err
	}

	tickArrayPDAs, err := GetCachedTickArrayPDAs(
		pool.Address,
		clmmData.CurrentTickIndex,
		clmmData.TickSpacing,
		hop.AToB,
	)
	if err != nil {
		return nil, err
	}

	remainingAccounts = append(remainingAccounts,
		&solana.AccountMeta{PublicKey: pool.Address, IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: pool.TokenVaultA, IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: pool.TokenVaultB, IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: tickArrayPDAs[0], IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: tickArrayPDAs[1], IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: tickArrayPDAs[2], IsSigner: false, IsWritable: true},
		&solana.AccountMeta{PublicKey: oracle, IsSigner: false, IsWritable: false},
	)

	var swapMode hyperion_ag.SwapMode
	if exactIn {
		swapMode = hyperion_ag.SwapModeExactIn
	} else {
		swapMode = hyperion_ag.SwapModeExactOut
	}

	routePlan := []hyperion_ag.RoutePlanStep{
		{
			Swap: &hyperion_ag.Swap{
				Value: hyperion_ag.SwapVortexTuple{
					AToB:     hop.AToB,
					SwapMode: swapMode,
				},
			},
			Percent:     100,
			InputIndex:  0,
			OutputIndex: 1,
		},
	}

	var amount uint64
	if exactIn {
		amount = multiQuote.AmountIn.Uint64()
	} else {
		amount = multiQuote.AmountOut.Uint64()
	}

	instruction, err := BuildRouteInstruction(
		common.TokenProgramID,
		req.UserWallet,
		uint8(len(tokenMints)),
		routePlan,
		amount,
		quotedOutAmount,
		slippageBps,
		remainingAccounts,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build route instruction: %w", err)
	}

	blockhash, lastValidBlockHeight, err := svc.blockhashCache.GetBlockhash(ctx)
	if err != nil {
		return nil, err
	}

	instructions := make([]solana.Instruction, 0, 3)
	for i, mint := range tokenMints {
		createATA := CreateATAInstructionForMint(req.UserWallet, req.UserWallet, mint, tokenPrograms[i])
		instructions = append(instructions, createATA)
	}
	// Create Fee Recipient ATA
	createFeeATA := CreateATAInstructionForMint(req.UserWallet, FeeRecipient, tokenMints[1], tokenPrograms[1])
	instructions = append(instructions, createFeeATA)

	instructions = append(instructions, instruction)

	tx, err := svc.buildTransaction(instructions, blockhash, req.UserWallet)
	if err != nil {
		return nil, err
	}
	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}

	var simResult *domain.SimulationResult
	var computeUnits uint64

	if !req.SkipSimulation {
		simResult, err = svc.SimulateTransaction(ctx, tx)
		if err != nil {
			simResult = &domain.SimulationResult{
				Success: false,
				Error:   fmt.Sprintf("simulation unavailable: %v", err),
			}
		}
		if simResult.Success {
			computeUnits = simResult.ComputeUnitsConsumed
			buffer := computeUnits * 20 / 100
			computeUnits += buffer
		}
	}

	return &domain.MultiHopSwapResponse{
		SwapResponse: domain.SwapResponse{
			Transaction:          base64.StdEncoding.EncodeToString(txBytes),
			TxSignature:          "",
			LastValidBlockHeight: lastValidBlockHeight,
			AmountIn:             multiQuote.AmountIn.String(),
			AmountOut:            multiQuote.AmountOut.String(),
			MinAmountOut:         minAmountOut,
			MaxAmountIn:          maxAmountIn,
			PoolAddress:          pool.Address.String(),
			FeeAmount:            multiQuote.TotalFee.String(),
			Simulation:           simResult,
			ComputeUnitsEstimate: computeUnits,
		},
		Route:        []string{inputMint.String(), outputMint.String()},
		HopCount:     1,
		Pools:        []string{pool.Address.String()},
		IsSplitRoute: false,
	}, nil
}

func (svc *BuilderService) BuildMultiHopSessionSwapTransaction(
	ctx context.Context,
	req *domain.SwapRequest,
	multiQuote *domain.MultiHopQuoteResult,
	sessionKey solana.PrivateKey,
	feePayer solana.PrivateKey,
) (*domain.MultiHopSwapResponse, error) {
	if req.UserWallet.IsZero() {
		return nil, ErrInvalidUserWallet
	}

	if len(multiQuote.Hops) > MaxHops {
		return nil, ErrRouteTooLarge
	}

	exactIn := req.SwapMode == "ExactIn"

	slippageBps := req.SlippageBps
	if slippageBps == 0 {
		slippageBps = 50
	}

	var quotedOutAmount uint64
	var minAmountOut, maxAmountIn string

	if exactIn {
		threshold := new(big.Int).Mul(multiQuote.AmountOut, big.NewInt(int64(10000-slippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		quotedOutAmount = threshold.Uint64()
		minAmountOut = threshold.String()
	} else {
		threshold := new(big.Int).Mul(multiQuote.AmountIn, big.NewInt(int64(10000+slippageBps)))
		threshold.Div(threshold, big.NewInt(10000))
		quotedOutAmount = threshold.Uint64()
		maxAmountIn = threshold.String()
	}

	sessionAccountPubkey := sessionKey.PublicKey()

	tokenMints := multiQuote.Route
	tokenAccounts := make([]solana.PublicKey, len(tokenMints))

	// Extract token programs from pools (zero RPC calls)
	tokenPrograms := extractTokenProgramsFromHops(multiQuote.Hops, tokenMints)

	for i, mint := range tokenMints {
		ata, _, err := GetATAAddressForMint(req.UserWallet, mint, tokenPrograms[i])
		if err != nil {
			return nil, fmt.Errorf("failed to derive ATA for mint %d: %w", i, err)
		}
		tokenAccounts[i] = ata
	}

	remainingAccounts := make([]*solana.AccountMeta, 0, len(tokenAccounts)+len(multiQuote.Hops)*7)

	for _, ata := range tokenAccounts {
		remainingAccounts = append(remainingAccounts, &solana.AccountMeta{
			PublicKey:  ata,
			IsSigner:   false,
			IsWritable: true,
		})
	}

	poolAddresses := make([]string, 0, len(multiQuote.Hops))

	for _, hop := range multiQuote.Hops {
		pool := hop.Pool
		if pool == nil {
			return nil, errors.New("hop has nil pool")
		}

		poolAddresses = append(poolAddresses, pool.Address.String())

		clmmData, ok := pool.TypeSpecific.(*domain.VortexPoolData)
		if !ok || clmmData == nil || clmmData.ParsedVortex == nil {
			return nil, ErrMissingPoolData
		}

		oracle, _, err := GetOraclePDA(pool.Address)
		if err != nil {
			return nil, err
		}

		tickArrayPDAs, err := GetCachedTickArrayPDAs(
			pool.Address,
			clmmData.CurrentTickIndex,
			clmmData.TickSpacing,
			hop.AToB,
		)
		if err != nil {
			return nil, err
		}

		remainingAccounts = append(remainingAccounts,
			&solana.AccountMeta{PublicKey: pool.Address, IsSigner: false, IsWritable: true},
			&solana.AccountMeta{PublicKey: pool.TokenVaultA, IsSigner: false, IsWritable: true},
			&solana.AccountMeta{PublicKey: pool.TokenVaultB, IsSigner: false, IsWritable: true},
			&solana.AccountMeta{PublicKey: tickArrayPDAs[0], IsSigner: false, IsWritable: true},
			&solana.AccountMeta{PublicKey: tickArrayPDAs[1], IsSigner: false, IsWritable: true},
			&solana.AccountMeta{PublicKey: tickArrayPDAs[2], IsSigner: false, IsWritable: true},
			&solana.AccountMeta{PublicKey: oracle, IsSigner: false, IsWritable: false},
		)
	}

	// Add Fee Recipient ATA to remaining accounts
	outputMint := tokenMints[len(tokenMints)-1]
	outputTokenProgram := tokenPrograms[len(tokenPrograms)-1]

	feeRecipientATA, _, err := GetATAAddressForMint(FeeRecipient, outputMint, outputTokenProgram)
	if err != nil {
		return nil, fmt.Errorf("failed to derive fee recipient ATA: %w", err)
	}

	remainingAccounts = append(remainingAccounts, &solana.AccountMeta{
		PublicKey:  feeRecipientATA,
		IsSigner:   false,
		IsWritable: true,
	})

	routePlanSession := make([]hyperion_ag.RoutePlanStep, 0, len(multiQuote.Hops))

	for i, hop := range multiQuote.Hops {
		var swapModeSession hyperion_ag.SwapMode
		if exactIn {
			swapModeSession = hyperion_ag.SwapModeExactIn
		} else {
			swapModeSession = hyperion_ag.SwapModeExactOut
		}

		percent := uint8(100)
		inputIndex := uint8(i)
		outputIndex := uint8(i + 1)

		routePlanSession = append(routePlanSession, hyperion_ag.RoutePlanStep{
			Swap: &hyperion_ag.Swap{
				Value: hyperion_ag.SwapVortexTuple{
					AToB:     hop.AToB,
					SwapMode: swapModeSession,
				},
			},
			Percent:     percent,
			InputIndex:  inputIndex,
			OutputIndex: outputIndex,
		})
	}

	var amount uint64
	if exactIn {
		amount = multiQuote.AmountIn.Uint64()
	} else {
		amount = multiQuote.AmountOut.Uint64()
	}

	instruction, err := BuildRouteWithSessionInstruction(
		common.TokenProgramID,
		sessionAccountPubkey,
		uint8(len(tokenMints)),
		routePlanSession,
		amount,
		quotedOutAmount,
		slippageBps,
		remainingAccounts,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build route_with_session instruction: %w", err)
	}

	blockhash, lastValidBlockHeight, err := svc.blockhashCache.GetBlockhash(ctx)
	if err != nil {
		return nil, err
	}
	payer := feePayer.PublicKey()

	// Create Fee Recipient ATA
	createFeeATA := CreateATAInstructionForMint(payer, FeeRecipient, outputMint, outputTokenProgram)

	tx, err := svc.buildTransaction([]solana.Instruction{createFeeATA, instruction}, blockhash, payer)
	if err != nil {
		return nil, err
	}

	_, err = tx.Sign(
		func(key solana.PublicKey) *solana.PrivateKey {
			if key.Equals(sessionKey.PublicKey()) {
				return &sessionKey
			}
			if key.Equals(feePayer.PublicKey()) {
				return &feePayer
			}
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to sign transaction: %w", err)
	}

	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return nil, err
	}
	routeStrings := make([]string, len(multiQuote.Route))
	for i, mint := range multiQuote.Route {
		routeStrings[i] = mint.String()
	}

	sig, err := svc.rpcClient.SendTransaction(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to submit transaction: %w", err)
	}

	return &domain.MultiHopSwapResponse{
		SwapResponse: domain.SwapResponse{
			Transaction:          base64.StdEncoding.EncodeToString(txBytes),
			TxSignature:          sig.String(),
			LastValidBlockHeight: lastValidBlockHeight,
			AmountIn:             multiQuote.AmountIn.String(),
			AmountOut:            multiQuote.AmountOut.String(),
			MinAmountOut:         minAmountOut,
			MaxAmountIn:          maxAmountIn,
			PoolAddress:          poolAddresses[0],
			FeeAmount:            multiQuote.TotalFee.String(),
		},
		Route:    routeStrings,
		HopCount: len(multiQuote.Hops),
		Pools:    poolAddresses,
	}, nil
}

var (
	_ = associatedtokenaccount.ProgramID
	_ = token.ProgramID
)

// extractTokenProgramsFromHops extracts token programs from pool data without RPC calls
// Route is [inputMint, ...intermediateMints, outputMint]
// Each hop has Pool with TokenMintA, TokenMintB, TokenProgramA, TokenProgramB
func extractTokenProgramsFromHops(hops []domain.HopQuote, route []solana.PublicKey) []solana.PublicKey {
	programs := make([]solana.PublicKey, len(route))

	// Default to SPL Token program
	for i := range programs {
		programs[i] = common.TokenProgramID
	}

	// Extract from pools
	for i, hop := range hops {
		if hop.Pool == nil {
			continue
		}
		pool := hop.Pool

		// For hop i: input is route[i], output is route[i+1]
		inputMint := route[i]
		outputMint := route[i+1]

		// Match mint to pool's token A or B
		if inputMint.Equals(pool.TokenMintA) {
			if !pool.TokenProgramA.IsZero() {
				programs[i] = pool.TokenProgramA
			}
		} else if inputMint.Equals(pool.TokenMintB) {
			if !pool.TokenProgramB.IsZero() {
				programs[i] = pool.TokenProgramB
			}
		}

		if outputMint.Equals(pool.TokenMintA) {
			if !pool.TokenProgramA.IsZero() {
				programs[i+1] = pool.TokenProgramA
			}
		} else if outputMint.Equals(pool.TokenMintB) {
			if !pool.TokenProgramB.IsZero() {
				programs[i+1] = pool.TokenProgramB
			}
		}
	}

	return programs
}

// extractTokenProgramsFromPool extracts token programs for a direct swap (single pool)
func extractTokenProgramsFromPool(pool *domain.Pool, inputMint, outputMint solana.PublicKey) []solana.PublicKey {
	programs := []solana.PublicKey{common.TokenProgramID, common.TokenProgramID}

	if pool == nil {
		return programs
	}

	// Input token program
	if inputMint.Equals(pool.TokenMintA) && !pool.TokenProgramA.IsZero() {
		programs[0] = pool.TokenProgramA
	} else if inputMint.Equals(pool.TokenMintB) && !pool.TokenProgramB.IsZero() {
		programs[0] = pool.TokenProgramB
	}

	// Output token program
	if outputMint.Equals(pool.TokenMintA) && !pool.TokenProgramA.IsZero() {
		programs[1] = pool.TokenProgramA
	} else if outputMint.Equals(pool.TokenMintB) && !pool.TokenProgramB.IsZero() {
		programs[1] = pool.TokenProgramB
	}

	return programs
}
