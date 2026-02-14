// Package common contains common constants and variables used across services
package common

import "github.com/gagliardetto/solana-go"

var (
	TokenProgramID    = solana.MustPublicKeyFromBase58("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	Token2022ID       = solana.MustPublicKeyFromBase58("TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb")
	MemoProgramID     = solana.MustPublicKeyFromBase58("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr")
	ATAProgramID      = solana.MustPublicKeyFromBase58("ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")
	SystemProgramID   = solana.SystemProgramID
	OracleSeed        = "oracle"
	ProgramSignerSeed = "fogo_session_program_signer"
)
