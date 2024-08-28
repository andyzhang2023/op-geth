package state

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type PVMStateDB struct {
	maindb *StateDB
}

// Note: journal: is not copied when Copy a stateDB, so , not part of the state?

// ===============================================
// Constructor

func (pst *PVMStateDB) CreateAccount(addr common.Address) {
	// very hard, how to make it???
	// 1. maindb state need to keep unchanged
	// 2. obj need to be created correctly, and keep in the "write" cache
	// 3. obj need to be merged into the maindb correctly.
	//	3.1 journal.
	//	3.2 account, storage, accountOriginal, storageOriginal (when overwritten)
	pst.maindb.CreateAccount(addr)
}

// Prepare handles the preparatory steps for executing a state transition with.
// This method must be invoked before state transition.
//
// Berlin fork:
// - Add sender to access list (2929)
// - Add destination to access list (2929)
// - Add precompiles to access list (2929)
// - Add the contents of the optional tx access list (2930)
//
// Potential EIPs:
// - Reset access list (Berlin)
// - Add coinbase to access list (EIP-3651)
// - Reset transient storage (EIP-1153)
func (pst *PVMStateDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
	pst.maindb.Prepare(rules, sender, coinbase, dest, precompiles, txAccesses)
}

// ===============================================
// Object Methods
//  1. journal
//  2. object

func (pst *PVMStateDB) SubBalance(addr common.Address, amount *big.Int) {
	pst.maindb.SubBalance(addr, amount)
}

func (pst *PVMStateDB) AddBalance(addr common.Address, amount *big.Int) {
	pst.maindb.AddBalance(addr, amount)
}

func (pst *PVMStateDB) GetBalance(addr common.Address) *big.Int {
	return pst.maindb.GetBalance(addr)
}

func (pst *PVMStateDB) GetNonce(addr common.Address) uint64 {
	return pst.maindb.GetNonce(addr)
}

func (pst *PVMStateDB) SetNonce(addr common.Address, nonce uint64) {
	pst.maindb.SetNonce(addr, nonce)
}

func (pst *PVMStateDB) GetCodeHash(addr common.Address) common.Hash {
	return pst.maindb.GetCodeHash(addr)
}

func (pst *PVMStateDB) GetCode(addr common.Address) []byte {
	return pst.maindb.GetCode(addr)
}

func (pst *PVMStateDB) GetCodeSize(addr common.Address) int {
	return pst.maindb.GetCodeSize(addr)
}

func (pst *PVMStateDB) SetCode(addr common.Address, code []byte) {
	pst.maindb.SetCode(addr, code)
}

func (pst *PVMStateDB) GetCommittedState(addr common.Address, hash common.Hash) common.Hash {
	return pst.maindb.GetCommittedState(addr, hash)
}

func (pst *PVMStateDB) GetState(addr common.Address, hash common.Hash) common.Hash {
	return pst.maindb.GetState(addr, hash)
}

func (pst *PVMStateDB) SetState(addr common.Address, key, value common.Hash) {
	pst.maindb.SetState(addr, key, value)
}

func (pst *PVMStateDB) SelfDestruct(addr common.Address) {
	pst.maindb.SelfDestruct(addr)
}

func (pst *PVMStateDB) HasSelfDestructed(addr common.Address) bool {
	return pst.maindb.HasSelfDestructed(addr)
}

func (pst *PVMStateDB) Selfdestruct6780(addr common.Address) {
	pst.maindb.Selfdestruct6780(addr)
}

func (pst *PVMStateDB) Exist(addr common.Address) bool {
	return pst.maindb.Exist(addr)
}

func (pst *PVMStateDB) Empty(addr common.Address) bool {
	return pst.maindb.Empty(addr)
}

// ===============================================
//  Refund Methods
// 	1. journal
//  2. refunds

func (pst *PVMStateDB) AddRefund(amount uint64) {
	pst.maindb.AddRefund(amount)
}

func (pst *PVMStateDB) SubRefund(amount uint64) {
	pst.maindb.SubRefund(amount)
}

func (pst *PVMStateDB) GetRefund() uint64 {
	return pst.maindb.GetRefund()
}

// ===============================================
// transientStorage Methods (EIP-1153: https://eips.ethereum.org/EIPS/eip-1153)

func (pst *PVMStateDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return pst.maindb.GetTransientState(addr, key)
}
func (pst *PVMStateDB) SetTransientState(addr common.Address, key, value common.Hash) {
	pst.maindb.SetTransientState(addr, key, value)
}

// ===============================================
// AccessList Methods (EIP-2930: https://eips.ethereum.org/EIPS/eip-2930)
func (pst *PVMStateDB) AddressInAccessList(addr common.Address) bool {
	return pst.maindb.AddressInAccessList(addr)
}
func (pst *PVMStateDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return pst.maindb.SlotInAccessList(addr, slot)
}
func (pst *PVMStateDB) AddAddressToAccessList(addr common.Address) {
	pst.maindb.AddAddressToAccessList(addr)
}
func (pst *PVMStateDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	pst.maindb.AddSlotToAccessList(addr, slot)
}

// ===============================================
// Snapshot Methods
func (pst *PVMStateDB) RevertToSnapshot(id int) {
	pst.maindb.RevertToSnapshot(id)
}
func (pst *PVMStateDB) Snapshot() int {
	return pst.maindb.Snapshot()
}

// ===============================================
// Logs Methods
func (pst *PVMStateDB) AddLog(log *types.Log) {
	pst.maindb.AddLog(log)
}

// ===============================================
// Preimage Methods (EIP-1352: https://eips.ethereum.org/EIPS/eip-1352)
func (pst *PVMStateDB) AddPreimage(hash common.Hash, preimage []byte) {
	pst.maindb.AddPreimage(hash, preimage)
}

//CreateAccount(common.Address)
//
//SubBalance(common.Address, *big.Int)
//AddBalance(common.Address, *big.Int)
//GetBalance(common.Address) *big.Int
//
//GetNonce(common.Address) uint64
//SetNonce(common.Address, uint64)
//
//GetCodeHash(common.Address) common.Hash
//GetCode(common.Address) []byte
//SetCode(common.Address, []byte)
//GetCodeSize(common.Address) int
//
//AddRefund(uint64)
//SubRefund(uint64)
//GetRefund() uint64
//
//GetCommittedState(common.Address, common.Hash) common.Hash
//GetState(common.Address, common.Hash) common.Hash
//SetState(common.Address, common.Hash, common.Hash)
//
//GetTransientState(addr common.Address, key common.Hash) common.Hash
//SetTransientState(addr common.Address, key, value common.Hash)
//
//SelfDestruct(common.Address)
//HasSelfDestructed(common.Address) bool
//
//Selfdestruct6780(common.Address)
//
//// Exist reports whether the given account exists in state.
//// Notably this should also return true for self-destructed accounts.
//Exist(common.Address) bool
//// Empty returns whether the given account is empty. Empty
//// is defined according to EIP161 (balance = nonce = code = 0).
//Empty(common.Address) bool
//
//AddressInAccessList(addr common.Address) bool
//SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool)
//// AddAddressToAccessList adds the given address to the access list. This operation is safe to perform
//// even if the feature/fork is not active yet
//AddAddressToAccessList(addr common.Address)
//// AddSlotToAccessList adds the given (address,slot) to the access list. This operation is safe to perform
//// even if the feature/fork is not active yet
//AddSlotToAccessList(addr common.Address, slot common.Hash)
//Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList)
//
//RevertToSnapshot(int)
//Snapshot() int
//
//AddLog(*types.Log)
//AddPreimage(common.Hash, []byte)
