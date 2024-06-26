package state

import (
	"bytes"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"math/big"
	"runtime"
	"sort"
	"sync"
)

const defaultNumOfSlots = 100

var parallelKvOnce sync.Once

type ParallelKvCheckUnit struct {
	addr common.Address
	key  common.Hash
	val  common.Hash
}

type ParallelKvCheckMessage struct {
	slotDB   *ParallelStateDB
	isStage2 bool
	kvUnit   ParallelKvCheckUnit
}

var parallelKvCheckReqCh chan ParallelKvCheckMessage
var parallelKvCheckResCh chan bool

type ParallelStateDB struct {
	StateDB
}

func hasKvConflict(slotDB *ParallelStateDB, addr common.Address, key common.Hash, val common.Hash, isStage2 bool) bool {
	mainDB := slotDB.parallel.baseStateDB

	if isStage2 { // update slotDB's unconfirmed DB list and try
		if valUnconfirm, ok := slotDB.getKVFromUnconfirmedDB(addr, key); ok {
			if !bytes.Equal(val.Bytes(), valUnconfirm.Bytes()) {
				log.Debug("IsSlotDBReadsValid KV read is invalid in unconfirmed", "addr", addr,
					"valSlot", val, "valUnconfirm", valUnconfirm,
					"SlotIndex", slotDB.parallel.SlotIndex,
					"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
				return true
			}
		}
	}
	valMain := mainDB.GetState(addr, key)
	if !bytes.Equal(val.Bytes(), valMain.Bytes()) {
		log.Debug("hasKvConflict is invalid", "addr", addr,
			"key", key, "valSlot", val,
			"valMain", valMain, "SlotIndex", slotDB.parallel.SlotIndex,
			"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
		return true // return false, Range will be terminated.
	}
	return false
}

// StartKvCheckLoop start several routines to do conflict check
func StartKvCheckLoop() {
	parallelKvCheckReqCh = make(chan ParallelKvCheckMessage, 200)
	parallelKvCheckResCh = make(chan bool, 10)
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for {
				kvEle1 := <-parallelKvCheckReqCh
				parallelKvCheckResCh <- hasKvConflict(kvEle1.slotDB, kvEle1.kvUnit.addr,
					kvEle1.kvUnit.key, kvEle1.kvUnit.val, kvEle1.isStage2)
			}
		}()
	}
}

// NewSlotDB creates a new State DB based on the provided StateDB.
// With parallel, each execution slot would have its own StateDB.
// This method must be called after the baseDB call PrepareParallel()
func NewSlotDB(db *StateDB, txIndex int, baseTxIndex int, unconfirmedDBs *sync.Map /*map[int]*ParallelStateDB*/) *ParallelStateDB {
	slotDB := db.CopyForSlot()
	slotDB.txIndex = txIndex
	slotDB.originalRoot = db.originalRoot
	slotDB.parallel.baseStateDB = db
	slotDB.parallel.baseTxIndex = baseTxIndex
	slotDB.parallel.unconfirmedDBs = unconfirmedDBs

	return slotDB
}

// RevertSlotDB keep the Read list for conflict detect,
// discard all state changes except:
//   - nonce and balance of from address
//   - balance of system address: will be used on merge to update SystemAddress's balance
func (s *ParallelStateDB) RevertSlotDB(from common.Address) {
	s.parallel.kvChangesInSlot = make(map[common.Address]StateKeys)
	s.parallel.nonceChangesInSlot = make(map[common.Address]struct{})
	s.parallel.balanceChangesInSlot = make(map[common.Address]struct{}, 1)
	s.parallel.addrStateChangesInSlot = make(map[common.Address]bool) // 0: created, 1: deleted

	selfStateObject := s.parallel.dirtiedStateObjectsInSlot[from]
	s.parallel.dirtiedStateObjectsInSlot = make(map[common.Address]*stateObject, 2)
	// keep these elements
	if from.Hex() == "0x6295eE1B4F6dD65047762F924Ecd367c17eaBf8f" {
		fmt.Printf("Dav - RevertSlotDB - set dirtiedStateObjectsInSlot[%s] = obj, obj.codehash: %s\n",
			from, common.Bytes2Hex(selfStateObject.CodeHash()))
	}
	s.parallel.dirtiedStateObjectsInSlot[from] = selfStateObject
	s.parallel.balanceChangesInSlot[from] = struct{}{}
	s.parallel.nonceChangesInSlot[from] = struct{}{}
}

func (s *ParallelStateDB) getBaseStateDB() *StateDB {
	return &s.StateDB
}

func (s *ParallelStateDB) SetSlotIndex(index int) {
	s.parallel.SlotIndex = index
}

// for parallel execution mode, try to get dirty StateObject in slot first.
// it is mainly used by journal revert right now.
func (s *ParallelStateDB) getStateObject(addr common.Address) *stateObject {
	var ret *stateObject
	if obj, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if obj.deleted {
			return nil
		}
		ret = obj
	} else {
		// can not call s.StateDB.getStateObject(), since `newObject` need ParallelStateDB as the interface
		ret = s.getStateObjectNoSlot(addr)
	}
	return ret
}

func (s *ParallelStateDB) storeStateObj(addr common.Address, stateObject *stateObject) {
	// When a state object is stored into s.parallel.stateObjects,
	// it belongs to base StateDB, it is confirmed and valid.
	// todo Dav: why need change this? -- delete me !
	// stateObject.db = s.parallel.baseStateDB
	// stateObject.dbItf = s.parallel.baseStateDB

	// the object could be created in SlotDB, if it got the object from DB and
	// update it to the shared `s.parallel.stateObjects``
	stateObject.db.storeParallelLock.Lock()
	if _, ok := s.parallel.stateObjects.Load(addr); !ok {
		s.parallel.stateObjects.Store(addr, stateObject)
	}
	stateObject.db.storeParallelLock.Unlock()
}

func (s *ParallelStateDB) getStateObjectNoSlot(addr common.Address) *stateObject {
	if obj := s.getDeletedStateObject(addr); obj != nil && !obj.deleted {
		return obj
	}
	return nil
}

// createObject creates a new state object. If there is an existing account with
// the given address, it is overwritten and returned as the second return value.

// prev is used for CreateAccount to get its balance
// Parallel mode:
// if prev in dirty:  revert is ok
// if prev in unconfirmed DB:  addr state read record, revert should not put it back
// if prev in main DB:  addr state read record, revert should not put it back
// if pre no exist:  addr state read record,

