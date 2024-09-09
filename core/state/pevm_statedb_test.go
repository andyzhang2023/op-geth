package state

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/triedb/hashdb"
	"github.com/ethereum/go-ethereum/trie/triedb/pathdb"
)

var Address1 = common.HexToAddress("0x1")
var Address2 = common.HexToAddress("0x2")

// TestUncommitedDBCreateAccount tests the creation of an account in an uncommited DB.
func TestPevmUncommitedDBCreateAccount(t *testing.T) {
	// case 1. create an account without previous state
	txs := Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SetNonce", Address1, 2},
		},
	}
	check := CheckState{
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"balance", Address1, big.NewInt(100)},
				{"nonce", Address1, 2},
			},
			Maindb: []Check{
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 0},
			},
		},
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(100)},
			{"nonce", Address1, 2},
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
			{"SetNonce", Address1, 2},
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

func TestPevmAddBalance(t *testing.T) {
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

func TestPevmSubBalance(t *testing.T) {
	txs := Txs{
		{
			{"AddBalance", Address1, big.NewInt(120)},
			{"SubBalance", Address1, big.NewInt(100)},
			{"SetNonce", Address1, 2},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 0},
			},
			Maindb: []Check{
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 0},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"balance", Address1, big.NewInt(20)},
				{"nonce", Address1, 2},
			},
			Maindb: []Check{
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 0},
			},
		},
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(20)},
			{"nonce", Address1, 2},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

}

