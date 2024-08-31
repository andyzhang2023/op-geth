package state

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

var Address1 = common.HexToAddress("0x1")
var Address2 = common.HexToAddress("0x2")

// TestUncommitedDBCreateAccount tests the creation of an account in an uncommited DB.
func TestUncommitedDBCreateAccount(t *testing.T) {
	// case 1. create an account without previous state
	txs := Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
		},
	}
	check := CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(100)},
			{"nonce", Address1, 1},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

	// case 2. create an account with previous state
	txs = Txs{
		// previous state
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(200)},
		},
		// create a new account, should have a balance of 100, and nonce = 0
		{
			{"Create", Address1},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(200)},
			{"nonce", Address1, 0},
		},
	}
	if er := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); er != nil {
		t.Fatalf("ut failed, err=%s", er.Error())
	}
}

func TestAddBalance(t *testing.T) {
	// case 1. add balance to an account without previous state
	txs := Txs{
		{
			{"AddBalance", Address1, big.NewInt(100)},
		},
	}
	check := CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(100)},
			{"nonce", Address1, 0},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

}

func TestSubBalance(t *testing.T) {
	txs := Txs{
		{
			{"AddBalance", Address1, big.NewInt(100)},
			{"SubBalance", Address1, big.NewInt(100)},
		},
	}
	check := CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(0)},
			{"nonce", Address1, 2},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

	// sub balance to an account without previous state
	txs = Txs{
		{
			{"SubBalance", Address1, big.NewInt(100)},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(-100)},
			{"nonce", Address1, 1},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

}

func TestSetCode(t *testing.T) {
	// case 1. set code to an account without previous state
	txs := Txs{
		{
			{"SetCode", Address1, []byte("hello")},
		},
	}
	check := CheckState{
		AfterMerge: []Check{
			{"code", Address1, []byte("hello")},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// case 2.
	txs = Txs{
		{
			{"Create", Address1},
			{"SetCode", Address1, []byte("hello")},
			{"AddBalance", Address1, big.NewInt(100)},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"code", Address1, []byte("hello")},
			{"balance", Address1, big.NewInt(100)},
			{"nonce", Address1, 3},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestSetState(t *testing.T) {
	// 1. set state to an account without previous state, check getState, getCommittedState
	txs := Txs{
		{
			{"SetState", Address1, "key1", "value1"},
			{"SetState", Address1, "key2", "value2"},
		},
	}
	check := CheckState{
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"state", Address1, "key1", "value1"},
				{"state", Address1, "key2", "value2"},
				{"nonce", 2},
			},
			Maindb: []Check{
				{"obj", "nil"},
			},
		},
		AfterMerge: []Check{
			{"state", Address1, "key1", "value1"},
			{"state", Address1, "key2", "value2"},
			{"state", Address1, "key3", ""},
			{"nonce", Address1, 2},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// 2. set state to an account with previous state, check getState, getCommittedState
	txs = Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SetState", Address1, "key1", "value1"},
			{"SetState", Address1, "key2", "value2"},
		},
	}
	check = CheckState{
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"state", Address1, "key1", "value1"},
				{"state", Address1, "key2", "value2"},
				{"cstate", Address1, "key1", ""},
				{"cstate", Address1, "key2", ""},
				{"nonce", 3},
			},
			Maindb: []Check{
				{"balance", big.NewInt(100)},
				{"nonce", 1},
				{"state", Address1, "key1", ""},
				{"state", Address1, "key2", ""},
				{"cstate", Address1, "key1", ""},
				{"cstate", Address1, "key2", ""},
			},
		},
		AfterMerge: []Check{
			{"state", Address1, "key1", "value1"},
			{"state", Address1, "key2", "value2"},
			{"cstate", Address1, "key1", "value1"},
			{"cstate", Address1, "key2", "value2"},
			{"state", Address1, "key3", ""},
			{"nonce", Address1, 3},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestSelfDestruct(t *testing.T) {
	// case 1. self destruct an account without previous state
	txs := Txs{
		{
			{"SelfDestruct", Address1},
		},
	}
	check := CheckState{
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"obj", Address1, "nil"},
			},
			Maindb: []Check{
				{"obj", Address1, "nil"},
			},
		},
		AfterMerge: []Check{
			{"obj", Address1, "nil"},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// case 2. self destruct an account with previous state
	txs = Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SelfDestruct", Address1},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(0)},
			{"nonce", Address1, 0},
			{"obj", Address1, "nil"},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// case 3. create an account after self destruct
	txs = Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SelfDestruct", Address1},
			{"Create", Address1},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(100)},
			{"nonce", Address1, 0},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestSelfDestruct6780(t *testing.T) {
	// what's it used for ?
}

