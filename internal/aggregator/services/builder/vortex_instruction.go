package builder

import (
	"github.com/gagliardetto/solana-go"
	hyperion_ag "github.com/thehyperflames/hyperion_ag_go/generated"
)

func BuildRouteInstruction(
	tokenProgram solana.PublicKey,
	userWallet solana.PublicKey,
	numTokenAccounts uint8,
	routePlan []hyperion_ag.RoutePlanStep,
	inAmount uint64,
	quotedOutAmount uint64,
	slippageBps uint16,
	remainingAccounts []*solana.AccountMeta,
) (solana.Instruction, error) {
	builder := hyperion_ag.NewRouteInstructionBuilder().
		SetNumTokenAccounts(numTokenAccounts).
		SetRoutePlan(routePlan).
		SetInAmount(inAmount).
		SetQuotedOutAmount(quotedOutAmount).
		SetSlippageBps(slippageBps).
		SetSimulate(false).
		SetTokenProgramAccount(tokenProgram).
		SetUserTransferAuthorityAccount(userWallet).
		SetProtocolConfigAccount(protocolConfigPDA).
		SetVortexProgramAccount(VortexProgramID).
		SetProgramSignerAccount(programSignerPDA)

	for _, acc := range remainingAccounts {
		builder.Append(acc)
	}

	return builder.ValidateAndBuild()
}

func BuildRouteWithSessionInstruction(
	tokenProgram solana.PublicKey,
	sessionAccount solana.PublicKey,
	numTokenAccounts uint8,
	routePlan []hyperion_ag.RoutePlanStep,
	inAmount uint64,
	quotedOutAmount uint64,
	slippageBps uint16,
	remainingAccounts []*solana.AccountMeta,
) (solana.Instruction, error) {
	builder := hyperion_ag.NewRouteWithSessionInstructionBuilder().
		SetNumTokenAccounts(numTokenAccounts).
		SetRoutePlan(routePlan).
		SetInAmount(inAmount).
		SetQuotedOutAmount(quotedOutAmount).
		SetSlippageBps(slippageBps).
		SetSimulate(false).
		SetTokenProgramAccount(tokenProgram).
		SetSessionAccount(sessionAccount).
		SetProtocolConfigAccount(protocolConfigPDA).
		SetVortexProgramAccount(VortexProgramID).
		SetProgramSignerAccount(programSignerPDA)

	for _, acc := range remainingAccounts {
		builder.Append(acc)
	}

	return builder.ValidateAndBuild()
}