// `prev` is used to handle revert, to recover with the `prev` object
// In Parallel mode, we only need to recover to `prev` in SlotDB,
//
//	a.if it is not in SlotDB, `revert` will remove it from the SlotDB
//	b.if it is existed in SlotDB, `revert` will recover to the `prev` in SlotDB
//	c.as `snapDestructs` it is the same
func (s *ParallelStateDB) createObject(addr common.Address) (newobj *stateObject) {
	prev := s.parallel.dirtiedStateObjectsInSlot[addr]
	// TODO-dav: check
	// There can be tx0 create an obj at addr0, tx1 destruct it, and tx2 recreate it use create2.
	// so if tx0 is finalized, and tx1 is unconfirmed, we have to check the states of unconfirmed, otherwise there
	// will be wrong behavior that we recreate an object that is already there. see. test "TestDeleteThenCreate"
	var prevdestruct bool

	if s.snap != nil && prev != nil {
		s.snapParallelLock.Lock()
		_, prevdestruct = s.snapDestructs[prev.address]
		s.parallel.addrSnapDestructsReadsInSlot[addr] = prevdestruct
		if !prevdestruct {
			// To destroy the previous trie node first and update the trie tree
			// with the new object on block commit.
			s.snapDestructs[prev.address] = struct{}{}
		}
		s.snapParallelLock.Unlock()
	}
	newobj = newObject(s, s.isParallel, addr, nil)
	newobj.setNonce(0) // sets the object to dirty
	if prev == nil {
		s.journal.append(createObjectChange{account: &addr})
	} else {
		s.journal.append(resetObjectChange{prev: prev, prevdestruct: prevdestruct})
	}

	s.parallel.addrStateChangesInSlot[addr] = true // the object is created
	s.parallel.nonceChangesInSlot[addr] = struct{}{}
	s.parallel.balanceChangesInSlot[addr] = struct{}{}
	s.parallel.codeChangesInSlot[addr] = struct{}{}
	// notice: all the KVs are cleared if any
	s.parallel.kvChangesInSlot[addr] = make(StateKeys)
	newobj.created = true
	s.parallel.dirtiedStateObjectsInSlot[addr] = newobj
	return newobj
}

// getDeletedStateObject is similar to getStateObject, but instead of returning
// nil for a deleted state object, it returns the actual object with the deleted
// flag set. This is needed by the state journal to revert to the correct s-
// destructed object instead of wiping all knowledge about the state object.
func (s *ParallelStateDB) getDeletedStateObject(addr common.Address) *stateObject {

	// Prefer live objects if any is available
	if obj, _ := s.getStateObjectFromStateObjects(addr); obj != nil {
		return obj
	}

	data, ok := s.getStateObjectFromSnapshotOrTrie(addr)
	if !ok {
		return nil
	}

	// this is why we have to use a separate getDeletedStateObject for ParallelStateDB
	// `s` has to be the ParallelStateDB
	obj := newObject(s, s.isParallel, addr, data)
	s.storeStateObj(addr, obj)
	return obj
}

// GetOrNewStateObject retrieves a state object or create a new state object if nil.
// dirtyInSlot -> Unconfirmed DB -> main DB -> snapshot, no? create one
func (s *ParallelStateDB) GetOrNewStateObject(addr common.Address) *stateObject {
	var object *stateObject
	var ok bool
	if object, ok = s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
		object, _ = s.getStateObjectFromUnconfirmedDB(addr)
		if object == nil {
			object = s.getStateObjectNoSlot(addr) // try to get from base db
		}
		exist := true
		if object == nil || object.deleted /*|| object.selfDestructed*/ {
			object = s.createObject(addr)
			exist = false
		}

		s.parallel.addrStateReadsInSlot[addr] = exist // true: exist, false: not exist
	}
	return object
}

// Exist reports whether the given account address exists in the state.
// Notably this also returns true for suicided accounts.
func (s *ParallelStateDB) Exist(addr common.Address) bool {
	// 1.Try to get from dirty
	if obj, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if obj.deleted /*|| obj.selfDestructed */ {
			log.Error("Exist in dirty, but marked as deleted or suicided",
				"txIndex", s.txIndex, "baseTxIndex:", s.parallel.baseTxIndex)
			return false
		}
		return true
	}
	// 2.Try to get from unconfirmed & main DB
	// 2.1 Already read before
	if exist, ok := s.parallel.addrStateReadsInSlot[addr]; ok {
		return exist
	}

	// 2.2 Try to get from unconfirmed DB if exist
	if exist, ok := s.getAddrStateFromUnconfirmedDB(addr); ok {
		s.parallel.addrStateReadsInSlot[addr] = exist // update and cache
		return exist
	}

	// 3.Try to get from main StateDB
	exist := s.getStateObjectNoSlot(addr) != nil
	s.parallel.addrStateReadsInSlot[addr] = exist // update and cache
	return exist
}

// Empty returns whether the state object is either non-existent
// or empty according to the EIP161 specification (balance = nonce = code = 0)
func (s *ParallelStateDB) Empty(addr common.Address) bool {
	// 1.Try to get from dirty
	if obj, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		// dirty object is light copied and fixup on need,
		// empty could be wrong, except it is created with this TX
		if _, ok := s.parallel.addrStateChangesInSlot[addr]; ok {
			return obj.empty()
		}
		// so we have to check it manually
		// empty means: Nonce == 0 && Balance == 0 && CodeHash == emptyCodeHash
		if s.GetBalance(addr).Sign() != 0 { // check balance first, since it is most likely not zero
			return false
		}
		if s.GetNonce(addr) != 0 {
			return false
		}
		codeHash := s.GetCodeHash(addr)
		return bytes.Equal(codeHash.Bytes(), emptyCodeHash) // code is empty, the object is empty
	}
	// 2.Try to get from unconfirmed & main DB
	// 2.1 Already read before
	if exist, ok := s.parallel.addrStateReadsInSlot[addr]; ok {
		// exist means not empty
		return !exist
	}
	// 2.2 Try to get from unconfirmed DB if exist
	if exist, ok := s.getAddrStateFromUnconfirmedDB(addr); ok {
		s.parallel.addrStateReadsInSlot[addr] = exist // update and cache
		return !exist
	}

	so := s.getStateObjectNoSlot(addr)
	empty := so == nil || so.empty()
	s.parallel.addrStateReadsInSlot[addr] = !empty // update and cache
	return empty
}