func TestPevmSetCode(t *testing.T) {
	// case 1. set code to an account without previous state
	txs := Txs{
		{
			{"SetCode", Address1, []byte("hello")},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"code", Address1, []byte("")},
			},
			Maindb: []Check{
				{"code", Address1, []byte("")},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"code", Address1, []byte("hello")},
			},
			Maindb: []Check{
				{"code", Address1, []byte("")},
			},
		},
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
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPevmSetState(t *testing.T) {
	// 1. set state to an account without previous state, check getState, getCommittedState
	txs := Txs{
		{
			{"SetState", Address1, "key1", "value1"},
			{"SetState", Address1, "key2", "value2"},
			{"SetNonce", Address1, 1}, // to avoid the deletion when finalise
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"state", Address1, "key1", ""},
				{"state", Address1, "key2", ""},
				{"obj", Address1, "nil"},
			},
			Maindb: []Check{
				{"state", Address1, "key1", ""},
				{"state", Address1, "key2", ""},
				{"obj", Address1, "nil"},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"state", Address1, "key1", "value1"},
				{"state", Address1, "key2", "value2"},
				{"obj", Address1, "exists"},
			},
			Maindb: []Check{
				{"state", Address1, "key1", ""},
				{"state", Address1, "key2", ""},
				{"obj", Address1, "nil"},
			},
		},
		AfterMerge: []Check{
			{"state", Address1, "key1", "value1"},
			{"state", Address1, "key2", "value2"},
			{"cstate", Address1, "key1", "value1"},
			{"cstate", Address1, "key2", "value2"},
			{"obj", Address1, "exists"},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// 1. set state to an account without previous state, check getState, getCommittedState
	prepare := Txs{
		{
			{"Create", Address1},
			{"SetState", Address1, "key0", "value0"},
			{"SetState", Address1, "key1", "valuea"},
			{"AddBalance", Address1, big.NewInt(100)}, // to avoid the deletion when finalise
		},
	}
	statedb := newStateDB()
	prepare.Call(statedb)
	sroot := statedb.IntermediateRoot(true)

	maindb := newStateDB()
	prepare.Call(maindb)
	proot := maindb.IntermediateRoot(true)
	if sroot.Cmp(proot) != 0 {
		t.Fatalf("ut failed, err=%s", "roots mismatch")
	}
	uncommitedDB := newUncommittedDB(maindb)

	txs = Txs{
		{
			{"SetState", Address1, "key1", "value1"},
			{"SetState", Address1, "key2", "value2"},
		},
	}
	check = CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"state", Address1, "key1", "valuea"},
				{"state", Address1, "key2", ""},
				{"cstate", Address1, "key1", "valuea"},
				{"cstate", Address1, "key0", "value0"},
				{"obj", Address1, "exists"},
			},
			Maindb: []Check{
				{"state", Address1, "key1", "valuea"},
				{"state", Address1, "key2", ""},
				{"cstate", Address1, "key1", "valuea"},
				{"cstate", Address1, "key0", "value0"},
				{"obj", Address1, "exists"},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"state", Address1, "key1", "value1"},
				{"state", Address1, "key2", "value2"},
				{"obj", Address1, "exists"},
			},
			Maindb: []Check{
				{"obj", Address1, "exists"},
				{"state", Address1, "key1", "valuea"},
				{"state", Address1, "key2", ""},
			},
		},
		AfterMerge: []Check{
			{"state", Address1, "key0", "value0"},
			{"state", Address1, "key1", "value1"},
			{"state", Address1, "key2", "value2"},
			{"cstate", Address1, "key0", "value0"},
			{"cstate", Address1, "key1", "value1"},
			{"cstate", Address1, "key2", "value2"},
			{"obj", Address1, "exists"},
			{"balance", Address1, big.NewInt(100)},
		},
	}
	if err := runCase(txs, statedb, uncommitedDB, check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPevmSelfDestructStateDB(t *testing.T) {
	//1. create address1, set code, set balance, set nonce
	//2. self destruct address1
	//3. check the state of address1 before finalize
	//4. finalize the stateDB
	//5. check the state of address1 after finalize
	statedb := newStateDB()
	uncommitedState := newUncommittedDB(newStateDB())
	prepare := Txs{
		{
			{"Create", Address1},
			{"SetNonce", Address1, 12},
			{"SetCode", Address1, []byte("hello world")},
			{"AddBalance", Address1, big.NewInt(100)},
		},
	}
	prepare.Call(statedb)
	prepare.Call(uncommitedState)
	checks := Checks{
		{"code", Address1, []byte("hello world")},
		{"balance", Address1, big.NewInt(100)},
		{"nonce", Address1, 12},
	}
	if err := checks.Verify(statedb); err != nil {
		t.Fatalf("unexpected prepared state, err=%s", err.Error())
	}
	if err := checks.Verify(uncommitedState); err != nil {
		t.Fatalf("unexpected prepared state, err=%s", err.Error())
	}
	destruct := Txs{
		{
			{"SelfDestruct", Address1},
		},
	}
	destruct.Call(statedb)
	destruct.Call(uncommitedState)
	// check the state of address1 before finalise: they should keep the same except the balance
	checks = Checks{
		{"code", Address1, []byte("hello world")},
		{"balance", Address1, big.NewInt(0)},
		{"nonce", Address1, 12},
	}
	if err := checks.Verify(statedb); err != nil {
		t.Fatalf("[statedb] unexpected selfdestruct state before finalized, err=%s", err.Error())
	}
	if err := checks.Verify(uncommitedState); err != nil {
		t.Fatalf("[uncommitted] unexpected selfdestruct state before finalized, err=%s", err.Error())
	}
	statedb.Finalise(true)
	uncommitedState.Merge()
	uncommitedState.maindb.Finalise(true)
	checks = Checks{
		{"code", Address1, []byte(nil)},
		{"balance", Address1, big.NewInt(0)},
		{"nonce", Address1, 0},
	}
	// check the state of address1 after finalise: they should be all nil
	if err := checks.Verify(statedb); err != nil {
		t.Fatalf("[statedb] unexpected selfdestruct state after finalized, err=%s", err.Error())
	}
	if err := checks.Verify(uncommitedState.maindb); err != nil {
		t.Fatalf("[uncommitted] unexpected selfdestruct state after finalized, err=%s", err.Error())
	}
	statedb.IntermediateRoot(true)
	uncommitedState.maindb.IntermediateRoot(true)
	// check the state of address1 after finalise: they should be all nil
	checks = Checks{
		{"code", Address1, []byte(nil)},
		{"balance", Address1, big.NewInt(0)},
		{"nonce", Address1, 0},
		{"obj", Address1, "nil"},
	}
	if err := checks.Verify(statedb); err != nil {
		t.Fatalf("[statedb] unexpected selfdestruct state after committed, err=%s", err.Error())
	}
	if err := checks.Verify(uncommitedState.maindb); err != nil {
		t.Fatalf("[unstate.maindb] unexpected selfdestruct state after committed, err=%s", err.Error())
	}
}

func TestPevmSelfDestruct(t *testing.T) {
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
	// case 2. selfdestruct an account who exists in uncommited db but not maindb
	txs = Txs{
		{
			{"Create", Address1},
			{"SetNonce", Address1, 12},
			{"SetCode", Address1, []byte("hello world")},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SelfDestruct", Address1},
		},
	}
	check = CheckState{
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"code", Address1, []byte("hello world")},
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 12},
				{"obj", Address1, "exists"},
			},
			Maindb: []Check{
				{"codeHash", Address1, common.Hash{}}, // an empty hash will be returned if the account does not exist
				{"code", Address1, []byte(nil)},
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 0},
				{"obj", Address1, "nil"},
			},
		},
		AfterMerge: []Check{
			{"codeHash", Address1, common.Hash{}},
			{"code", Address1, []byte(nil)},
			{"balance", Address1, big.NewInt(0)},
			{"nonce", Address1, 0},
			{"obj", Address1, "nil"},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// case 3. destruct an account who exist in maindb but not in uncommited db
	prepare := Txs{
		{
			{"Create", Address1},
			{"SetNonce", Address1, 12},
			{"SetCode", Address1, []byte("hello world")},
			{"AddBalance", Address1, big.NewInt(100)},
		},
	}
	statedb := newStateDB()
	maindb := newStateDB()
	prepare.Call(statedb)
	prepare.Call(maindb)
	UncommittedDB := newUncommittedDB(maindb)
	txs = Txs{
		{
			{"SelfDestruct", Address1},
		},
	}
	check = CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"code", Address1, []byte("hello world")},
				{"balance", Address1, big.NewInt(100)},
				{"nonce", Address1, 12},
				{"obj", Address1, "exists"},
			},
			Maindb: []Check{
				{"code", Address1, []byte("hello world")},
				{"balance", Address1, big.NewInt(100)},
				{"nonce", Address1, 12},
				{"obj", Address1, "exists"},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"code", Address1, []byte("hello world")},
				{"balance", Address1, big.NewInt(0)},
				{"nonce", Address1, 12},
				{"obj", Address1, "exists"},
			},
			Maindb: []Check{
				{"code", Address1, []byte("hello world")},
				{"balance", Address1, big.NewInt(100)},
				{"nonce", Address1, 12},
				{"obj", Address1, "exists"},
			},
		},
		AfterMerge: []Check{
			{"codeHash", Address1, common.Hash{}},
			{"code", Address1, []byte(nil)},
			{"balance", Address1, big.NewInt(0)},
			{"nonce", Address1, 0},
			{"obj", Address1, "nil"},
		},
	}
	if err := runCase(txs, statedb, UncommittedDB, check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// case 3. create an account after self destruct
	txs = Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SelfDestruct", Address1},
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(50)},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(50)},
			{"nonce", Address1, 0},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPevmSelfDestruct6780(t *testing.T) {
	// case 1. no previous state
	txs := Txs{
		{
			{"SelfDestruct6780", Address1},
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
			{"SelfDestruct6780", Address1},
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
			{"SetNonce", Address1, 10},
			{"SelfDestruct6780", Address1},
			{"Create", Address1},
		},
	}
	check = CheckState{
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(0)},
			{"nonce", Address1, 0},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
	// case 4. self destruct 6780 should not work if the account is not empty
	maindb := newStateDB()
	statedb := newStateDB()
	prepare := Txs{
		{
			{"Create", Address1},
			{"AddBalance", Address1, big.NewInt(100)},
			{"SetNonce", Address1, 2},
		},
	}
	prepare.Call(maindb)
	prepare.Call(statedb)
	maindb.IntermediateRoot(true)
	statedb.IntermediateRoot(true)
	txs = Txs{
		{
			{"AddBalance", Address1, big.NewInt(100)},
			{"SelfDestruct6780", Address1},
		},
	}
	check = CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"balance", Address1, big.NewInt(200)},
				{"nonce", Address1, 2},
			},
			Maindb: []Check{
				{"balance", Address1, big.NewInt(200)},
				{"nonce", Address1, 2},
			},
		},
		AfterMerge: []Check{
			{"balance", Address1, big.NewInt(200)},
			{"nonce", Address1, 2},
		},
	}
	if err := runCase(txs, statedb, newUncommittedDB(maindb), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

}

