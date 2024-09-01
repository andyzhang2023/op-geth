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
	discarded bool

	// transaction context
	txHash  common.Hash
	txIndex uint
	logs    []*types.Log

	// accessList
	accessList *accessList

	transientStorage transientStorage

	// object reads and writes
	reads reads
	cache writes

	// refund reads and writes
	prevRefund *uint64
	refund     uint64

	// preimages reads and writes
	preimages map[common.Hash][]byte

	maindb *StateDB
}

func NewUncommittedDB(maindb *StateDB) *UncommittedDB {
	return &UncommittedDB{
		accessList:       maindb.accessList.Copy(),
		transientStorage: maindb.transientStorage.Copy(),
		preimages:        make(map[common.Hash][]byte),
	}
}

func (pst *UncommittedDB) SetTxContext(txHash common.Hash, txIndex uint) {
	pst.txHash = txHash
	pst.txIndex = txIndex
}

// ===============================================
// Constructor
// getDeletedObject vs getStateObject ?

func (pst *UncommittedDB) CreateAccount(addr common.Address) {
	obj := pst.getDeletedObject(addr, pst.maindb)
	// keep the balance
	pst.cache.create(addr)
	if obj != nil {
		pst.cache[addr].balance.Set(obj.balance)
	}
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
func (pst *UncommittedDB) Prepare(rules params.Rules, sender, coinbase common.Address, dst *common.Address, precompiles []common.Address, list types.AccessList) {
	// do nothing, because the work had been done in constructer NewUncommittedDB()
	// 0. init accessList
	// 1. init transientStorage
	if rules.IsBerlin {
		// Clear out any leftover from previous executions
		al := newAccessList()
		pst.accessList = al

		al.AddAddress(sender)
		if dst != nil {
			al.AddAddress(*dst)
			// If it's a create-tx, the destination will be added inside evm.create
		}
		for _, addr := range precompiles {
			al.AddAddress(addr)
		}
		for _, el := range list {
			al.AddAddress(el.Address)
			for _, key := range el.StorageKeys {
				al.AddSlot(el.Address, key)
			}
		}
		if rules.IsShanghai { // EIP-3651: warm coinbase
			al.AddAddress(coinbase)
		}
	}
	// Reset transient storage at the beginning of transaction execution
	pst.transientStorage = newTransientStorage()
}

// ===============================================
// Object Methods
//  1. journal
//  2. object

func (pst *UncommittedDB) SubBalance(addr common.Address, amount *big.Int) {
	obj := pst.getOrNewObject(addr)
	newb := new(big.Int).Sub(obj.balance, amount)
	pst.cache.setBalance(addr, newb)
}

func (pst *UncommittedDB) AddBalance(addr common.Address, amount *big.Int) {
	obj := pst.getOrNewObject(addr)
	newb := new(big.Int).Add(obj.balance, amount)
	pst.cache.setBalance(addr, newb)
}

func (pst *UncommittedDB) GetBalance(addr common.Address) *big.Int {
	return new(big.Int).Set(pst.getOrNewObject(addr).balance)
}

func (pst *UncommittedDB) GetNonce(addr common.Address) uint64 {
	return pst.getOrNewObject(addr).nonce
}

func (pst *UncommittedDB) SetNonce(addr common.Address, nonce uint64) {
	pst.cache.setNonce(addr, nonce)
}

func (pst *UncommittedDB) GetCodeHash(addr common.Address) common.Hash {
	return common.Hash{}
}

func (pst *UncommittedDB) GetCode(addr common.Address) []byte {
	return nil
}

func (pst *UncommittedDB) GetCodeSize(addr common.Address) int {
	return pst.maindb.GetCodeSize(addr)
}

func (pst *UncommittedDB) SetCode(addr common.Address, code []byte)

func (pst *UncommittedDB) GetCommittedState(addr common.Address, hash common.Hash) common.Hash {
	// this is a uncommitted db, so just get it from the maindb
	//@TODO GetCommittedState() need to be thread safe
	return pst.maindb.GetCommittedState(addr, hash)
}

func (pst *UncommittedDB) GetState(addr common.Address, hash common.Hash) common.Hash {
	obj := pst.getObject(addr)
	if obj == nil {
		return common.Hash{}
	}
	if data, ok := obj.state[hash]; ok {
		return data
	}
	return pst.GetCommittedState(addr, hash)
}

func (pst *UncommittedDB) SetState(addr common.Address, key, value common.Hash) {
	pst.getOrNewObject(addr)
	pst.cache.setState(addr, key, value)
}

func (pst *UncommittedDB) SelfDestruct(addr common.Address) {
	if obj := pst.getObject(addr); obj == nil {
		return
	}
	pst.cache.selfDestruct(addr)
}

func (pst *UncommittedDB) HasSelfDestructed(addr common.Address) bool {
	if obj := pst.getObject(addr); obj != nil {
		return obj.selfDestruct
	}
	return false
}

func (pst *UncommittedDB) Selfdestruct6780(addr common.Address) {
	obj := pst.getObject(addr)
	if obj == nil {
		return
	}
	if obj.created {
		pst.SelfDestruct(addr)
	}
}

func (pst *UncommittedDB) Exist(addr common.Address) bool {
	return pst.getObject(addr) != nil
}

func (pst *UncommittedDB) Empty(addr common.Address) bool {
	obj := pst.getObject(addr)
	return obj == nil || obj.empty()
}

// ===============================================
//  Refund Methods
// 	1. journal
//  2. refunds

func (pst *UncommittedDB) AddRefund(gas uint64) {
	pst.readRefund()
	pst.refund += gas
}

func (pst *UncommittedDB) SubRefund(gas uint64) {
	pst.readRefund()
	if pst.refund < gas {
		panic(fmt.Sprintf("Refund counter below zero (gas: %d > refund: %d)", gas, pst.refund))
	}
	pst.refund -= gas
}

func (pst *UncommittedDB) GetRefund() uint64 {
	return pst.readRefund()
}

func (pst *UncommittedDB) readRefund() uint64 {
	if pst.prevRefund == nil {
		refund := pst.maindb.GetRefund()
		pst.prevRefund = &refund
		pst.refund = refund
	}
	return pst.refund
}

// ===============================================
// transientStorage Methods (EIP-1153: https://eips.ethereum.org/EIPS/eip-1153)

func (pst *UncommittedDB) GetTransientState(addr common.Address, key common.Hash) common.Hash {
	return pst.transientStorage.Get(addr, key)
}
func (pst *UncommittedDB) SetTransientState(addr common.Address, key, value common.Hash) {
	pst.transientStorage.Set(addr, key, value)
}

// ===============================================
// AccessList Methods (EIP-2930: https://eips.ethereum.org/EIPS/eip-2930)
func (pst *UncommittedDB) AddressInAccessList(addr common.Address) bool {
	return pst.accessList.ContainsAddress(addr)
}
func (pst *UncommittedDB) SlotInAccessList(addr common.Address, slot common.Hash) (addressOk bool, slotOk bool) {
	return pst.accessList.Contains(addr, slot)
}
func (pst *UncommittedDB) AddAddressToAccessList(addr common.Address) {
	pst.accessList.AddAddress(addr)
}
func (pst *UncommittedDB) AddSlotToAccessList(addr common.Address, slot common.Hash) {
	pst.accessList.AddSlot(addr, slot)
}

// ===============================================
// Snapshot Methods
// (is it necessary to do snapshot and revert ?)
func (pst *UncommittedDB) RevertToSnapshot(id int) {
	// just simply mark this db discarded
	pst.discarded = true
}
func (pst *UncommittedDB) Snapshot() int {
	return 1
}

// ===============================================
// Logs Methods
func (pst *UncommittedDB) AddLog(log *types.Log) {
	log.TxHash = pst.txHash
	log.TxIndex = pst.txIndex
	// we don't need to set Index now, because it will be recalculated when merging into maindb
	log.Index = 0
	pst.logs = append(pst.logs, log)
}

// ===============================================
// Preimage Methods (EIP-1352: https://eips.ethereum.org/EIPS/eip-1352)
func (pst *UncommittedDB) AddPreimage(hash common.Hash, preimage []byte) {
	pst.maindb.AddPreimage(hash, preimage)
	if _, ok := pst.preimages[hash]; !ok {
		pi := make([]byte, len(preimage))
		copy(pi, preimage)
		pst.preimages[hash] = pi
	}
}

// check conflict
func (pst *UncommittedDB) HasConflict() error {
	// 1. check preimages reads conflict
	// 2. check accesslist reads conflict
	// 3. check logs conflict (check the txIndex)
	// 4. check object conflict
	return nil
}

func (pst *UncommittedDB) Merge() error {
	// 1. merge preimages writes
	// 2. merge accesslist writes
	// 3. merge logs writes
	// 4. merge object conflict
	pst.maindb.Finalise(true)
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
	return pst.cache.create(addr)
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

type state struct {
	// object states
	modified int32 //records all the modified fields
	addr     common.Address
	balance  *big.Int
	nonce    uint64
	//@TODO code is lazy loaded, be careful to record its state
	code         []byte
	codeHash     []byte
	state        map[common.Hash]common.Hash
	selfDestruct bool
	created      bool
	deleted      bool
}

func (s *state) markAllModified() {
	s.modified |= ModifyBalance
	s.modified |= ModifyNonce
	s.modified |= ModifyCode
	s.modified |= ModifyState
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

func (s *state) empty() bool {
	return s.nonce == 0 && s.balance.Sign() == 0 && bytes.Equal(s.codeHash, types.EmptyCodeHash.Bytes())
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
	ModifyCreate
	ModifySelfDestruct
)

func (wst writes) setBalance(addr common.Address, balance *big.Int) {
	wst[addr].balance = balance
	wst[addr].modified |= ModifyBalance
}

func (wst writes) setNonce(addr common.Address, nonce uint64) {
	wst[addr].nonce = nonce
	wst[addr].modified |= ModifyNonce
}

func (wst writes) setState(addr common.Address, key, val common.Hash) {
	wst[addr].state[key] = val
	wst[addr].modified |= ModifyState
}

func (wst writes) setCode(addr common.Address, code []byte) {
	wst[addr].code = code
	wst[addr].modified |= ModifyCode
}

func (wst writes) obj(addr common.Address) *state {
	return wst[addr]
}

func (wst writes) create(addr common.Address) *state {
	wst[addr] = &state{
		addr:    addr,
		state:   make(map[common.Hash]common.Hash),
		balance: big.NewInt(0),
		nonce:   0,
	}
	wst[addr].modified |= ModifyCreate
	wst[addr].markAllModified()
	return wst[addr]
}

func (wst writes) selfDestruct(addr common.Address) {
	obj := wst[addr]
	if obj == nil {
		return
	}
	obj.selfDestruct = true
	obj.balance = big.NewInt(0)
	obj.modified |= ModifySelfDestruct
	wst[addr].markAllModified()
}

func (wst writes) getOrNew(addr common.Address) *state {
	if wst[addr] == nil {
		return wst.create(addr)
	}
	return wst[addr]
}