// GetBalance retrieves the balance from the given address or 0 if object not found
// GetFrom the dirty list => from unconfirmed DB => get from main stateDB
func (s *ParallelStateDB) GetBalance(addr common.Address) *big.Int {
	var dirtyObj *stateObject
	// 0. Test whether it is deleted in dirty.
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o.deleted {
			return common.Big0
		}
		dirtyObj = o
	}

	// 1.Try to get from dirty
	if _, ok := s.parallel.balanceChangesInSlot[addr]; ok {
		// on balance fixup, addr may not exist in dirtiedStateObjectsInSlot
		// we intend to fixup balance based on unconfirmed DB or main DB
		return dirtyObj.Balance()
	}
	// 2.Try to get from unconfirmed DB or main DB
	// 2.1 Already read before
	if balance, ok := s.parallel.balanceReadsInSlot[addr]; ok {
		return balance
	}

	balance := common.Big0
	// 2.2 Try to get from unconfirmed DB if exist
	if blc := s.getBalanceFromUnconfirmedDB(addr); blc != nil {
		balance = blc
	} else {
		// 3. Try to get from main StateObject
		blc = common.Big0
		object := s.getStateObjectNoSlot(addr)
		if object != nil {
			blc = object.Balance()
		}
		balance = blc
	}
	s.parallel.balanceReadsInSlot[addr] = balance

	// fixup dirties
	if dirtyObj != nil && dirtyObj.Balance() != balance {
		dirtyObj.setBalance(balance)
	}

	return balance
}

// GetBalanceOpCode different from GetBalance(), it is opcode triggered
func (s *ParallelStateDB) GetBalanceOpCode(addr common.Address) *big.Int {
	return s.GetBalance(addr)
}

func (s *ParallelStateDB) GetNonce(addr common.Address) uint64 {
	var dirtyObj *stateObject
	// 0. Test whether it is deleted in dirty.
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o.deleted {
			return 0
		}
		dirtyObj = o
	}

	// 1.Try to get from dirty
	if _, ok := s.parallel.nonceChangesInSlot[addr]; ok {
		// on nonce fixup, addr may not exist in dirtiedStateObjectsInSlot
		// we intend to fixup nonce based on unconfirmed DB or main DB
		return dirtyObj.Nonce()
	}
	// 2.Try to get from unconfirmed DB or main DB
	// 2.1 Already read before
	if nonce, ok := s.parallel.nonceReadsInSlot[addr]; ok {
		return nonce
	}

	var nonce uint64 = 0
	// 2.2 Try to get from unconfirmed DB if exist
	if nc, ok := s.getNonceFromUnconfirmedDB(addr); ok {
		nonce = nc
	} else {
		// 3.Try to get from main StateDB
		nc = 0
		object := s.getStateObjectNoSlot(addr)
		if object != nil {
			nc = object.Nonce()
		}
		nonce = nc
	}
	s.parallel.nonceReadsInSlot[addr] = nonce

	// fixup dirties
	if dirtyObj != nil && dirtyObj.Nonce() < nonce {
		dirtyObj.setNonce(nonce)
	}
	return nonce
}

func (s *ParallelStateDB) GetCode(addr common.Address) []byte {
	var dirtyObj *stateObject
	// 0. Test whether it is deleted in dirty.
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o.deleted {
			return nil
		}
		dirtyObj = o
	}

	// 1.Try to get from dirty
	if _, ok := s.parallel.codeChangesInSlot[addr]; ok {
		// on code fixup, addr may not exist in dirtiedStateObjectsInSlot
		// we intend to fixup code based on unconfirmed DB or main DB
		return dirtyObj.Code()
	}
	// 2.Try to get from unconfirmed DB or main DB
	// 2.1 Already read before
	if code, ok := s.parallel.codeReadsInSlot[addr]; ok {
		return code
	}
	var code []byte
	// 2.2 Try to get from unconfirmed DB if exist
	if cd, ok := s.getCodeFromUnconfirmedDB(addr); ok {
		code = cd
	} else {
		// 3. Try to get from main StateObject
		object := s.getStateObjectNoSlot(addr)
		if object != nil {
			code = object.Code()
		}
	}
	s.parallel.codeReadsInSlot[addr] = code

	// fixup dirties
	if dirtyObj != nil && !bytes.Equal(dirtyObj.code, code) {
		dirtyObj.code = code
	}
	return code
}

func (s *ParallelStateDB) GetCodeSize(addr common.Address) int {
	var dirtyObj *stateObject
	// 0. Test whether it is deleted in dirty.
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o.deleted {
			return 0
		}
		dirtyObj = o
	}
	// 1.Try to get from dirty
	if _, ok := s.parallel.codeChangesInSlot[addr]; ok {
		// on code fixup, addr may not exist in dirtiedStateObjectsInSlot
		// we intend to fixup code based on unconfirmed DB or main DB
		return dirtyObj.CodeSize()
	}
	// 2.Try to get from unconfirmed DB or main DB
	// 2.1 Already read before
	if code, ok := s.parallel.codeReadsInSlot[addr]; ok {
		return len(code) // len(nil) is 0 too
	}

	cs := 0
	var code []byte
	// 2.2 Try to get from unconfirmed DB if exist
	if cd, ok := s.getCodeFromUnconfirmedDB(addr); ok {
		cs = len(cd) // len(nil) is 0 too
		code = cd
	} else {
		// 3. Try to get from main StateObject
		var cc []byte
		object := s.getStateObjectNoSlot(addr)
		if object != nil {
			// This is where we update the code from possible db.ContractCode if the original object.code is nil.
			cc = object.Code()
			cs = object.CodeSize()
		}
		code = cc
	}
	s.parallel.codeReadsInSlot[addr] = code
	// fixup dirties
	if dirtyObj != nil {
		if !bytes.Equal(dirtyObj.code, code) {
			dirtyObj.code = code
		}
	}
	return cs
}