func TestExistsAndEmpty(t *testing.T) {
	// case 1. exists an account without previous state
	txs := Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"exists", Address1, false},
				{"empty", Address1, true},
			},
			Maindb: []Check{
				{"exists", Address1, false},
				{"empty", Address1, true},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"exists", Address1, true},
				{"empty", Address1, false},
			},
			Maindb: []Check{
				{"exists", Address1, false},
				{"empty", Address1, true},
				{"obj", "nil"},
			},
		},
		AfterMerge: []Check{
			{"empty", Address1, false},
			{"exists", Address1, true},
			{"balance", Address1, big.NewInt(100)},
			{"nonce", Address1, 1},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestRefund(t *testing.T) {
	// case 1. add refund from 0
	txs := Txs{
		{
			{"AddRefund", 100},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"refund", uint64(0)},
			},
			Maindb: []Check{
				{"refund", uint64(0)},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"refund", uint64(100)},
			},
			Maindb: []Check{
				{"refund", uint64(0)},
			},
		},
		AfterMerge: []Check{
			{"refund", uint64(100)},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

	// case 2. add refund to a new account
	statedb := newStateDB()
	unstateMain := newStateDB()
	unstateMain.refund, statedb.refund = 100, 100
	txs = Txs{
		{
			{"AddRefund", 100},
			{"SubRefund", 20},
		},
	}
	check = CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"refund", uint64(100)},
			},
			Maindb: []Check{
				{"refund", uint64(100)},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"refund", uint64(180)},
			},
			Maindb: []Check{
				{"refund", uint64(100)},
			},
		},
		AfterMerge: []Check{
			{"refund", uint64(180)},
		},
	}
	if err := runCase(txs, statedb, newUncommittedDB(unstateMain), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestTransientStorage(t *testing.T) {
	// case 1. set transient storage
	txs := Txs{
		{
			{"SetTransientStorage", Address1, "key1", "value1"},
			{"SetTransientStorage", Address1, "key2", "value2"},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"tstorage", Address1, "key1", ""},
				{"tstorage", Address1, "key2", ""},
			},
			Maindb: []Check{
				{"tstorage", Address1, "key1", ""},
				{"tstorage", Address1, "key2", ""},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"tstorage", Address1, "key1", "value1"},
				{"tstorage", Address1, "key2", "value2"},
			},
			Maindb: []Check{
				{"tstorage", Address1, "key1", ""},
				{"tstorage", Address1, "key2", ""},
			},
		},
		AfterMerge: []Check{
			{"tstorage", Address1, "key1", "value1"},
			{"tstorage", Address1, "key2", "value2"},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPreimages(t *testing.T) {
	txs := Txs{
		{
			{"AddPreimage", common.BytesToHash([]byte("hello")), []byte("world")},
			{"AddPreimage", common.BytesToHash([]byte("hello")), []byte("world2")}, // the second one should be ignored
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"preimage", common.BytesToHash([]byte("hello")), []byte("")},
			},
			Maindb: []Check{
				{"preimage", common.BytesToHash([]byte("hello")), []byte("")},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"preimage", common.BytesToHash([]byte("hello")), []byte("world")},
			},
			Maindb: []Check{
				{"preimage", common.BytesToHash([]byte("hello")), []byte("")},
			},
		},
		AfterMerge: []Check{
			{"preimage", common.BytesToHash([]byte("hello")), []byte("world")},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestLogs(t *testing.T) {
	// diff logs of two stateDBs:
	//	1. check thash
	//  2. check index
	//  3. check txIndex
	// log.thash, log.index, log.txIndex = s.txHash, s.logSize, s.txIndex
	// so we need to test it by running a true transaction, in which we call the log function
	txs := Txs{
		{
			{"AddLog", &types.Log{Data: []byte("hello")}},
			{"AddLog", &types.Log{Data: []byte("world")}},
		},
	}
	thash := common.BytesToHash([]byte("00000000000000000tx"))
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"loglen", 0},
			},
			Maindb: []Check{
				{"loglen", 0},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"loglen", 1},
				{"log", thash, 0, []byte("hello"), 20, 0},
				{"log", thash, 1, []byte("world"), 20, 1},
			},
			Maindb: []Check{
				{"loglen", 0},
			},
		},
		AfterMerge: []Check{
			{"loglen", 1},
			{"log", thash, 0, []byte("hello"), 20, 0},
			{"log", thash, 1, []byte("world"), 20, 1},
		},
	}
	state := newStateDB()
	maindb := newStateDB()
	state.txIndex, maindb.txIndex = 20, 20
	state.thash, maindb.thash = thash, thash
	unstate := newUncommittedDB(maindb)
	if err := runCase(txs, state, unstate, check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestAccessList(t *testing.T) {
	// accesslist will be copy from maindb into uncommited db
	// accesslist is modified in the uncommited db' cache
	// accesslist is merged back into maindb
}

// ================== conflict test ==================
// case 1. conflict in objects: balance, nonce, code, state
// case 2. conflict in transient storage
// case 3. conflict in accesslist
// case 4. conflict in logs
func TestConflictObject(t *testing.T) {
	// case 1. conflict in balance
	// case 2. conflict in nonce
	// case 3. conflict in code
	// case 4. conflict in state

	// case of balance
	prepare := Txs{
		{
			{"SetBalance", Address1, 10},
		},
	}
	add40 := Txs{
		{
			{"AddBalance", Address1, big.NewInt(40)},
		},
	}
	add50 := Txs{
		{
			{"AddBalance", Address1, big.NewInt(50)},
		},
	}
	check := []Check{
		{"balance", big.NewInt(40)},
	}
	if err := runConflictCase(nil, add40, add50, check); err != nil {
		t.Fatalf("ut failed, errr:%s", err.Error())
	}
	if err := runConflictCase(prepare, add40, add50, check); err != nil {
		t.Fatalf("ut failed, errr:%s", err.Error())
	}

	// case of nonce
	prepare = Txs{
		{
			{"SetNonce", Address1, 10},
		},
	}
	txs := Txs{
		{
			{"SetNonce", Address1, 21},
		},
	}
	txs2 := Txs{
		{
			{"SetNonce", Address1, 22},
		},
	}
	check = []Check{
		{"nonce", 21},
	}
	if err := runConflictCase(nil, txs, txs2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(prepare, txs, txs2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

	// case of code
	prepare = Txs{
		{
			{"SetCode", []byte("hello")},
		},
	}
	op1 := Txs{
		{
			{"SetCode", []byte("here we go")},
		},
	}
	op2 := Txs{
		{
			{"SetCode", []byte("here we go now")},
		},
	}
	check = []Check{
		{"code", []byte("here we go")},
	}
	if err := runConflictCase(nil, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(prepare, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

	// case of state
	prepare = Txs{
		{
			{"SetState", "key1", "value1"},
			{"SetState", "key2", "value2"},
		},
	}
	op1 = Txs{
		{
			{"SetState", "key2", "value2.22"},
			{"SetState", "key3", "value2.33"},
		},
	}
	op2 = Txs{
		{
			{"SetState", "key2", "value3.22"},
			{"SetState", "key1", "value3.11"},
		},
	}
	check = []Check{
		{"state", "key2", "value3.22"},
		{"state", "key1", "value3.11"},
		{"state", "key3", ""},
	}
	if err := runConflictCase(prepare, op2, op1, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(nil, op2, op1, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

}
func TestConflictTransientStorage(t *testing.T) {
	prepare := Txs{
		{
			{"SetTransient", Address1, "key1", "value1"},
			{"SetTransient", Address1, "key2", "value2"},
		},
	}
	op1 := Txs{
		{
			{"SetTransient", Address1, "key1", "value11"},
			{"SetTransient", Address1, "key2", "value2"},
		},
	}
	op2 := Txs{
		{
			{"SetTransient", Address1, "key1", "value11"},
			{"SetTransient", Address1, "key3", "value33"},
		},
	}
	check := []Check{
		{"transient", Address1, "key1", "value11"},
		{"transient", Address1, "key2", "value2"},
		{"transient", Address1, "key3", ""},
	}
	if err := runConflictCase(prepare, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(nil, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
}

func TestConflictAccessList(t *testing.T) {
	// case 1. add address
	// case 2. add slot

	// case "add address"
	prepare := Txs{
		{
			{"AddAddress", Address2},
			{"AddSlots", Address2, "key1"},
		},
	}
	op1 := Txs{
		{
			{"AddAddress", Address1},
		},
	}
	op2 := Txs{
		{
			{"AddAddress", Address1},
		},
	}
	check := []Check{
		{"address", Address1, true},
	}
	if err := runConflictCase(nil, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(prepare, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

	// case "add slot"
	op1 = Txs{
		{
			{"AddSlot", Address2, "key1"},
			{"AddSlot", Address2, "key2"},
		},
	}
	op2 = Txs{
		{
			{"AddSlot", Address2, "key2"},
			{"AddSlot", Address2, "key3"},
		},
	}
	check = []Check{
		{"address", Address2, true},
		{"address", Address1, true},
		{"slot", Address1, "key1", true},
		{"slot", Address1, "key2", true},
		{"slot", Address1, "key3", false},
	}
	if err := runConflictCase(nil, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(prepare, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
}

func TestConflictLogs(t *testing.T) {
	thash := common.BytesToHash([]byte("tx1"))
	prepare := Txs{
		{
			{"AddLog", &types.Log{Data: []byte("hello")}},
			{"setTxHash", thash},
			{"setTxIndex", 0},
		},
	}
	op1 := Txs{
		{
			{"AddLog", &types.Log{Data: []byte("world")}},
		},
	}
	op2 := Txs{
		{
			{"AddLog", &types.Log{Data: []byte("eth")}},
		},
	}
	check := []Check{
		{"logSize", 2},
		{"log", 0, []byte("hello")},
		{"log", 1, []byte("world")},
	}
	if err := runConflictCase(prepare, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

}

func TestConflictPreimage(t *testing.T) {
	op1 := Txs{
		{
			{"AddPreimage", "key1", []byte("value1")},
		},
	}
	op2 := Txs{
		{
			{"AddPreimage", "key1", []byte("value2")},
		},
	}
	check := []Check{
		{"preimage", "key1", []byte("value1")},
	}
	if err := runConflictCase(nil, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
}

func TestReverAndSnapshot(t *testing.T) {
	// is it necessary to do snapshot and revert ?
}

func TestMergeWithDestruct(t *testing.T) {
	// this is a special case for merging with some selfDestruct op in the transaction
	//  1. CreateAccount
	//  2. AddBalance(100), SetNonce(1)
	//  3. SelfDestruct()
	//  4. SelfDestruct()
}

type uncommitedState struct {
	Uncommited []Check
	Maindb     []Check
}

type CheckState struct {
	BeforeRun   uncommitedState
	BeforeMerge uncommitedState
	AfterMerge  []Check
}

type Check []interface{}

func (c Check) Verify(state vm.StateDB) error {
	return nil
}

type Op []interface{}

type Tx []Op

// Call executes the transaction.
func (tx Tx) Call(db vm.StateDB) error {
	return nil
}

type Txs []Tx

func (txs Txs) Call(db vm.StateDB) error {
	return nil
}

func newStateDB() *StateDB {
	return &StateDB{}
}

func newUncommittedDB(db *StateDB) *UncommittedDB {
	return &UncommittedDB{}
}

func exampleRun(t *testing.T) {
}

func runTxsOnStateDB(txs Txs, db *StateDB) (common.Hash, error) {
	// run the transactions
	if err := txs.Call(db); err != nil {
		return common.Hash{}, fmt.Errorf("state failed to run txs: %v", err)
	}
	db.Finalise(true)
	return db.trie.Hash(), nil
}

func runTxOnUncommittedDB(txs Txs, db *UncommittedDB, check CheckState) (common.Hash, error) {
	// run the transaction
	if err := txs.Call(db); err != nil {
		return common.Hash{}, fmt.Errorf("unconfirm db failed to run txs: %v", err)
	}
	for _, check := range check.BeforeMerge.Uncommited {
		if err := check.Verify(db); err != nil {
			return common.Hash{}, fmt.Errorf("failed to verify uncommited db: %v", err)
		}
	}
	for _, check := range check.BeforeMerge.Maindb {
		if err := check.Verify(db.maindb); err != nil {
			return common.Hash{}, fmt.Errorf("failed to verify main db: %v", err)
		}
	}
	if err := db.Merge(); err != nil {
		return common.Hash{}, fmt.Errorf("failed to merge: %v", err)
	}
	db.maindb.Finalise(true)
	return db.maindb.trie.Hash(), nil
}

func runConflictCase(prepare, txs1, txs2 Txs, checks []Check) error {
	maindb := newStateDB()
	maindb.CreateAccount(Address1)
	for _, op := range prepare {
		if err := op.Call(maindb); err != nil {
			return fmt.Errorf("failed to call prepare txs, err:%s", err.Error())
		}
	}
	un1, un2 := newUncommittedDB(maindb), newUncommittedDB(maindb)
	if err := txs1.Call(un1); err != nil {
		return fmt.Errorf("failed to call txs1, err:%s", err.Error())
	}
	if err := txs2.Call(un2); err != nil {
		return fmt.Errorf("failed to call txs2, err:%s", err.Error())
	}
	if err := un1.Merge(); err != nil {
		return fmt.Errorf("failed to merge un1, err:%s", err.Error())
	}
	if err := un2.Merge(); err == nil {
		return fmt.Errorf("un2 merge is expected to be failed")
	}
	for _, c := range checks {
		if err := c.Verify(maindb); err != nil {
			return fmt.Errorf("failed to verify maindb, err:%s", err.Error())
		}
	}
	return nil
}

func runCase(txs Txs, state *StateDB, unstate *UncommittedDB, check CheckState) error {
	stRoot, errSt := runTxsOnStateDB(txs, state)
	unRoot, errUn := runTxOnUncommittedDB(txs, unstate, check)
	if errSt != nil {
		return fmt.Errorf("failed to run txs on main db: %v", errSt)
	}
	if errUn != nil {
		return fmt.Errorf("failed to run tx on uncommited db: %v", errUn)
	}
	if stRoot.Cmp(unRoot) != 0 {
		return fmt.Errorf("state root mismatch: %s != %s", stRoot.String(), unRoot.String())
	}
	for _, check := range check.AfterMerge {
		if err := check.Verify(state); err != nil {
			return fmt.Errorf("failed to verify main db: %v", err)
		}
		if err := check.Verify(unstate); err != nil {
			return fmt.Errorf("failed to verify uncommited db: %v", err)
		}
	}
	return nil
}

func Diff(a, b *StateDB) []common.Hash {
	// compare the two stateDBs, and return the different objects
	return nil
}
