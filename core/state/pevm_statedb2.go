package state

import (
	"bytes"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type PVMStateDB2 struct {
	reads   Rreads2
	slavedb *StateDB
}

func NewPVMStateDB2(maindb *StateDB) *PVMStateDB2 {
	pvmDB := &PVMStateDB2{
		slavedb: &StateDB{
			// trie, snap must be thread safe
			trie:         maindb.trie,
			snap:         maindb.snap,
			stateObjects: make(map[common.Address]*stateObject),
			journal:      newJournal(),
		},
		reads: Rreads2{
			balance: make(map[common.Address]*big.Int),
		},
	}
	return pvmDB
}

// Note: journal: is not copied when Copy a stateDB, so , not part of the state?

// ===============================================
// Constructor

func (pst *PVMStateDB2) CreateAccount(addr common.Address) {
	// very hard, how to make it???
	// 1. slavedb state need to keep unchanged
	// 2. obj need to be created correctly, and keep in the "write" cache
	// 3. obj need to be merged into the slavedb correctly.
	//	3.1 journal.
	//	3.2 account, storage, accountOriginal, storageOriginal (when overwritten)
	pst.slavedb.CreateAccount(addr)
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
func (pst *PVMStateDB2) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
	pst.slavedb.Prepare(rules, sender, coinbase, dest, precompiles, txAccesses)
}

// ===============================================
// Object Methods
//  1. journal
//  2. object

func (pst *PVMStateDB2) SubBalance(addr common.Address, amount *big.Int) {
	pst.slavedb.SubBalance(addr, amount)
	it := pst.slavedb.journal.peak()
	//store the result in reads
	pst.reads.putBalanceOnce(addr, it.(*balanceChange).prev)
}

func (pst *PVMStateDB2) AddBalance(addr common.Address, amount *big.Int) {
	pst.slavedb.AddBalance(addr, amount)
	it := pst.slavedb.journal.peak()
	//store the result in reads
	pst.reads.putBalanceOnce(addr, it.(*balanceChange).prev)
}

func (pst *PVMStateDB2) GetBalance(addr common.Address) *big.Int {
	return pst.slavedb.GetBalance(addr)
}

func (pst *PVMStateDB2) GetNonce(addr common.Address) uint64 {
	return pst.slavedb.GetNonce(addr)
}

func (pst *PVMStateDB2) SetNonce(addr common.Address, nonce uint64) {
	pst.slavedb.SetNonce(addr, nonce)
	it := pst.slavedb.journal.peak()
	pst.reads.putNonceOnce(addr, it.(*nonceChange).prev)
}

func (pst *PVMStateDB2) GetCodeHash(addr common.Address) common.Hash {
	return pst.slavedb.GetCodeHash(addr)
}

func (pst *PVMStateDB2) GetCode(addr common.Address) []byte {
	return pst.slavedb.GetCode(addr)
}

func (pst *PVMStateDB2) GetCodeSize(addr common.Address) int {
	return pst.slavedb.GetCodeSize(addr)
}

func (pst *PVMStateDB2) SetCode(addr common.Address, code []byte) {
	pst.slavedb.SetCode(addr, code)
	ch := pst.slavedb.journal.peak().(*codeChange)
	pst.reads.putCodeOnce(*ch.account, ch.prevcode)
}

func (pst *PVMStateDB2) GetCommittedState(addr common.Address, hash common.Hash) common.Hash {
	return pst.slavedb.GetCommittedState(addr, hash)
}

func (pst *PVMStateDB2) GetState(addr common.Address, hash common.Hash) common.Hash {
	return pst.slavedb.GetState(addr, hash)
}

func (pst *PVMStateDB2) SetState(addr common.Address, key, value common.Hash) {
	pst.slavedb.SetState(addr, key, value)
	ch := pst.slavedb.journal.peak().(*storageChange)
	pst.reads.putStateOnce(*ch.account, ch.key, ch.prevalue)
}

func (pst *PVMStateDB2) SelfDestruct(addr common.Address) {
	pst.slavedb.SelfDestruct(addr)
}

func (pst *PVMStateDB2) HasSelfDestructed(addr common.Address) bool {
	return pst.slavedb.HasSelfDestructed(addr)
}

func (pst *PVMStateDB2) Selfdestruct6780(addr common.Address) {
	pst.slavedb.Selfdestruct6780(addr)
}

func (pst *PVMStateDB2) Exist(addr common.Address) bool {
	return pst.slavedb.Exist(addr)
}

func (pst *PVMStateDB2) Empty(addr common.Address) bool {
	return pst.slavedb.Empty(addr)
}

// ===============================================
//  Refund Methods
// 	1. journal
//  2. refunds

func (pst *PVMStateDB2) AddRefund(amount uint64) {
	pst.slavedb.AddRefund(amount)
}

func (pst *PVMStateDB2) SubRefund(amount uint64) {
	pst.slavedb.SubRefund(amount)
}

func (pst *PVMStateDB2) GetRefund() uint64 {
	return pst.slavedb.GetRefund()
}

// ===============================================
// transientStorage Methods (EIP-1153: https://eips.ethereum.org/EIPS/eip-1153)

func (pst *PVMStateDB2) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return pst.slavedb.GetTransientState(addr, key)
}
func (pst *PVMStateDB2) SetTransientState(addr common.Address, key, value common.Hash) {
	pst.slavedb.SetTransientState(addr, key, value)
}