// GetCodeHash return:
//   - common.Hash{}: the address does not exist
//   - emptyCodeHash: the address exist, but code is empty
//   - others:        the address exist, and code is not empty
func (s *ParallelStateDB) GetCodeHash(addr common.Address) common.Hash {
	var dirtyObj *stateObject
	// 0. Test whether it is deleted in dirty.
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o.deleted {
			return common.Hash{}
		}
		dirtyObj = o
	}

	// 1.Try to get from dirty
	if _, ok := s.parallel.codeChangesInSlot[addr]; ok {
		// on code fixup, addr may not exist in dirtiedStateObjectsInSlot
		// we intend to fixup balance based on unconfirmed DB or main DB
		return common.BytesToHash(dirtyObj.CodeHash())
	}
	// 2.Try to get from unconfirmed DB or main DB
	// 2.1 Already read before
	if codeHash, ok := s.parallel.codeHashReadsInSlot[addr]; ok {
		return codeHash
	}
	codeHash := common.Hash{}
	// 2.2 Try to get from unconfirmed DB if exist
	if cHash, ok := s.getCodeHashFromUnconfirmedDB(addr); ok {
		codeHash = cHash
	} else {
		// 3. Try to get from main StateObject
		object := s.getStateObjectNoSlot(addr)

		if object != nil {
			codeHash = common.BytesToHash(object.CodeHash())
		}
	}
	s.parallel.codeHashReadsInSlot[addr] = codeHash

	// fill slots in dirty if exist.
	// A case for this:
	// TX0: createAccount at addr 0x123, set code and codehash
	// TX1: AddBalance - now an obj in dirty with empty codehash, and codeChangesInSlot is false (not changed)
	//      GetCodeHash - get from unconfirmedDB or mainDB, set codeHashReadsInSlot to the new val.
	//      SELFDESTRUCT - set codeChangesInSlot, but the obj in dirty is with Empty codehash.
	//     				   obj marked selfdestructed but not deleted. so CodeHash is not empty.
	//      GetCodeHash - since the codeChangesInslot is marked, get the object from dirty, and get the
	//                    wrong 'empty' hash.
	if dirtyObj != nil {
		// found one
		if dirtyObj.CodeHash() == nil || bytes.Equal(dirtyObj.CodeHash(), emptyCodeHash) {
			if bytes.Equal(codeHash.Bytes(), emptyCodeHash) {
				fmt.Printf("Dav -- update codehash to empty in dirty - addr: %s\n", addr)
			}
			dirtyObj.data.CodeHash = codeHash.Bytes()
		}
	}
	return codeHash
}

// GetState retrieves a value from the given account's storage trie.
// For parallel mode wih, get from the state in order:
//
//	-> self dirty, both Slot & MainProcessor
//	-> pending of self: Slot on merge
//	-> pending of unconfirmed DB
//	-> pending of main StateDB
//	-> origin
func (s *ParallelStateDB) GetState(addr common.Address, hash common.Hash) common.Hash {
	var dirtyObj *stateObject
	// 0. Test whether it is deleted in dirty.
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o == nil || o.deleted {
			return common.Hash{}
		}
		dirtyObj = o
	}
	// 1.Try to get from dirty
	if exist, ok := s.parallel.addrStateChangesInSlot[addr]; ok {
		if !exist {
			// it could be suicided within this SlotDB?
			// it should be able to get state from suicided address within a Tx:
			// e.g. within a transaction: call addr:suicide -> get state: should be ok
			// return common.Hash{}
			log.Info("ParallelStateDB GetState suicided", "addr", addr, "hash", hash)
		} else {
			// It is possible that an object get created but not dirtied since there is no state set, such as recreate.
			// In this case, simply return common.Hash{}.
			// This is for corner case:
			//	B0: TX0 --> createAccount @addr1	-- merged into DB
			//  B1: Tx1 and Tx2
			//      Tx1 account@addr1 selfDestruct  -- unconfirmed
			//      Tx2 recreate account@addr2  	-- executing
			// Since any state change and suicide could record in s.parallel.addrStateChangeInSlot, it is save to simple
			// return common.Hash{} for this case as the previous TX must has the object destructed.
			// P.S. if the Tx2 both destruct and recreate the object, it will not fall into this logic, as the change
			// will be recorded in dirtiedStateObjectsInSlot.

			// it could be suicided within this SlotDB?
			// it should be able to get state from suicided address within a Tx:
			// e.g. within a transaction: call addr:suicide -> get state: should be ok
			// return common.Hash{}
			log.Info("ParallelStateDB GetState suicided", "addr", addr, "hash", hash)

			if dirtyObj == nil {
				log.Error("ParallelStateDB GetState access untouched object after create, may check create2")
				return common.Hash{}
			}
			return dirtyObj.GetState(hash)
		}
	}

	if keys, ok := s.parallel.kvChangesInSlot[addr]; ok {
		if _, ok := keys[hash]; ok {
			return dirtyObj.GetState(hash)
		}
	}
	// 2.Try to get from unconfirmed DB or main DB
	// 2.1 Already read before
	if storage, ok := s.parallel.kvReadsInSlot[addr]; ok {
		if val, ok := storage.GetValue(hash); ok {
			return val
		}
	}

	value := common.Hash{}
	// 2.2 Try to get from unconfirmed DB if exist
	if val, ok := s.getKVFromUnconfirmedDB(addr, hash); ok {
		value = val
	} else {
		// 3.Get from main StateDB
		object := s.getStateObjectNoSlot(addr)
		val = common.Hash{}
		if object != nil {
			val = object.GetState(hash)
			// TODO-dav: delete following originStorage change, as lightCopy is copy originStorage now.
			// test dirty, there can be a case the object saved in dirty by other changes such as SetBalance. But the
			// addrStateChangesInSlot[addr] does not record it. So later load from the dirties would cause flaw because the
			// first value loaded from main stateDB is not updated to the object in dirties.
			// Moreover, there is also an issue that the other kv in the object get from snap or trie that is accessed from
			// previous tx in same block but not touched in current tx, is missed in the dirty. which may cause issues when
			// calculate the root.
			_, recorded := s.parallel.addrStateChangesInSlot[addr]
			obj, isDirty := s.parallel.dirtiedStateObjectsInSlot[addr]
			if !recorded && isDirty {
				v, ok := obj.originStorage.GetValue(hash)

				if !(ok && v.Cmp(val) == 0) {
					obj.originStorage.StoreValue(hash, val)
				}
			}
		}
		value = val
	}
	if s.parallel.kvReadsInSlot[addr] == nil {
		s.parallel.kvReadsInSlot[addr] = newStorage(false)
	}
	s.parallel.kvReadsInSlot[addr].StoreValue(hash, value) // update cache

	// fixup Dirty
	if dirtyObj != nil {
		old := dirtyObj.GetState(hash)
		if old.Cmp(value) != 0 {
			dirtyObj.setState(hash, value)
		}
	}
	return value
}