func TestPevmExistsAndEmpty(t *testing.T) {
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
				{"obj", Address1, "nil"},
			},
		},
		AfterMerge: []Check{
			{"empty", Address1, false},
			{"exists", Address1, true},
			{"balance", Address1, big.NewInt(100)},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

	txs = Txs{
		{
			{"Create", Address1},
		},
	}
	check = CheckState{
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
				{"exists", Address1, true}, //the account is created, but empty
				{"empty", Address1, true},
			},
			Maindb: []Check{
				{"exists", Address1, false},
				{"empty", Address1, true},
				{"obj", Address1, "nil"},
			},
		},
		AfterMerge: []Check{
			{"empty", Address1, true}, //empty accounts will be deleted after finalise
			{"exists", Address1, false},
			{"obj", Address1, "nil"},
		},
	}
	if err := runCase(txs, newStateDB(), newUncommittedDB(newStateDB()), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPevmRefund(t *testing.T) {
	// case 1. add refund from 0
	txs := Txs{
		{
			{"AddRefund", 100},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"refund", 0},
			},
			Maindb: []Check{
				{"refund", 0},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"refund", 100},
			},
			Maindb: []Check{
				{"refund", 0},
			},
		},
		AfterMerge: []Check{
			{"refund", 0}, //refund will be cleared after finalise
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
				{"refund", 100},
			},
			Maindb: []Check{
				{"refund", 100},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"refund", 180},
			},
			Maindb: []Check{
				{"refund", 100},
			},
		},
		AfterMerge: []Check{
			{"refund", 0},
		},
	}
	if err := runCase(txs, statedb, newUncommittedDB(unstateMain), check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPevmPreimages(t *testing.T) {
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

func TestPevmLogs(t *testing.T) {
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
				{"loglen", 2},
				{"log", thash, 0, []byte("hello"), 20, 0}, // i,  Data, TxIndex, Index
				{"log", thash, 1, []byte("world"), 20, 1},
			},
			Maindb: []Check{
				{"loglen", 0},
			},
		},
		AfterMerge: []Check{
			{"loglen", 2},
			{"log", thash, 0, []byte("hello"), 20, 0},
			{"log", thash, 1, []byte("world"), 20, 1},
		},
	}
	state := newStateDB()
	maindb := newStateDB()
	unstate := newUncommittedDB(maindb)
	//set TxContext
	unstate.txIndex, state.txIndex, maindb.txIndex = 20, 20, 19
	unstate.txHash, state.thash, maindb.thash = thash, thash, common.Hash{}

	if err := runCase(txs, state, unstate, check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}
}