// ===============================================
// AccessList Methods (EIP-2930: https://eips.ethereum.org/EIPS/eip-2930)
func (pst *PVMStateDB2) AddressInAccessList(addr common.Address) bool {
	return pst.slavedb.AddressInAccessList(addr)
}
func (pst *PVMStateDB2) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return pst.slavedb.SlotInAccessList(addr, slot)
}
func (pst *PVMStateDB2) AddAddressToAccessList(addr common.Address) {
	pst.slavedb.AddAddressToAccessList(addr)
}
func (pst *PVMStateDB2) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	pst.slavedb.AddSlotToAccessList(addr, slot)
}

// ===============================================
// Snapshot Methods
func (pst *PVMStateDB2) RevertToSnapshot(id int) {
	pst.slavedb.RevertToSnapshot(id)
}
func (pst *PVMStateDB2) Snapshot() int {
	return pst.slavedb.Snapshot()
}

// ===============================================
// Logs Methods
func (pst *PVMStateDB2) AddLog(log *types.Log) {
	pst.slavedb.AddLog(log)
}

// ===============================================
// Preimage Methods (EIP-1352: https://eips.ethereum.org/EIPS/eip-1352)
func (pst *PVMStateDB2) AddPreimage(hash common.Hash, preimage []byte) {
	pst.slavedb.AddPreimage(hash, preimage)
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

type Rreads2 struct {
	balance  map[common.Address]*big.Int
	nonce    map[common.Address]uint64
	code     map[common.Address][]byte
	state    map[common.Address]map[common.Hash]common.Hash
	destruct map[common.Address]bool
	created  map[common.Address]bool
}

func (r *Rreads2) putBalanceOnce(addr common.Address, amount *big.Int) {
	if _, ok := r.balance[addr]; !ok {
		r.balance[addr] = amount
	}
}

func (r *Rreads2) putNonceOnce(addr common.Address, nonce uint64) {
	if _, ok := r.nonce[addr]; !ok {
		r.nonce[addr] = nonce
	}
}

func (r *Rreads2) putCodeOnce(addr common.Address, code []byte) {
	if _, ok := r.code[addr]; !ok {
		r.code[addr] = code
	}
}

func (r *Rreads2) putStateOnce(addr common.Address, key, value common.Hash) {
	if _, ok := r.state[addr]; !ok {
		r.state[addr] = make(map[common.Hash]common.Hash)
	}
	if _, ok := r.state[addr][key]; !ok {
		r.state[addr][key] = value
	}
}

func (r *Rreads2) checkBalance(maindb *StateDB) bool {
	for addr, amount := range r.balance {
		if maindb.GetBalance(addr).Cmp(amount) != 0 {
			return false
		}
	}
	return true
}

func (r *Rreads2) checkNonce(maindb *StateDB) bool {
	for addr, nonce := range r.nonce {
		if maindb.GetNonce(addr) != nonce {
			return false
		}
	}
	return true
}

func (r *Rreads2) checkCode(maindb *StateDB) bool {
	for addr, code := range r.code {
		if bytes.Compare(maindb.GetCode(addr), code) != 0 {
			return false
		}
	}
	return true
}

func (r *Rreads2) checkState(maindb *StateDB) bool {
	for addr, states := range r.state {
		for key, value := range states {
			if maindb.GetState(addr, key).Cmp(value) != 0 {
				return false
			}
		}
	}
	return true
}