// GetCommittedState retrieves a value from the given account's committed storage trie.
func (s *ParallelStateDB) GetCommittedState(addr common.Address, hash common.Hash) common.Hash {
	// 0. Test whether it is deleted.
	var dirtyObj *stateObject
	if o, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if o.deleted {
			return common.Hash{}
		}
		dirtyObj = o
	}
	// 2.Try to get from unconfirmed DB or main DB
	//   KVs in unconfirmed DB can be seen as pending storage
	//   KVs in main DB are merged from SlotDB and has done finalise() on merge, can be seen as pending storage too.
	// 2.1 Already read before
	if storage, ok := s.parallel.kvReadsInSlot[addr]; ok {
		if val, ok := storage.GetValue(hash); ok {
			return val
		}
	}
	value := common.Hash{}
	// 2.2 Try to get from unconfirmed DB if exist
	if val, ok := s.getKVFromUnconfirmedDB(addr, hash); ok {
		value = val
	} else {
		// 3. Try to get from main DB
		val = common.Hash{}
		object := s.getStateObjectNoSlot(addr)
		if object != nil {
			val = object.GetCommittedState(hash)
		}
		value = val
	}
	if s.parallel.kvReadsInSlot[addr] == nil {
		s.parallel.kvReadsInSlot[addr] = newStorage(false)
	}
	s.parallel.kvReadsInSlot[addr].StoreValue(hash, value) // update cache

	// fixup Dirty
	if dirtyObj != nil {
		old := dirtyObj.GetState(hash)
		if old.Cmp(value) != 0 {
			dirtyObj.setState(hash, value)
		}
	}

	return value
}

func (s *ParallelStateDB) HasSelfDestructed(addr common.Address) bool {
	// 1.Try to get from dirty
	if obj, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; ok {
		if obj == nil || obj.deleted {
			return false
		}
		return obj.selfDestructed
	}
	// 2.Try to get from unconfirmed
	if exist, ok := s.getAddrStateFromUnconfirmedDB(addr); ok {
		return !exist
	}

	object := s.getDeletedStateObject(addr)
	if object != nil {
		return object.selfDestructed
	}
	return false
}

// AddBalance adds amount to the account associated with addr.
func (s *ParallelStateDB) AddBalance(addr common.Address, amount *big.Int) {
	// add balance will perform a read operation first
	// if amount == 0, no balance change, but there is still an empty check.
	object := s.GetOrNewStateObject(addr)
	if object != nil {
		if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
			newStateObject := object.lightCopy(s) // light copy from main DB
			// do balance fixup from the confirmed DB, it could be more reliable than main DB
			balance := s.GetBalance(addr) // it will record the balance read operation
			newStateObject.setBalance(balance)
			newStateObject.AddBalance(amount)
			s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
			s.parallel.balanceChangesInSlot[addr] = struct{}{}
			return
		}
		// already dirty, make sure the balance is fixed up since it could be previously dirtied by nonce or KV...
		balance := s.GetBalance(addr)
		if object.Balance().Cmp(balance) != 0 {
			log.Warn("AddBalance in dirty, but balance has not do fixup", "txIndex", s.txIndex, "addr", addr,
				"stateObject.Balance()", object.Balance(), "s.GetBalance(addr)", balance)
			object.setBalance(balance)
		}

		object.AddBalance(amount)
		s.parallel.balanceChangesInSlot[addr] = struct{}{}
	}
}

// SubBalance subtracts amount from the account associated with addr.
func (s *ParallelStateDB) SubBalance(addr common.Address, amount *big.Int) {
	// unlike add, sub 0 balance will not touch empty object
	object := s.GetOrNewStateObject(addr)
	if object != nil {
		if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
			newStateObject := object.lightCopy(s) // light copy from main DB
			// do balance fixup from the confirmed DB, it could be more reliable than main DB
			balance := s.GetBalance(addr)
			newStateObject.setBalance(balance)
			newStateObject.SubBalance(amount)
			s.parallel.balanceChangesInSlot[addr] = struct{}{}
			s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
			return
		}
		// already dirty, make sure the balance is fixed up since it could be previously dirtied by nonce or KV...
		balance := s.GetBalance(addr)
		if object.Balance().Cmp(balance) != 0 {
			log.Warn("SubBalance in dirty, but balance is incorrect", "txIndex", s.txIndex, "addr", addr,
				"stateObject.Balance()", object.Balance(), "s.GetBalance(addr)", balance)
			object.setBalance(balance)
		}
		object.SubBalance(amount)
		s.parallel.balanceChangesInSlot[addr] = struct{}{}
	}
}

func (s *ParallelStateDB) SetBalance(addr common.Address, amount *big.Int) {
	object := s.GetOrNewStateObject(addr)
	if object != nil {
		if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
			newStateObject := object.lightCopy(s)
			// update balance for revert, in case child contract is reverted,
			// it should revert to the previous balance
			balance := s.GetBalance(addr)
			newStateObject.setBalance(balance)
			newStateObject.SetBalance(amount)
			s.parallel.balanceChangesInSlot[addr] = struct{}{}
			s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
			return
		}

		balance := s.GetBalance(addr)
		object.setBalance(balance)
		object.SetBalance(amount)
		s.parallel.balanceChangesInSlot[addr] = struct{}{}
	}
}

func (s *ParallelStateDB) SetNonce(addr common.Address, nonce uint64) {
	object := s.GetOrNewStateObject(addr)
	if object != nil {
		if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
			newStateObject := object.lightCopy(s)
			noncePre := s.GetNonce(addr)
			newStateObject.setNonce(noncePre) // nonce fixup
			newStateObject.SetNonce(nonce)
			s.parallel.nonceChangesInSlot[addr] = struct{}{}
			s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
			return
		}
		noncePre := s.GetNonce(addr)
		object.setNonce(noncePre) // nonce fixup
		object.SetNonce(nonce)
		s.parallel.nonceChangesInSlot[addr] = struct{}{}
	}
}

func (s *ParallelStateDB) SetCode(addr common.Address, code []byte) {
	object := s.GetOrNewStateObject(addr)
	if object != nil {
		codeHash := crypto.Keccak256Hash(code)
		if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
			newStateObject := object.lightCopy(s)
			codePre := s.GetCode(addr) // code fixup
			codeHashPre := crypto.Keccak256Hash(codePre)
			newStateObject.setCode(codeHashPre, codePre)
			newStateObject.SetCode(codeHash, code)
			s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
			s.parallel.codeChangesInSlot[addr] = struct{}{}
			return
		}
		codePre := s.GetCode(addr) // code fixup
		codeHashPre := crypto.Keccak256Hash(codePre)
		object.setCode(codeHashPre, codePre)
		object.SetCode(codeHash, code)
		s.parallel.codeChangesInSlot[addr] = struct{}{}
	}
}