func TestPevmAccessList(t *testing.T) {
	// before Berlin hardfork:
	//	 accesslist will be available in the whole scope of block
	// after Berlin hardfork:
	//   accesslist will be available only in the scope of the transaction, except those predefined.

	// before Berlin hardfork:
	// case 1.
	//		prepare: add address 1, add slot 1
	//      tx: add address 2, add slot 2
	//		check: address 1, slot 1, address 2, slot 2 are all in the accesslist
	coinBase := common.BytesToAddress([]byte("0xcoinbase"))
	sender := common.BytesToAddress([]byte("0xsender"))
	prepare := Txs{
		{
			{"AddAddress", Address1},
			{"AddSlots", Address1, common.BytesToHash([]byte("key1"))},
		},
	}
	tx := Txs{
		{
			{"AddAddress", Address2},
			{"AddSlots", Address2, common.BytesToHash([]byte("key2"))},
		},
	}
	check := CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"address", Address1, true},
				{"slot", Address1, "key1", true},
			},
			Maindb: []Check{
				{"address", Address1, true},
				{"slot", Address1, "key1", true},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"address", Address1, true},
				{"address", Address2, true},
				{"slot", Address1, "key1", true},
				{"slot", Address2, "key2", true},
				{"address", sender, false},
				{"address", coinBase, false},
			},
			Maindb: []Check{
				{"address", Address1, true},
				{"address", Address2, false},
				{"slot", Address1, "key1", true},
				{"slot", Address2, "key2", false},
			},
		},
		AfterMerge: []Check{
			{"address", Address1, true},
			{"address", Address2, true},
			{"slot", Address1, "key1", true},
			{"slot", Address2, "key2", true},
			{"address", sender, false},
			{"address", coinBase, false},
		},
	}
	statedb := newStateDB()
	maindb := newStateDB()
	prepare.Call(statedb)
	prepare.Call(maindb)

	statedb.Prepare(params.Rules{IsBerlin: false}, sender, coinBase, nil, nil, nil)
	uncommited := newUncommittedDB(maindb)
	uncommited.Prepare(params.Rules{IsBerlin: false}, sender, coinBase, nil, nil, nil)
	if err := runCase(tx, statedb, uncommited, check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

	// after Berlin hardfork:
	//	case 2:
	//		prepare: add address 1, add slot 1
	//		tx: add address 2, add slot 2
	//		before merge:
	//			address 2, slot 2 are in the accesslist; but not address 1, slot 1
	//			predefined address n, slot n are in the accesslist
	//		after merge:
	//			the same as before merge
	check = CheckState{
		BeforeRun: uncommitedState{
			Uncommited: []Check{
				{"address", sender, true},
				{"address", coinBase, true},
				{"address", Address1, false},
				{"slot", Address1, "key1", false},
			},
		},
		BeforeMerge: uncommitedState{
			Uncommited: []Check{
				{"address", sender, true},
				{"address", coinBase, true},
				{"address", Address1, false},
				{"address", Address2, true},
				{"slot", Address1, "key1", false},
				{"slot", Address2, "key2", true},
			},
		},
		AfterMerge: []Check{
			{"address", sender, true},
			{"address", coinBase, true},
			{"address", Address1, false},
			{"address", Address2, true},
			{"slot", Address1, "key1", false},
			{"slot", Address2, "key2", true},
		},
	}
	statedb, maindb = newStateDB(), newStateDB()
	prepare.Call(statedb)
	prepare.Call(maindb)
	statedb.Prepare(params.Rules{IsBerlin: true, IsShanghai: true}, sender, coinBase, nil, nil, nil)
	uncommited = newUncommittedDB(maindb)
	uncommited.Prepare(params.Rules{IsBerlin: true, IsShanghai: true}, sender, coinBase, nil, nil, nil)
	if err := runCase(tx, statedb, uncommited, check); err != nil {
		t.Fatalf("ut failed, err=%s", err.Error())
	}

}

