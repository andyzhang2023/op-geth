package state

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// for debug
type GasSummary struct {
	TxHash       common.Hash
	GasLimit     uint64
	IntrinsicGas struct {
		BasicGas uint64
		InputGas struct {
			NonZeroGas  uint64
			ZeroGas     uint64
			InitCodeGas uint64
		}
		AccessListGas struct {
			AccessListGas uint64
			StorageKeyGas uint64
		}
	}
	EvmCallGas uint64
	Refunds    struct {
		IsLondon     bool
		Refunds      uint64
		StateRefunds uint64
		GasUsed      uint64
	}
	GasUsed uint64
}

func (gs *GasSummary) Debug() string {
	intri := fmt.Sprintf("BasicGas:%d, NonZeroGas:%d, ZeroGas:%d, InitCodeGas:%d, Acceslist:%d, Storagekey:%d", gs.IntrinsicGas.BasicGas, gs.IntrinsicGas.InputGas.NonZeroGas, gs.IntrinsicGas.InputGas.ZeroGas, gs.IntrinsicGas.InputGas.InitCodeGas, gs.IntrinsicGas.AccessListGas.AccessListGas, gs.IntrinsicGas.AccessListGas.StorageKeyGas)
	refunds := fmt.Sprintf("IsLondon:%t, Refunds:%d, StateRefunds:%d, GasUsed:%d", gs.Refunds.IsLondon, gs.Refunds.Refunds, gs.Refunds.StateRefunds, gs.Refunds.GasUsed)
	return fmt.Sprintf("TxHash:%s, GasLimit:%d, IntrinsicGas:{%s}, EvmCallGas:%d, Refunds:{%s}, GasUsed:%d", gs.TxHash.Hex(), gs.GasLimit, intri, gs.EvmCallGas, refunds, gs.GasUsed)
}