func (s *ParallelStateDB) SetState(addr common.Address, key, value common.Hash) {
	object := s.GetOrNewStateObject(addr) // attention: if StateObject's lightCopy, its storage is only a part of the full storage,
	if object != nil {
		if s.parallel.baseTxIndex+1 == s.txIndex {
			// we check if state is unchanged
			// only when current transaction is the next transaction to be committed
			// fixme: there is a bug, block: 14,962,284,
			//        stateObject is in dirty (light copy), but the key is in mainStateDB
			//        stateObject dirty -> committed, will skip mainStateDB dirty
			if s.GetState(addr, key) == value {
				log.Debug("Skip set same state", "baseTxIndex", s.parallel.baseTxIndex,
					"txIndex", s.txIndex, "addr", addr,
					"key", key, "value", value)
				return
			}
		}

		if s.parallel.kvChangesInSlot[addr] == nil {
			s.parallel.kvChangesInSlot[addr] = make(StateKeys) // make(Storage, defaultNumOfSlots)
		}

		if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
			newStateObject := object.lightCopy(s)
			newStateObject.SetState(key, value)
			s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
			s.parallel.addrStateChangesInSlot[addr] = true
			return
		}
		// do State Update
		object.SetState(key, value)
		s.parallel.addrStateChangesInSlot[addr] = true
	}
}

// SelfDestruct marks the given account as suicided.
// This clears the account balance.
//
// The account's state object is still available until the state is committed,
// getStateObject will return a non-nil account after Suicide.
func (s *ParallelStateDB) SelfDestruct(addr common.Address) {
	var object *stateObject
	// 1.Try to get from dirty, it could be suicided inside of contract call
	object = s.parallel.dirtiedStateObjectsInSlot[addr]

	if object == nil {
		// 2.Try to get from unconfirmed, if deleted return false, since the address does not exist
		if obj, ok := s.getStateObjectFromUnconfirmedDB(addr); ok {
			object = obj
			s.parallel.addrStateReadsInSlot[addr] = !object.deleted // true: exist, false: deleted
		}
	}
	if object != nil && object.deleted {
		return
	}

	if object == nil {
		// 3.Try to get from main StateDB
		object = s.getStateObjectNoSlot(addr)
		if object == nil || object.deleted {
			s.parallel.addrStateReadsInSlot[addr] = false // true: exist, false: deleted
			return
		}
		s.parallel.addrStateReadsInSlot[addr] = true // true: exist, false: deleted
	}

	s.journal.append(selfDestructChange{
		account:     &addr,
		prev:        object.selfDestructed, // todo: must be false?
		prevbalance: new(big.Int).Set(s.GetBalance(addr)),
	})

	if _, ok := s.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
		// do copy-on-write for suicide "write"
		newStateObject := object.lightCopy(s)
		newStateObject.markSelfdestructed()
		newStateObject.data.Balance = new(big.Int)
		s.parallel.dirtiedStateObjectsInSlot[addr] = newStateObject
		s.parallel.addrStateChangesInSlot[addr] = false // false: the address does not exist any more,
		// s.parallel.nonceChangesInSlot[addr] = struct{}{}
		s.parallel.balanceChangesInSlot[addr] = struct{}{}
		s.parallel.codeChangesInSlot[addr] = struct{}{}
		// s.parallel.kvChangesInSlot[addr] = make(StateKeys) // all key changes are discarded
		return
	}
	s.parallel.addrStateChangesInSlot[addr] = false // false: the address does not exist anymore
	s.parallel.balanceChangesInSlot[addr] = struct{}{}
	s.parallel.codeChangesInSlot[addr] = struct{}{}

	object.markSelfdestructed()
	object.data.Balance = new(big.Int)
}

func (s *ParallelStateDB) Selfdestruct6780(addr common.Address) {
	object := s.getStateObject(addr)
	if object == nil {
		return
	}
	if object.created {
		s.SelfDestruct(addr)
	}
}

// CreateAccount explicitly creates a state object. If a state object with the address
// already exists the balance is carried over to the new account.
//
// CreateAccount is called during the EVM CREATE operation. The situation might arise that
// a contract does the following:
//
//  1. sends funds to sha(account ++ (nonce + 1))
//  2. tx_create(sha(account ++ nonce)) (note that this gets the address of 1)
//
// Carrying over the balance ensures that Ether doesn't disappear.
func (s *ParallelStateDB) CreateAccount(addr common.Address) {
	// no matter it is got from dirty, unconfirmed or main DB
	// if addr not exist, preBalance will be common.Big0, it is same as new(big.Int) which
	// is the value newObject(),
	preBalance := s.GetBalance(addr) // parallel balance read will be recorded inside GetBalance
	newObj := s.createObject(addr)
	newObj.setBalance(new(big.Int).Set(preBalance)) // new big.Int for newObj
}

// RevertToSnapshot reverts all state changes made since the given revision.
func (s *ParallelStateDB) RevertToSnapshot(revid int) {
	// Find the snapshot in the stack of valid snapshots.
	idx := sort.Search(len(s.validRevisions), func(i int) bool {
		return s.validRevisions[i].id >= revid
	})
	if idx == len(s.validRevisions) || s.validRevisions[idx].id != revid {
		panic(fmt.Errorf("revision id %v cannot be reverted", revid))
	}
	snapshot := s.validRevisions[idx].journalIndex

	// Replay the journal to undo changes and remove invalidated snapshots
	s.journal.revert(s, snapshot)
	s.validRevisions = s.validRevisions[:idx]
}

// AddRefund adds gas to the refund counter
// journal.append will use ParallelState for revert
func (s *ParallelStateDB) AddRefund(gas uint64) { // todo: not needed, can be deleted
	s.journal.append(refundChange{prev: s.refund})
	s.refund += gas
}

// SubRefund removes gas from the refund counter.
// This method will panic if the refund counter goes below zero
func (s *ParallelStateDB) SubRefund(gas uint64) {
	s.journal.append(refundChange{prev: s.refund})
	if gas > s.refund {
		// we don't need to panic here if we read the wrong state in parallel mode
		// we just need to redo this transaction
		log.Info(fmt.Sprintf("Refund counter below zero (gas: %d > refund: %d)", gas, s.refund), "tx", s.thash.String())
		s.parallel.needsRedo = true
		return
	}
	s.refund -= gas
}

