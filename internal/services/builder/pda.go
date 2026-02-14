package builder

import (
	"sync"

	"github.com/gagliardetto/solana-go"
	"github.com/hxuan190/route-engine/internal/common"
	hyperion_ag "github.com/thehyperflames/hyperion_ag_go/generated"
	valiant "github.com/thehyperflames/valiant_go"
)

var (
	VortexProgramID = solana.MustPublicKeyFromBase58("vnt1u7PzorND5JjweFWmDawKe2hLWoTwHU6QKz6XX98")

	// DrogoAggregatorProgramID is the deployed Drogo aggregator contract
	DrogoAggregatorProgramID = solana.MustPublicKeyFromBase58("DrgonT27zxadFF6KQauQdwy378Mj8ykDqZf3vC3VfTs9")

	// FeeRecipient is the account that receives protocol fees
	FeeRecipient = solana.MustPublicKeyFromBase58("DrgoGtJXes9sJStL4c1oMc8wce5njNiFriste4d1NYT")
)

func init() {
	// Override the default ProgramID in hyperion_ag_go SDK with Drogo program ID
	hyperion_ag.SetProgramID(DrogoAggregatorProgramID)
}

var (
	oraclePDACache   = make(map[solana.PublicKey]solana.PublicKey)
	oraclePDACacheMu sync.RWMutex
)

// tickArrayCacheKey is the key for caching tick array PDAs
type tickArrayCacheKey struct {
	pool        solana.PublicKey
	tickIndex   int32
	tickSpacing uint16
	aToB        bool
}

var (
	tickArrayPDACache   = make(map[tickArrayCacheKey][]solana.PublicKey)
	tickArrayPDACacheMu sync.RWMutex
)

// GetCachedTickArrayPDAs returns cached tick array PDAs or computes and caches them
func GetCachedTickArrayPDAs(pool solana.PublicKey, tickIndex int32, tickSpacing uint16, aToB bool) ([]solana.PublicKey, error) {
	key := tickArrayCacheKey{
		pool:        pool,
		tickIndex:   tickIndex,
		tickSpacing: tickSpacing,
		aToB:        aToB,
	}

	tickArrayPDACacheMu.RLock()
	if cached, ok := tickArrayPDACache[key]; ok {
		tickArrayPDACacheMu.RUnlock()
		return cached, nil
	}
	tickArrayPDACacheMu.RUnlock()

	// Compute tick array PDAs
	pdas, err := valiant.GetTickArrayPDAsForSwap(
		VortexProgramID,
		pool,
		tickIndex,
		tickSpacing,
		aToB,
		3,
	)
	if err != nil {
		return nil, err
	}

	// Cache the result
	tickArrayPDACacheMu.Lock()
	tickArrayPDACache[key] = pdas
	tickArrayPDACacheMu.Unlock()

	return pdas, nil
}

// Pre-computed PDAs (computed once at init)
var (
	protocolConfigPDA solana.PublicKey
	programSignerPDA  solana.PublicKey
	pdasInitialized   bool
	pdasInitMu        sync.Once
)

func initPDAs() {
	pdasInitMu.Do(func() {
		var err error
		protocolConfigPDA, _, err = solana.FindProgramAddress(
			[][]byte{[]byte("protocol_config")},
			hyperion_ag.ProgramID,
		)
		if err != nil {
			panic("failed to derive protocol config PDA: " + err.Error())
		}

		programSignerPDA, _, err = solana.FindProgramAddress(
			[][]byte{[]byte(common.ProgramSignerSeed)},
			VortexProgramID,
		)
		if err != nil {
			panic("failed to derive program signer PDA: " + err.Error())
		}
		pdasInitialized = true
	})
}

func init() {
	initPDAs()
}

func GetOraclePDA(vortexAddress solana.PublicKey) (solana.PublicKey, uint8, error) {
	oraclePDACacheMu.RLock()
	if cached, ok := oraclePDACache[vortexAddress]; ok {
		oraclePDACacheMu.RUnlock()
		return cached, 0, nil
	}
	oraclePDACacheMu.RUnlock()

	seeds := [][]byte{
		[]byte(common.OracleSeed),
		vortexAddress[:],
	}
	pda, bump, err := solana.FindProgramAddress(seeds, VortexProgramID)
	if err != nil {
		return solana.PublicKey{}, 0, err
	}

	oraclePDACacheMu.Lock()
	oraclePDACache[vortexAddress] = pda
	oraclePDACacheMu.Unlock()

	return pda, bump, nil
}

