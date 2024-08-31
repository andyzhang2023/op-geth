package state

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// UncommittedDB is a wrapper of StateDB, which records all the writes of the state.
// It is designed for parallel running of the EVM.
// An UncommittedDB instance only serves one transaction, and will be destruct after the transaction is executed.
type UncommittedDB struct {
	// transaction context
	txHash  common.Hash
	txIndex uint64
	logs    []*types.Log

	// accessList
	accessList *accessList

	stateObjects map[common.Address]*stateObject
	// reads records the read state of the maindb before any modification
	reads  reads
	cache  writes
	refund uint64
	maindb *StateDB
}

func NewUncommittedDB(maindb *StateDB) *UncommittedDB {
	return &UncommittedDB{}
}

// ===============================================
// Constructor
// getDeletedObject vs getStateObject ?

func (pst *UncommittedDB) CreateAccount(addr common.Address) {
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
func (pst *UncommittedDB) Prepare(rules params.Rules, sender, coinbase common.Address, dest *common.Address, precompiles []common.Address, txAccesses types.AccessList) {
	pst.maindb.Prepare(rules, sender, coinbase, dest, precompiles, txAccesses)
}

// ===============================================
// Object Methods
//  1. journal
//  2. object

func (pst *UncommittedDB) SubBalance(addr common.Address, amount *big.Int) {
	obj := pst.getObject(addr)
	obj.balance.Sub(big.NewInt(0).Set(obj.balance), amount)
}

func (pst *UncommittedDB) AddBalance(addr common.Address, amount *big.Int) {
	obj := pst.getObject(addr)
	obj.balance.Add(big.NewInt(0).Set(obj.balance), amount)
	pst.maindb.AddBalance(addr, amount)
}

func (pst *UncommittedDB) GetBalance(addr common.Address) *big.Int {
	return nil
}

func (pst *UncommittedDB) GetNonce(addr common.Address) uint64 {
	return pst.maindb.GetNonce(addr)
}

func (pst *UncommittedDB) SetNonce(addr common.Address, nonce uint64)

func (pst *UncommittedDB) GetCodeHash(addr common.Address) common.Hash {
	return pst.maindb.GetCodeHash(addr)
}

func (pst *UncommittedDB) GetCode(addr common.Address) []byte {
	return pst.maindb.GetCode(addr)
}

func (pst *UncommittedDB) GetCodeSize(addr common.Address) int {
	return pst.maindb.GetCodeSize(addr)
}

func (pst *UncommittedDB) SetCode(addr common.Address, code []byte)

func (pst *UncommittedDB) GetCommittedState(addr common.Address, hash common.Hash) common.Hash {
	// this is a uncommitted db, so just get it from the maindb
	return pst.maindb.GetCommittedState(addr, hash)
}

func (pst *UncommittedDB) GetState(addr common.Address, hash common.Hash) common.Hash {
	return pst.maindb.GetState(addr, hash)
}

func (pst *UncommittedDB) SetState(addr common.Address, key, value common.Hash) {
	// "recording reads" here is a little different to other Setters of the object, because:
	//  1. different key holds different value, for the same object.
	//  2. correct value might not be found in the cache, but in the snapshot, or even deeper, in the trie.
	pst.maindb.SetState(addr, key, value)
}

func (pst *UncommittedDB) SelfDestruct(addr common.Address) {
	pst.maindb.SelfDestruct(addr)
}

func (pst *UncommittedDB) HasSelfDestructed(addr common.Address) bool {
	return pst.maindb.HasSelfDestructed(addr)
}

func (pst *UncommittedDB) Selfdestruct6780(addr common.Address)

func (pst *UncommittedDB) Exist(addr common.Address) bool {
	return pst.maindb.Exist(addr)
}

func (pst *UncommittedDB) Empty(addr common.Address) bool {
	return pst.maindb.Empty(addr)
}

// ===============================================
//  Refund Methods
// 	1. journal
//  2. refunds

func (pst *UncommittedDB) AddRefund(amount uint64) {
	pst.maindb.AddRefund(amount)
}

func (pst *UncommittedDB) SubRefund(amount uint64) {
	pst.maindb.SubRefund(amount)
}

func (pst *UncommittedDB) GetRefund() uint64 {
	return pst.maindb.GetRefund()
}

// ===============================================
// transientStorage Methods (EIP-1153: https://eips.ethereum.org/EIPS/eip-1153)

func (pst *UncommittedDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return pst.maindb.GetTransientState(addr, key)
}
func (pst *UncommittedDB) SetTransientState(addr common.Address, key, value common.Hash) {
	pst.maindb.SetTransientState(addr, key, value)
}

// ===============================================
// AccessList Methods (EIP-2930: https://eips.ethereum.org/EIPS/eip-2930)
func (pst *UncommittedDB) AddressInAccessList(addr common.Address) bool {
	return pst.maindb.AddressInAccessList(addr)
}
func (pst *UncommittedDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return pst.maindb.SlotInAccessList(addr, slot)
}
func (pst *UncommittedDB) AddAddressToAccessList(addr common.Address) {
	pst.maindb.AddAddressToAccessList(addr)
}
func (pst *UncommittedDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	pst.maindb.AddSlotToAccessList(addr, slot)
}

// ===============================================
// Snapshot Methods
// (is it necessary to do snapshot and revert ?)
func (pst *UncommittedDB) RevertToSnapshot(id int) {
	pst.maindb.RevertToSnapshot(id)
}
func (pst *UncommittedDB) Snapshot() int {
	return pst.maindb.Snapshot()
}

// ===============================================
// Logs Methods
func (pst *UncommittedDB) AddLog(log *types.Log) {
	pst.maindb.AddLog(log)
}

// ===============================================
// Preimage Methods (EIP-1352: https://eips.ethereum.org/EIPS/eip-1352)
func (pst *UncommittedDB) AddPreimage(hash common.Hash, preimage []byte) {
	pst.maindb.AddPreimage(hash, preimage)
}

// check conflict
func (pst *UncommittedDB) HasConflict() error {
	return nil
}

func (pst *UncommittedDB) Merge() error {
	// 1. merge the writes into the maindb
	// 2. clear the writes
	return nil
}

// getDeletedObj returns the state object for the given address or nil if not found.
// it first gets from the maindb, if not found, then get from the maindb.
// it never modifies the maindb. the maindb is read-only.
// there are some cases to be handle:
//  1. it exists in the maindb's cache
//     a) just return it
//  2. it exists in the maindb, but not in the cache:
//     a) record a read state
//     b) clone it, and put it into the cache
//     c) return it
//  3. it doesn't exist in the maindb, and neighter in the slaveb:
//     a) record a read state with create = true
//     b) return nil
//
// it is notable that the getObj() will not be called parallelly, but the getStateObject() of maindb will be.
// so we need:
//  1. the maindb's cache is not thread safe
//  2. the maindb's cache is thread safe
func (pst *UncommittedDB) getDeletedObject(addr common.Address, maindb *StateDB) (o *state) {
	defer func() {
		pst.reads.recordOnce(addr, o)
	}()
	if pst.cache[addr] != nil {
		return pst.cache[addr]
	}
	// it reads the cache from the maindb
	obj := maindb.getDeletedStateObject(addr)
	if obj == nil {
		// the object is not found, do a read record
		return nil
	}
	// write it into the slavedb and return
	pst.cache[addr] = copyObj(obj)
	return pst.cache[addr]
}

// getObject returns the state object for the given address or nil if not found.
//  1. it first gets from the cache.
//  2. if not found, then get from the maindb,
//  3. record its state in maindb for the first read, which is for further conflict check.
func (pst *UncommittedDB) getObject(addr common.Address) *state {
	obj := pst.getDeletedObject(addr, pst.maindb)
	if obj == nil || obj.deleted {
		return nil
	}
	return obj
}

// getOrNewObj returns the state object for the given address or create a new one if not found.
//  1. it first gets from the cache.
//  2. if not found, then get from the maindb:
//     2.1) if not found
//     2.1.1) create a new one in the cache, keep maindb unchanged
//     2.1.2) record a read state with create = true
//     2.1.3) record a write cache with create = true, balance = 0
//     2.2) if found, record its state in maindb for the first read, which is for further conflict check.
//
// 3. return the state object
func (pst *UncommittedDB) getOrNewObject(addr common.Address) *state {
	obj := pst.getObject(addr)
	if obj != nil {
		return obj
	}
	pst.maindb.CreateAccount(addr)
	//maindb doesn't have it, create a new one in the cache
	pst.cache[addr] = &state{
		balance: big.NewInt(0), // balance need to be 0
		created: true,          // mark the object as created
		addr:    addr,
		state:   make(map[common.Hash]common.Hash),
	}
	return pst.cache[addr]
}

func currState(obj *stateObject, addr common.Address) *state {
	if obj == nil {
		return &state{addr: addr, created: true, state: make(map[common.Hash]common.Hash)}
	}
	return &state{
		addr:         addr,
		balance:      big.NewInt(0).Set(obj.Balance()),
		nonce:        obj.Nonce(),
		selfDestruct: obj.selfDestructed,
		code:         bytes.Clone(obj.code),
		state:        make(map[common.Hash]common.Hash),
		created:      false,
	}
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

type state struct {
	modified int32 //records all the modified fields
	addr     common.Address
	balance  *big.Int
	nonce    uint64
	//@TODO code is lazy loaded, be careful to record its state
	code         []byte
	state        map[common.Hash]common.Hash
	selfDestruct bool
	created      bool
	deleted      bool
}

// check whether the state of current object is conflicted with the maindb
func (s state) conflicts(maindb *StateDB) error {
	addr := s.addr
	obj := maindb.getDeletedStateObject(addr)
	// created == true means it doen't exist in the maindb
	if s.created != (obj == nil) {
		return errors.New("conflict: created")
	}
	// newly created object, no need to compare anything
	if obj == nil {
		return nil
	}
	if s.selfDestruct != obj.selfDestructed {
		return errors.New("conflict: destruct")
	}
	if s.balance.Cmp(obj.Balance()) != 0 {
		return fmt.Errorf("conflict: balance, expected:%d, actual:%d", s.balance.Uint64(), obj.data.Balance.Uint64())
	}
	if s.nonce != obj.Nonce() {
		return fmt.Errorf("conflict: nonce, expected:%d, actual:%d", s.nonce, obj.Nonce())
	}
	// @TODO code is lazy loaded, and should be checked as less as possible.
	if !bytes.Equal(obj.Code(), s.code) {
		return fmt.Errorf("conflict: code, expected len:%d, actual len:%d", len(s.code), len(obj.Code()))
	}
	// @TODO state is lazy loaded, and should be checked as less as possible , too.
	for key, val := range s.state {
		if obj.GetState(key).Cmp(val) != 0 {
			return fmt.Errorf("conflict: state, key:%s, expected:%s, actual:%s", key.String(), val.String(), obj.GetState(key).String())
		}
	}
	return nil
}

func (s state) merge(maindb *StateDB) error {
	return nil
}

func copyObj(obj *stateObject) *state {
	// we don't copy the `code` and `state` here, because they are lazy loaded.
	// we don't copy the `created` eigher, because it true only when the object is nil, which means "it was created newly by the uncommited db".
	// we need to copy the fields `deleted`, because it identifies whether the object is deleted or not.
	return &state{
		nonce:        obj.Nonce(),
		balance:      new(big.Int).Set(obj.Balance()),
		selfDestruct: obj.selfDestructed,
		deleted:      obj.deleted, // deleted is true when a "selfDestruct=true" object is finalized. more details can be found in the method Finalize() of StateDB
	}
}

type reads map[common.Address]*state

func (sts reads) recordOnce(addr common.Address, st *state) *state {
	if _, ok := sts[addr]; !ok {
		if st == nil {
			// this is a newly created object
			sts[addr] = &state{addr: addr, created: true, state: make(map[common.Hash]common.Hash)}
		} else {
			sts[addr] = st
		}
	}
	return st
}

func (sts reads) recordKVOnce(addr common.Address, key, val common.Hash, maindb *StateDB) {
}

func (sts reads) recordCodeOnce(addr common.Address, key, val common.Hash, maindb *StateDB) {
}

// ===============================================
// Writes Methods
// it is used to record all the writes of the uncommitted db.

type writes map[common.Address]*state

const (
	ModifyNonce = 1 << iota
	ModifyBalance
	ModifyCode
	ModifyState
	ModifyDestruct
	ModifyCreate
)

func (wst writes) setBalance(addr common.Address, balance *big.Int) {
	wst.getOrNew(addr).balance = balance
	wst.getOrNew(addr).modified |= ModifyBalance
}

func (wst writes) setNonce(addr common.Address, nonce uint64) {
	wst.getOrNew(addr).nonce = nonce
	wst.getOrNew(addr).modified |= ModifyNonce
}

func (wst writes) setState(addr common.Address, key, val common.Hash) {
	wst.getOrNew(addr).state[key] = val
	wst.getOrNew(addr).modified |= ModifyState
}

func (wst writes) setCode(addr common.Address, code []byte) {
	wst.getOrNew(addr).code = code
	wst.getOrNew(addr).modified |= ModifyCode
}

func (wst writes) getOrNew(addr common.Address) *state {
	if wst[addr] == nil {
		wst[addr] = &state{addr: addr, state: make(map[common.Hash]common.Hash)}
	}
	return wst[addr]
}