// For Parallel Execution Mode, it can be seen as Penetrated Access:
//
//	-------------------------------------------------------
//	| BaseTxIndex | Unconfirmed Txs... | Current TxIndex |
//	-------------------------------------------------------
//
// Access from the unconfirmed DB with range&priority:  txIndex -1(previous tx) -> baseTxIndex + 1
func (s *ParallelStateDB) getBalanceFromUnconfirmedDB(addr common.Address) *big.Int {
	for i := s.txIndex - 1; i >= 0 && i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)
		// 1.Refer the state of address, exist or not in dirtiedStateObjectsInSlot
		balanceHit := false
		if _, exist := db.parallel.addrStateChangesInSlot[addr]; exist {
			balanceHit = true
		}
		if _, exist := db.parallel.balanceChangesInSlot[addr]; exist { // only changed balance is reliable
			balanceHit = true
		}
		if !balanceHit {
			continue
		}
		obj := db.parallel.dirtiedStateObjectsInSlot[addr]
		balance := obj.Balance()
		if obj.deleted {
			balance = common.Big0
		}
		return balance

	}
	return nil
}

// Similar to getBalanceFromUnconfirmedDB
func (s *ParallelStateDB) getNonceFromUnconfirmedDB(addr common.Address) (uint64, bool) {
	for i := s.txIndex - 1; i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)

		nonceHit := false

		if _, ok := db.parallel.addrStateChangesInSlot[addr]; ok {
			nonceHit = true
		} else if _, ok := db.parallel.nonceChangesInSlot[addr]; ok {
			nonceHit = true
		}
		if !nonceHit {
			// nonce refer not hit, try next unconfirmedDb
			continue
		}
		// nonce hit, return the nonce
		obj := db.parallel.dirtiedStateObjectsInSlot[addr]
		if obj == nil {
			// could not exist, if it is changed but reverted
			// fixme: revert should remove the change record
			log.Debug("Get nonce from UnconfirmedDB, changed but object not exist, ",
				"txIndex", s.txIndex, "referred txIndex", i, "addr", addr)
			continue
		}
		nonce := obj.Nonce()
		// deleted object with nonce == 0
		if obj.deleted {
			nonce = 0
		}
		return nonce, true
	}
	return 0, false
}

// Similar to getBalanceFromUnconfirmedDB
// It is not only for code, but also codeHash and codeSize, we return the *stateObject for convenience.
func (s *ParallelStateDB) getCodeFromUnconfirmedDB(addr common.Address) ([]byte, bool) {
	for i := s.txIndex - 1; i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)

		codeHit := false
		if _, exist := db.parallel.addrStateChangesInSlot[addr]; exist {
			codeHit = true
		}
		if _, exist := db.parallel.codeChangesInSlot[addr]; exist {
			codeHit = true
		}
		if !codeHit {
			// try next unconfirmedDb
			continue
		}
		obj := db.parallel.dirtiedStateObjectsInSlot[addr]
		if obj == nil {
			// could not exist, if it is changed but reverted
			// fixme: revert should remove the change record
			log.Debug("Get code from UnconfirmedDB, changed but object not exist, ",
				"txIndex", s.txIndex, "referred txIndex", i, "addr", addr)
			continue
		}
		code := obj.Code()
		if obj.deleted {
			code = nil
		}
		return code, true

	}
	return nil, false
}

// Similar to getCodeFromUnconfirmedDB
// but differ when address is deleted or not exist
func (s *ParallelStateDB) getCodeHashFromUnconfirmedDB(addr common.Address) (common.Hash, bool) {
	for i := s.txIndex - 1; i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)
		hashHit := false
		if _, exist := db.parallel.addrStateChangesInSlot[addr]; exist {
			hashHit = true
		}
		if _, exist := db.parallel.codeChangesInSlot[addr]; exist {
			hashHit = true
		}
		if !hashHit {
			// try next unconfirmedDb
			continue
		}
		obj := db.parallel.dirtiedStateObjectsInSlot[addr]
		if obj == nil {
			// could not exist, if it is changed but reverted
			// fixme: revert should remove the change record
			log.Debug("Get codeHash from UnconfirmedDB, changed but object not exist, ",
				"txIndex", s.txIndex, "referred txIndex", i, "addr", addr)
			continue
		}
		codeHash := common.Hash{}
		if !obj.deleted {
			codeHash = common.BytesToHash(obj.CodeHash())
		}
		return codeHash, true
	}
	return common.Hash{}, false
}

// Similar to getCodeFromUnconfirmedDB
// It is for address state check of: Exist(), Empty() and HasSuicided()
// Since the unconfirmed DB should have done Finalise() with `deleteEmptyObjects = true`
// If the dirty address is empty or suicided, it will be marked as deleted, so we only need to return `deleted` or not.
func (s *ParallelStateDB) getAddrStateFromUnconfirmedDB(addr common.Address) (bool, bool) {
	// check the unconfirmed DB with range:  baseTxIndex -> txIndex -1(previous tx)
	for i := s.txIndex - 1; i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)
		if exist, ok := db.parallel.addrStateChangesInSlot[addr]; ok {
			if obj, ok := db.parallel.dirtiedStateObjectsInSlot[addr]; !ok {
				// could not exist, if it is changed but reverted
				// fixme: revert should remove the change record
				log.Debug("Get addr State from UnconfirmedDB, changed but object not exist, ",
					"txIndex", s.txIndex, "referred txIndex", i, "addr", addr)
				continue
			} else {
				if obj.selfDestructed || obj.deleted {
					return false, true
				}
			}

			return exist, true
		}
	}
	return false, false
}

func (s *ParallelStateDB) getKVFromUnconfirmedDB(addr common.Address, key common.Hash) (common.Hash, bool) {
	// check the unconfirmed DB with range:  baseTxIndex -> txIndex -1(previous tx)
	for i := s.txIndex - 1; i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)
		if _, ok := db.parallel.kvChangesInSlot[addr]; ok {
			obj := db.parallel.dirtiedStateObjectsInSlot[addr]
			if val, exist := obj.dirtyStorage.GetValue(key); exist {
				return val, true
			}
		}
	}
	return common.Hash{}, false
}

func (s *ParallelStateDB) GetStateObjectFromUnconfirmedDB(addr common.Address) (*stateObject, bool) {
	return s.getStateObjectFromUnconfirmedDB(addr)
}

func (s *ParallelStateDB) getStateObjectFromUnconfirmedDB(addr common.Address) (*stateObject, bool) {
	// check the unconfirmed DB with range:  baseTxIndex -> txIndex -1(previous tx)
	for i := s.txIndex - 1; i > s.BaseTxIndex(); i-- {
		db_, ok := s.parallel.unconfirmedDBs.Load(i)
		if !ok {
			continue
		}
		db := db_.(*ParallelStateDB)
		if obj, ok := db.parallel.dirtiedStateObjectsInSlot[addr]; ok {
			return obj, true
		}
	}
	return nil, false
}