func GetATAAddress(wallet, mint solana.PublicKey) (solana.PublicKey, uint8, error) {
	return solana.FindProgramAddress(
		[][]byte{
			wallet[:],
			common.TokenProgramID[:],
			mint[:],
		},
		common.ATAProgramID,
	)
}

func GetATAAddressToken2022(wallet, mint solana.PublicKey) (solana.PublicKey, uint8, error) {
	return solana.FindProgramAddress(
		[][]byte{
			wallet[:],
			common.Token2022ID[:],
			mint[:],
		},
		common.ATAProgramID,
	)
}

type ataKey struct {
	Wallet       solana.PublicKey
	Mint         solana.PublicKey
	TokenProgram solana.PublicKey
}

var (
	ataCache   = make(map[ataKey]solana.PublicKey)
	ataCacheMu sync.RWMutex
)

func GetATAAddressForMint(wallet, mint, tokenProgram solana.PublicKey) (solana.PublicKey, uint8, error) {
	key := ataKey{Wallet: wallet, Mint: mint, TokenProgram: tokenProgram}

	ataCacheMu.RLock()
	if cached, ok := ataCache[key]; ok {
		ataCacheMu.RUnlock()
		return cached, 0, nil
	}
	ataCacheMu.RUnlock()

	ata, bump, err := solana.FindProgramAddress(
		[][]byte{
			wallet[:],
			tokenProgram[:],
			mint[:],
		},
		common.ATAProgramID,
	)
	if err != nil {
		return solana.PublicKey{}, 0, err
	}

	ataCacheMu.Lock()
	ataCache[key] = ata
	ataCacheMu.Unlock()

	return ata, bump, nil
}

// CreateATAInstruction creates an idempotent ATA creation instruction.
func CreateATAInstruction(payer, owner, mint solana.PublicKey) solana.Instruction {
	ata, _, _ := GetATAAddress(owner, mint)
	return &createATAInstructionGeneric{
		payer:        payer,
		ata:          ata,
		owner:        owner,
		mint:         mint,
		tokenProgram: common.TokenProgramID,
	}
}

func CreateATAInstructionToken2022(payer, owner, mint solana.PublicKey) solana.Instruction {
	ata, _, _ := GetATAAddressToken2022(owner, mint)
	return &createATAInstructionGeneric{
		payer:        payer,
		ata:          ata,
		owner:        owner,
		mint:         mint,
		tokenProgram: common.Token2022ID,
	}
}

func CreateATAInstructionForMint(payer, owner, mint, tokenProgram solana.PublicKey) solana.Instruction {
	ata, _, _ := GetATAAddressForMint(owner, mint, tokenProgram)
	return &createATAInstructionGeneric{
		payer:        payer,
		ata:          ata,
		owner:        owner,
		mint:         mint,
		tokenProgram: tokenProgram,
	}
}

type createATAInstructionGeneric struct {
	payer        solana.PublicKey
	ata          solana.PublicKey
	owner        solana.PublicKey
	mint         solana.PublicKey
	tokenProgram solana.PublicKey
}

func (i *createATAInstructionGeneric) ProgramID() solana.PublicKey {
	return common.ATAProgramID
}

func (i *createATAInstructionGeneric) Accounts() []*solana.AccountMeta {
	return []*solana.AccountMeta{
		{PublicKey: i.payer, IsSigner: true, IsWritable: true},
		{PublicKey: i.ata, IsSigner: false, IsWritable: true},
		{PublicKey: i.owner, IsSigner: false, IsWritable: false},
		{PublicKey: i.mint, IsSigner: false, IsWritable: false},
		{PublicKey: common.SystemProgramID, IsSigner: false, IsWritable: false},
		{PublicKey: i.tokenProgram, IsSigner: false, IsWritable: false},
	}
}

func (i *createATAInstructionGeneric) Data() ([]byte, error) {
	return []byte{1}, nil
}