// ================== conflict test ==================
// case 1. conflict in objects: balance, nonce, code, state
// case 2. conflict in transient storage
// case 3. conflict in accesslist
// case 4. conflict in logs
func TestPevmConflictObject(t *testing.T) {
	// case 1. conflict in balance
	// case 2. conflict in nonce
	// case 3. conflict in code
	// case 4. conflict in state

	// case of balance
	prepare := Txs{
		{
			{"AddBalance", Address1, big.NewInt(10)},
		},
	}
	add40 := Txs{
		{
			{"AddBalance", Address1, big.NewInt(30)},
		},
	}
	add50 := Txs{
		{
			{"AddBalance", Address1, big.NewInt(50)},
		},
	}
	check := []Check{
		{"balance", Address1, big.NewInt(40)},
	}
	check2 := []Check{
		{"balance", Address1, big.NewInt(30)},
	}
	if err := runConflictCase(nil, add40, add50, check2); err != nil {
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
		{"nonce", Address1, 21},
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
			{"SetCode", Address1, []byte("hello")},
		},
	}
	op1 := Txs{
		{
			{"SetCode", Address1, []byte("here we go")},
		},
	}
	op2 := Txs{
		{
			{"SetCode", Address1, []byte("here we go now")},
		},
	}
	check = []Check{
		{"code", Address1, []byte("here we go")},
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
			{"SetState", Address1, "key1", "value1"},
			{"SetState", Address1, "key2", "value2"},
		},
	}
	op1 = Txs{
		{
			{"SetState", Address1, "key2", "value2.22"},
			{"SetState", Address1, "key3", "value2.33"},
		},
	}
	op2 = Txs{
		{
			{"SetState", Address1, "key2", "value3.22"},
			{"SetState", Address1, "key1", "value3.11"},
		},
	}
	check = []Check{
		{"state", Address1, "key2", "value3.22"},
		{"state", Address1, "key1", "value3.11"},
		{"state", Address1, "key3", ""},
	}
	if err := runConflictCase(prepare, op2, op1, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
	if err := runConflictCase(nil, op2, op1, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

}

func TestPevmConflictLogs(t *testing.T) {
	thash1 := common.BytesToHash([]byte("tx1"))
	thash2 := common.BytesToHash([]byte("tx2"))
	thash3 := common.BytesToHash([]byte("tx3"))
	prepare := Txs{
		{
			{"SetTxContext", thash1, 0},
			{"AddLog", &types.Log{Data: []byte("hello")}},
		},
	}
	op1 := Txs{
		{
			{"SetTxContext", thash2, 1},
			{"AddLog", &types.Log{Data: []byte("world")}},
		},
	}
	op2 := Txs{
		{
			{"SetTxContext", thash3, 1},
			{"AddLog", &types.Log{Data: []byte("eth")}},
		},
	}
	check := []Check{
		{"loglen", 2},
		{"log", thash1, 0, []byte("hello"), 0, 0},
		{"log", thash2, 0, []byte("world"), 1, 1},
	}
	if err := runConflictCase(prepare, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}

}

func TestPevmConflictPreimage(t *testing.T) {
	op1 := Txs{
		{
			{"AddPreimage", common.BytesToHash([]byte("key1")), []byte("value1")},
		},
	}
	op2 := Txs{
		{
			{"AddPreimage", common.BytesToHash([]byte("key1")), []byte("value2")},
		},
	}
	check := []Check{
		{"preimage", common.BytesToHash([]byte("key1")), []byte("value1")},
	}
	if err := runConflictCase(nil, op1, op2, check); err != nil {
		t.Fatalf("ut failed, err:%s", err.Error())
	}
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

type Checks []Check

func (c Checks) Verify(state vm.StateDB) error {
	for _, check := range c {
		if err := check.Verify(state); err != nil {
			return err
		}
	}
	return nil
}

//
//		{"address", Address1, true},
//		{"slot", Address1, "key1", true},

func (c Check) Verify(state vm.StateDB) error {
	switch c[0].(string) {

	case "address":
		addr := c[1].(common.Address)
		exists := c[2].(bool)
		var accesslist *accessList
		if db, ok := state.(*StateDB); ok {
			accesslist = db.accessList
		} else if db, ok := state.(*UncommittedDB); ok {
			accesslist = db.accessList
		} else {
			panic("unknown stateDB type")
		}
		if exists == accesslist.ContainsAddress(addr) {
			return nil
		} else {
			return fmt.Errorf("address 'in list' mismatch, addr:%s, expected:%t, actual:%t", addr.String(), exists, accesslist.ContainsAddress(addr))
		}

	case "slot":
		addr := c[1].(common.Address)
		key := common.BytesToHash([]byte(c[2].(string)))
		exists := c[3].(bool)
		var accesslist *accessList
		if db, ok := state.(*StateDB); ok {
			accesslist = db.accessList
		} else if db, ok := state.(*UncommittedDB); ok {
			accesslist = db.accessList
		} else {
			panic("unknown stateDB type")
		}
		_, slotExists := accesslist.Contains(addr, key)
		if exists == slotExists {
			return nil
		} else {
			return fmt.Errorf("slot 'in list' mismatch, addr:%s, key:%s, expected:%t, actual:%t", addr, key.String(), exists, slotExists)
		}

	case "loglen":
		loglen := c[1].(int)
		var logSize int
		if db, ok := state.(*StateDB); ok {
			logSize = int(db.logSize)
		} else if db, ok := state.(*UncommittedDB); ok {
			logSize = len(db.logs)
		} else {
			panic("unknown stateDB type")
		}
		if loglen == logSize {
			return nil
		} else {
			return fmt.Errorf("loglen mismatch, expected:%d, actual:%d", loglen, logSize)
		}

		//{"log", thash, 0, []byte("hello"), 20, 0},
	case "log":
		thash := c[1].(common.Hash)
		i := c[2].(int)
		data := c[3].([]byte)
		txIndex := c[4].(int)
		Index := c[5].(int)
		var logs []*types.Log
		if db, ok := state.(*StateDB); ok {
			logs, ok = db.logs[thash]
			if !ok {
				return fmt.Errorf("log thash not found, thash:%s", thash.String())
			}
		} else if db, ok := state.(*UncommittedDB); ok {
			if thash.Cmp(db.txHash) != 0 {
				return fmt.Errorf("log thash not found, expected:%s, actual:%s", thash.String(), db.txHash.String())
			}
			logs = db.logs
		} else {
			panic("unknown stateDB type")
		}
		if len(logs) <= i {
			return fmt.Errorf("log index out of range, index:%d, len:%d", i, len(logs))
		}
		log := logs[i]
		if !bytes.Equal(log.Data, data) {
			return fmt.Errorf("log mismatch, expected:%s, actual:%s", string(data), string(logs[i].Data))
		}
		if log.TxIndex != uint(txIndex) {
			return fmt.Errorf("log txIndex mismatch, expected:%d, actual:%d", txIndex, log.TxIndex)
		}
		if log.Index != uint(Index) {
			return fmt.Errorf("log index mismatch, expected:%d, actual:%d", Index, log.Index)
		}
		return nil

	case "preimage":
		hash := c[1].(common.Hash)
		data := c[2].([]byte)
		var preimages map[common.Hash][]byte
		if db, ok := state.(*StateDB); ok {
			preimages = db.preimages
		} else if db, ok := state.(*UncommittedDB); ok {
			preimages = db.preimages
		} else {
			panic("unknown stateDB type")
		}
		if bytes.Equal(preimages[hash], data) {
			return nil
		} else {
			return fmt.Errorf("preimage mismatch, expected:%s, actual:%s", string(data), string(preimages[hash]))
		}

	case "tstorage":
		addr := c[1].(common.Address)
		key := common.BytesToHash([]byte(c[2].(string)))
		val := common.BytesToHash([]byte(c[3].(string)))
		if state.GetTransientState(addr, key).Cmp(val) == 0 {
			return nil
		} else {
			return fmt.Errorf("tstorage mismatch, key:%s, expected:%s, actual:%s", key.String(), val.String(), state.GetTransientState(addr, key).String())
		}

	case "exists":
		addr := c[1].(common.Address)
		exists := c[2].(bool)
		if state.Exist(addr) == exists {
			return nil
		} else {
			return fmt.Errorf("exists mismatch, expected:%t, actual:%t", exists, state.Exist(addr))
		}

	case "empty":
		addr := c[1].(common.Address)
		empty := c[2].(bool)
		if state.Empty(addr) == empty {
			return nil
		} else {
			return fmt.Errorf("empty mismatch, expected:%t, actual:%t", empty, state.Empty(addr))
		}

	case "balance":
		addr := c[1].(common.Address)
		balance := c[2].(*big.Int)
		if state.GetBalance(addr).Cmp(balance) == 0 {
			return nil
		} else {
			return fmt.Errorf("balance mismatch, expected:%d, actual:%d", balance.Uint64(), state.GetBalance(addr).Uint64())
		}

	case "nonce":
		addr := c[1].(common.Address)
		nonce := uint64(c[2].(int))
		if state.GetNonce(addr) == nonce {
			return nil
		} else {
			return fmt.Errorf("nonce mismatch, expected:%d, actual:%d", nonce, state.GetNonce(addr))
		}

	case "code":
		addr := c[1].(common.Address)
		code := c[2].([]byte)
		if bytes.Equal(state.GetCode(addr), code) {
			return nil
		} else {
			return fmt.Errorf("code mismatch, expected:%s, actual:%s", string(code), string(state.GetCode(addr)))
		}

	case "codeHash":
		addr := c[1].(common.Address)
		codeHash := c[2].(common.Hash)
		if codeHash.Cmp(state.GetCodeHash(addr)) == 0 {
			return nil
		} else {
			return fmt.Errorf("codeHash mismatch, expected:%s, actual:%s", codeHash.String(), state.GetCodeHash(addr).String())
		}

	case "state":
		addr := c[1].(common.Address)
		key := common.BytesToHash([]byte(c[2].(string)))
		val := common.BytesToHash([]byte(c[3].(string)))
		if state.GetState(addr, key).Cmp(val) == 0 {
			return nil
		} else {
			return fmt.Errorf("state mismatch, key:%s, expected:%s, actual:%s", key.String(), val.String(), state.GetState(addr, key).String())
		}

	case "cstate":
		addr := c[1].(common.Address)
		key := common.BytesToHash([]byte(c[2].(string)))
		val := common.BytesToHash([]byte(c[3].(string)))
		if state.GetCommittedState(addr, key).Cmp(val) == 0 {
			return nil
		} else {
			return fmt.Errorf("committed state mismatch, expected:%s, actual:%s", val.String(), state.GetCommittedState(addr, key).String())
		}

	case "obj":
		addr := c[1].(common.Address)
		exists := c[2].(string)
		if exists == "nil" {
			if state.Exist(addr) {
				return fmt.Errorf("object was expected not exists, addr:%s", addr.String())
			} else {
				return nil
			}
		} else {
			if !state.Exist(addr) {
				return fmt.Errorf("object was expected exists, addr:%s", addr.String())
			} else {
				return nil
			}
		}

	case "refund":
		refund := uint64(c[1].(int))
		if state.GetRefund() == refund {
			return nil
		} else {
			return fmt.Errorf("refund mismatch, expected:%d, actual:%d", refund, state.GetRefund())
		}

	default:
		panic(fmt.Sprintf("unknown check type: %s", c[0].(string)))
	}
}

type Op []interface{}

type Tx []Op

func (op Op) Call(db vm.StateDB) error {
	switch op[0].(string) {
	case "SetTxContext":
		state := db
		if db, ok := state.(*UncommittedDB); ok {
			db.SetTxContext(op[1].(common.Hash), op[2].(int))
		} else if db, ok := state.(*StateDB); ok {
			db.SetTxContext(op[1].(common.Hash), op[2].(int))
		} else {
			panic("unknown stateDB type")
		}
		return nil

	case "AddSlots":
		addr := op[1].(common.Address)
		slot := op[2].(common.Hash)
		db.AddSlotToAccessList(addr, slot)
		return nil

	case "AddAddress":
		addr := op[1].(common.Address)
		db.AddAddressToAccessList(addr)
		return nil

	case "AddLog":
		log := op[1].(*types.Log)
		db.AddLog(log)
		return nil

	case "SetTransientStorage":
		addr := op[1].(common.Address)
		key := op[2].(string)
		val := op[3].(string)
		db.SetTransientState(addr, common.BytesToHash([]byte(key)), common.BytesToHash([]byte(val)))
		return nil

	case "Create":
		addr := op[1].(common.Address)
		db.CreateAccount(addr)
		return nil

	case "AddPreimage":
		hash := op[1].(common.Hash)
		data := op[2].([]byte)
		db.AddPreimage(hash, data)
		return nil

	case "AddBalance":
		addr := op[1].(common.Address)
		balance := op[2].(*big.Int)
		db.AddBalance(addr, balance)
		return nil
	case "SubBalance":
		addr := op[1].(common.Address)
		balance := op[2].(*big.Int)
		db.SubBalance(addr, balance)
		return nil

	case "SetNonce":
		addr := op[1].(common.Address)
		nonce := uint64(op[2].(int))
		db.SetNonce(addr, nonce)
		return nil

	case "SetCode":
		addr := op[1].(common.Address)
		code := op[2].([]byte)
		db.SetCode(addr, code)
		return nil

	case "AddRefund":
		refund := uint64(op[1].(int))
		db.AddRefund(refund)
		return nil

	case "SubRefund":
		refund := uint64(op[1].(int))
		db.SubRefund(refund)
		return nil

	case "SetState":
		addr := op[1].(common.Address)
		key := common.BytesToHash([]byte(op[2].(string)))
		val := common.BytesToHash([]byte(op[3].(string)))
		db.SetState(addr, key, val)
		return nil

	case "SelfDestruct":
		addr := op[1].(common.Address)
		db.SelfDestruct(addr)
		return nil

	case "SelfDestruct6780":
		addr := op[1].(common.Address)
		db.Selfdestruct6780(addr)
		return nil

	default:
		return fmt.Errorf("unknown op type: %s", op[0].(string))
	}
}

// Call executes the transaction.
func (tx Tx) Call(db vm.StateDB) error {
	for _, op := range tx {
		if err := op.Call(db); err != nil {
			return err
		}
	}
	return nil
}

type Txs []Tx

func (txs Txs) Call(db vm.StateDB) error {
	for _, tx := range txs {
		if err := tx.Call(db); err != nil {
			return err
		}
	}
	return nil
}

func triedbConfig(StateScheme string) *trie.Config {
	config := &trie.Config{
		Preimages: true,
		NoTries:   false,
	}
	if StateScheme == rawdb.HashScheme {
		config.HashDB = &hashdb.Config{
			CleanCacheSize: 1 * 1024 * 1024,
		}
	}
	if StateScheme == rawdb.PathScheme {
		config.PathDB = &pathdb.Config{
			//TrieNodeBufferType:   c.PathNodeBuffer,
			//StateHistory:         c.StateHistory,
			CleanCacheSize: 1 * 1024 * 1024,
			DirtyCacheSize: 1 * 1024 * 1024,
			//ProposeBlockInterval: c.ProposeBlockInterval,
			//NotifyKeep:           keepFunc,
			//JournalFilePath:      c.JournalFilePath,
			//JournalFile:          c.JournalFile,
		}
	}
	return config
}

func newStateDB() *StateDB {
	memdb := rawdb.NewMemoryDatabase()
	// Open trie database with provided config
	triedb := trie.NewDatabase(memdb, triedbConfig(rawdb.HashScheme))
	stateCache := NewDatabaseWithNodeDB(memdb, triedb)
	st, err := New(common.Hash{}, stateCache, nil)
	if err != nil {
		panic(err)
	}
	return st
}

func newUncommittedDB(db *StateDB) *UncommittedDB {
	return NewUncommittedDB(db)
}

func runTxsOnStateDB(txs Txs, db *StateDB, check CheckState) (common.Hash, error) {
	// run the transactions
	if err := txs.Call(db); err != nil {
		return common.Hash{}, fmt.Errorf("state failed to run txs: %v", err)
	}
	// states before merge should be the same as the uncommitted db
	for _, c := range check.BeforeMerge.Uncommited {
		if err := c.Verify(db); err != nil {
			return common.Hash{}, fmt.Errorf("[before merge][uncommited db] failed to verify : %v", err)
		}
	}
	return db.IntermediateRoot(true), nil
}

func runTxOnUncommittedDB(txs Txs, db *UncommittedDB, check CheckState) (common.Hash, error) {
	// run the transaction
	if err := txs.Call(db); err != nil {
		return common.Hash{}, fmt.Errorf("unconfirm db failed to run txs: %v", err)
	}
	for _, check := range check.BeforeMerge.Uncommited {
		if err := check.Verify(db); err != nil {
			return common.Hash{}, fmt.Errorf("[before merge][uncommited db] failed to verify : %v", err)
		}
	}
	for _, check := range check.BeforeMerge.Maindb {
		if err := check.Verify(db.maindb); err != nil {
			return common.Hash{}, fmt.Errorf("[before merge][maindb] failed to verify : %v", err)
		}
	}
	if err := db.Merge(); err != nil {
		return common.Hash{}, fmt.Errorf("failed to merge: %v", err)
	}
	return db.maindb.IntermediateRoot(true), nil
}

func runConflictCase(prepare, txs1, txs2 Txs, checks []Check) error {
	maindb := newStateDB()
	if prepare != nil {
		maindb.CreateAccount(Address1)
		for _, op := range prepare {
			if err := op.Call(maindb); err != nil {
				return fmt.Errorf("failed to call prepare txs, err:%s", err.Error())
			}
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
	stRoot, errSt := runTxsOnStateDB(txs, state, check)
	unRoot, errUn := runTxOnUncommittedDB(txs, unstate, check)
	if errSt != nil {
		return fmt.Errorf("failed to run txs on state db: %v", errSt)
	}
	if errUn != nil {
		return fmt.Errorf("failed to run tx on uncommited db: %v", errUn)
	}
	for _, check := range check.AfterMerge {
		if err := check.Verify(state); err != nil {
			return fmt.Errorf("[after merge] failed to verify statedb: %v", err)
		}
		// an uncommitted is invalid after merge, so we verify its maindb instead
		if err := check.Verify(unstate.maindb); err != nil {
			return fmt.Errorf("[after merge] failed to verify uncommited db: %v", err)
		}
	}
	if stRoot.Cmp(unRoot) != 0 {
		return fmt.Errorf("state root mismatch: %s != %s", stRoot.String(), unRoot.String())
	}
	return nil
}

func Diff(a, b *StateDB) []common.Hash {
	// compare the two stateDBs, and return the different objects
	return nil
}