// IsParallelReadsValid If stage2 is true, it is a likely conflict check,
// to detect these potential conflict results in advance and schedule redo ASAP.
func (slotDB *ParallelStateDB) IsParallelReadsValid(isStage2 bool) bool {
	parallelKvOnce.Do(func() {
		StartKvCheckLoop()
	})

	mainDB := slotDB.parallel.baseStateDB
	// for nonce
	for addr, nonceSlot := range slotDB.parallel.nonceReadsInSlot {
		if isStage2 { // update slotDB's unconfirmed DB list and try
			if nonceUnconfirm, ok := slotDB.getNonceFromUnconfirmedDB(addr); ok {
				if nonceSlot != nonceUnconfirm {
					log.Debug("IsSlotDBReadsValid nonce read is invalid in unconfirmed", "addr", addr,
						"nonceSlot", nonceSlot, "nonceUnconfirm", nonceUnconfirm, "SlotIndex", slotDB.parallel.SlotIndex,
						"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
					return false
				}
			}
		}
		nonceMain := mainDB.GetNonce(addr)
		if nonceSlot != nonceMain {
			log.Debug("IsSlotDBReadsValid nonce read is invalid", "addr", addr,
				"nonceSlot", nonceSlot, "nonceMain", nonceMain, "SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
	}
	// balance
	for addr, balanceSlot := range slotDB.parallel.balanceReadsInSlot {
		if isStage2 { // update slotDB's unconfirmed DB list and try
			if balanceUnconfirm := slotDB.getBalanceFromUnconfirmedDB(addr); balanceUnconfirm != nil {
				if balanceSlot.Cmp(balanceUnconfirm) == 0 {
					continue
				}
				return false
			}
		}

		balanceMain := mainDB.GetBalance(addr)
		if balanceSlot.Cmp(balanceMain) != 0 {
			log.Debug("IsSlotDBReadsValid balance read is invalid", "addr", addr,
				"balanceSlot", balanceSlot, "balanceMain", balanceMain, "SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
	}
	// check KV
	var units []ParallelKvCheckUnit // todo: pre-allocate to make it faster
	for addr, read := range slotDB.parallel.kvReadsInSlot {
		read.Range(func(keySlot, valSlot interface{}) bool {
			units = append(units, ParallelKvCheckUnit{addr, keySlot.(common.Hash), valSlot.(common.Hash)})
			return true
		})
	}
	readLen := len(units)
	// TODO-dav: change back to 8 or 1?
	if readLen < 80000 || isStage2 {
		for _, unit := range units {
			if hasKvConflict(slotDB, unit.addr, unit.key, unit.val, isStage2) {
				return false
			}
		}
	} else {
		msgHandledNum := 0
		msgSendNum := 0
		for _, unit := range units {
			for { // make sure the unit is consumed
				consumed := false
				select {
				case conflict := <-parallelKvCheckResCh:
					msgHandledNum++
					if conflict {
						// make sure all request are handled or discarded
						for {
							if msgHandledNum == msgSendNum {
								break
							}
							select {
							case <-parallelKvCheckReqCh:
								msgHandledNum++
							case <-parallelKvCheckResCh:
								msgHandledNum++
							}
						}
						return false
					}
				case parallelKvCheckReqCh <- ParallelKvCheckMessage{slotDB, isStage2, unit}:
					msgSendNum++
					consumed = true
				}
				if consumed {
					break
				}
			}
		}
		for {
			if msgHandledNum == readLen {
				break
			}
			conflict := <-parallelKvCheckResCh
			msgHandledNum++
			if conflict {
				// make sure all request are handled or discarded
				for {
					if msgHandledNum == msgSendNum {
						break
					}
					select {
					case <-parallelKvCheckReqCh:
						msgHandledNum++
					case <-parallelKvCheckResCh:
						msgHandledNum++
					}
				}
				return false
			}
		}
	}
	if isStage2 { // stage2 skip check code, or state, since they are likely unchanged.
		return true
	}

	// check code
	for addr, codeSlot := range slotDB.parallel.codeReadsInSlot {
		codeMain := mainDB.GetCode(addr)
		if !bytes.Equal(codeSlot, codeMain) {
			log.Debug("IsSlotDBReadsValid code read is invalid", "addr", addr,
				"len codeSlot", len(codeSlot), "len codeMain", len(codeMain), "SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
	}
	// check codeHash
	for addr, codeHashSlot := range slotDB.parallel.codeHashReadsInSlot {
		codeHashMain := mainDB.GetCodeHash(addr)
		if !bytes.Equal(codeHashSlot.Bytes(), codeHashMain.Bytes()) {
			log.Debug("IsSlotDBReadsValid codehash read is invalid", "addr", addr,
				"codeHashSlot", codeHashSlot, "codeHashMain", codeHashMain, "SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
	}
	// addr state check
	for addr, stateSlot := range slotDB.parallel.addrStateReadsInSlot {
		stateMain := false // addr not exist
		if mainDB.getStateObject(addr) != nil {
			stateMain = true // addr exist in main DB
		}
		if stateSlot != stateMain {
			log.Debug("IsSlotDBReadsValid addrState read invalid(true: exist, false: not exist)",
				"addr", addr, "stateSlot", stateSlot, "stateMain", stateMain,
				"SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
	}
	// snapshot destructs check
	for addr, destructRead := range slotDB.parallel.addrSnapDestructsReadsInSlot {
		mainObj := mainDB.getStateObject(addr)
		if mainObj == nil {
			log.Debug("IsSlotDBReadsValid snapshot destructs read invalid, address should exist",
				"addr", addr, "destruct", destructRead,
				"SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
		slotDB.snapParallelLock.RLock()               // fixme: this lock is not needed
		_, destructMain := mainDB.snapDestructs[addr] // addr not exist
		slotDB.snapParallelLock.RUnlock()
		if destructRead != destructMain {
			log.Debug("IsSlotDBReadsValid snapshot destructs read invalid",
				"addr", addr, "destructRead", destructRead, "destructMain", destructMain,
				"SlotIndex", slotDB.parallel.SlotIndex,
				"txIndex", slotDB.txIndex, "baseTxIndex", slotDB.parallel.baseTxIndex)
			return false
		}
	}

	return true
}

// NeedsRedo returns true if there is any clear reason that we need to redo this transaction
func (s *ParallelStateDB) NeedsRedo() bool {
	return s.parallel.needsRedo
}
