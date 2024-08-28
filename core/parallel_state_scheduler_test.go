package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// mock transaction via ParallelTxRequest and ParallelTxResult. and the methods we need to mock is "transfer balance from on address to other"
// ParallelTxRequest:
//  1. usedGas -> value
//  2. staticSlotIndex -> fromAddress
//  3. runnable -> toAddress
//
// a mock transaction do the following steps when "execute" in parallel:
//   1. read balance of "fromAddress" from maindb
//   2. check balance and sub the amount of "value", records the "reads"(fromAddr->balance), write balance back into "slotDB"
//   3. read balance of "toAddress" from maindb
//   4. add the "value" to toAddress, records the "reads"(toAddr->balance), write it into "slotDB"
//
// a mock transaction do the following steps when "confirm":
//	 1. check the "reads"(addresses->balances) in maindb
//   2. merge the "slotDB"(addresses->balances) into maindb

type mockTx struct {
	req    *ParallelTxRequest
	reads  map[int]int
	slotDB map[int]int
}

var mockMainDB sync.Map

func putMainDB(data map[int]int) {
	for k, v := range data {
		mockMainDB.Store(k, v)
	}
}

func checkMainDB(data map[int]int) bool {
	for k, v := range data {
		val, ok := mockMainDB.Load(k)
		if !ok {
			return false
		}
		if val.(int) != v {
			return false
		}
	}
	return true
}

func newTxReq(from, to, value int) *ParallelTxRequest {
	usedGas := uint64(value)
	return &ParallelTxRequest{
		usedGas:         &usedGas,
		staticSlotIndex: from,
		runnable:        int32(to),
	}
}

func (mt *mockTx) Value() int {
	return int(*mt.req.usedGas)
}

func (mt *mockTx) From() int {
	return mt.req.staticSlotIndex
}

func (mt *mockTx) To() int {
	return int(mt.req.runnable)
}

func (mt *mockTx) execute(req *ParallelTxRequest) *ParallelTxResult {
	result := &ParallelTxResult{
		txReq: req,
		err:   nil,
	}
	mt.req = req
	// handle fromAddress
	balance, ok := mockMainDB.Load(mt.From())
	if !ok {
		result.err = fmt.Errorf("no balance from '%d'", mt.From())
		return result
	}
	if balance.(int) < mt.Value() {
		result.err = fmt.Errorf("insufficient found, need '%d', have '%d', fromaddr:%d", mt.Value(), balance.(int), mt.From())
		return result
	}
	mt.reads[mt.From()] = balance.(int)
	mt.slotDB[mt.From()] = balance.(int) - mt.Value()

	// handle toAddress
	balance, ok = mockMainDB.Load(mt.To())
	if !ok {
		result.err = fmt.Errorf("no balance from '%d'", mt.To())
		return result
	}
	mt.reads[mt.To()] = balance.(int)
	mt.slotDB[mt.To()] = balance.(int) + mt.Value()
	return result
}

func (mt *mockTx) confirm(result *ParallelTxResult) error {
	// check conflict
	fromBalance, _ := mockMainDB.Load(mt.From())
	toBalance, _ := mockMainDB.Load(mt.To())
	if fromBalance.(int) != mt.reads[mt.From()] {
		return fmt.Errorf("fromAddress state invalid")
	}
	if toBalance.(int) != mt.reads[mt.To()] {
		return fmt.Errorf("toAddress state invalid")
	}
	// merge slotDB into mainDB
	putMainDB(mt.slotDB)
	return nil
}

type caller struct {
	lock        sync.Mutex
	txs         map[*ParallelTxRequest]*mockTx
	conflictNum int32
}

func (c *caller) execute(req *ParallelTxRequest) *ParallelTxResult {
	mocktx := &mockTx{
		reads:  make(map[int]int),
		slotDB: make(map[int]int),
	}
	result := mocktx.execute(req)
	c.lock.Lock()
	c.txs[req] = mocktx
	if result.err != nil {
		atomic.AddInt32(&c.conflictNum, 1)
	}
	c.lock.Unlock()
	return result
}

func (c *caller) confirm(result *ParallelTxResult) error {
	c.lock.Lock()
	mtx := c.txs[result.txReq]
	c.lock.Unlock()
	err := mtx.confirm(result)
	if err != nil {
		atomic.AddInt32(&c.conflictNum, 1)
	}
	return err
}

func TestParallelProcess(t *testing.T) {
	// mock a state db,which provide:
	// KV read/write (in memory)
	// State read/write (in memory) ?
	// Trie read/write (in memory) ?
}

func TestTxLevelRun(t *testing.T) {
	// case 1: empty txs
	case1 := func() {
		levels([]uint64{}, [][]int{}).Run(
			func(*ParallelTxRequest) *ParallelTxResult { return nil },
			func(*ParallelTxResult) error { return nil })
	}
	// case 2: 4 txs with no dependencies, no conflicts
	case2 := func() {
		// mainDB: [1: 10, 2: 20, 3: 30, 4:40, 5:1, 6:1, 7:1, 8:1]
		// txs: [1,2,3,4] ->(all balances)-> [5,6,7,8]
		// true txdag: [][]int{nil, nil, nil, nil}
		// result:
		//	 mainDB: [1: 0, 2: 0, 3: 0, 4:0, 5:11, 6:21, 7:31, 8:41]
		//	 conflictNum: 0
		putMainDB(map[int]int{1: 10, 2: 20, 3: 30, 4: 40, 5: 1, 6: 1, 7: 1, 8: 1})
		allReqs := []*ParallelTxRequest{
			newTxReq(1, 5, 10),
			newTxReq(2, 6, 20),
			newTxReq(3, 7, 30),
			newTxReq(4, 8, 40),
		}
		txdag := int2txdag([][]int{
			nil, nil, nil, nil,
		})
		caller := caller{txs: make(map[*ParallelTxRequest]*mockTx)}
		err := NewTxLevels(allReqs, txdag).Run(caller.execute, caller.confirm)
		ok := checkMainDB(map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 11, 6: 21, 7: 31, 8: 41})
		if err != nil {
			t.Fatalf("failed, err:%v", err)
		}
		if !ok {
			t.Fatalf("invalid mainDB state")
		}
		if caller.conflictNum != 0 {
			t.Fatalf("conflict found, conflict:%d", caller.conflictNum)
		}
	}

	// case 3: 4 txs with 2 dependencies, no conflict
	//		actual:  t1->t0, t3->t2
	//		true txdag: [t0, t2], [t3, t4]
	//		result:  all of t1~t4 need rerun
	case3 := func() {
		// mainDB: [1: 10, 2: 20, 3: 0, 4:0, 5:1, 6:1]
		// txs: [1->3:10, 2->4:20, 3->5:10, 4->6:20]
		// true txdag: [][]int{nil, nil, {0}, {1}})
		// result:
		//	 mainDB: [1: 0, 2: 0, 3: 0, 4:0, 5:11, 6:21]
		//	 conflictNum: 0
		putMainDB(map[int]int{1: 10, 2: 20, 3: 0, 4: 0, 5: 1, 6: 1})
		allReqs := []*ParallelTxRequest{
			newTxReq(1, 3, 10),
			newTxReq(2, 4, 20),
			newTxReq(3, 5, 10),
			newTxReq(4, 6, 20),
		}
		txdag := int2txdag([][]int{
			nil, nil, {0}, {1},
		})
		caller := caller{txs: make(map[*ParallelTxRequest]*mockTx)}
		err := NewTxLevels(allReqs, txdag).Run(caller.execute, caller.confirm)
		ok := checkMainDB(map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 11, 6: 21})
		if err != nil {
			t.Fatalf("failed, err:%v", err)
		}
		if !ok {
			t.Fatalf("invalid mainDB state")
		}
		if caller.conflictNum != 0 {
			t.Fatalf("conflict found, conflict:%d", caller.conflictNum)
		}
	}

	// case 4: 4 txs with 2 dependencies, with 4 conflict
	//		actual:  t1->t0, t3->t2
	//		false txdag: [t4],[t3],[t0, t1]
	//		result:  all of t1~t4 need rerun
	case4 := func() {
		// mainDB: [1: 10, 2: 20, 3: 0, 4:0, 5:1, 6:1]
		// txs: [1->3:10, 2->4:20, 3->5:10, 4->6:20]
		// false txdag: {0}, nil, {-1}, {-1},
		// result:
		//	 mainDB: [1: 0, 2: 0, 3: 0, 4:0, 5:11, 6:21]
		//	 conflictNum: 2
		putMainDB(map[int]int{1: 10, 2: 20, 3: 0, 4: 0, 5: 1, 6: 1})
		allReqs := []*ParallelTxRequest{
			newTxReq(1, 3, 10),
			newTxReq(2, 4, 20),
			newTxReq(3, 5, 10),
			newTxReq(4, 6, 20),
		}
		txdag := int2txdag([][]int{
			{0}, nil, {-1}, {-1},
		})
		caller := caller{txs: make(map[*ParallelTxRequest]*mockTx)}
		err := NewTxLevels(allReqs, txdag).Run(caller.execute, caller.confirm)
		ok := checkMainDB(map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 11, 6: 21})
		if err != nil {
			t.Fatalf("failed, err:%v", err)
		}
		if !ok {
			t.Fatalf("invalid mainDB state")
		}
		if caller.conflictNum != 2 {
			t.Fatalf("conflict not found, conflict:%d", caller.conflictNum)
		}
	}

	// smoking test
	case5 := func() {
		// addressSetA: 1~1000,	addressSetB: 1001~2000, addressSetC: 2001~3000
		// store balance 1~1000 for addressSetA into mainDB
		// A -> B -> C
		// check balance of C
		// txdag: all in level 0, some conflict will come up
		storeBalance := func(from, to int, value int) {
			for i := from; i <= to; i++ {
				if value == 0 {
					mockMainDB.Store(i, 0)
				} else {
					mockMainDB.Store(i, i)
				}
			}
		}
		storeBalance(1, 1000, 1)
		storeBalance(1001, 2000, 0)
		storeBalance(2001, 3000, 0)
		allReqs := make([]*ParallelTxRequest, 0, 2000)
		// addressSetA -> addressSetB
		for i := 1; i <= 1000; i++ {
			allReqs = append(allReqs, newTxReq(i, i+1000, i))
		}
		// addressSetB -> addressSetC
		for i := 1; i <= 1000; i++ {
			allReqs = append(allReqs, newTxReq(i+1000, i+2000, i))
		}
		// result of addressSetC
		res := make(map[int]int, 1000)
		for i := 1; i <= 1000; i++ {
			res[i+2000] = i
		}
		caller := caller{txs: make(map[*ParallelTxRequest]*mockTx)}
		err := NewTxLevels(allReqs, nil).Run(caller.execute, caller.confirm)
		ok := checkMainDB(res)
		if err != nil {
			t.Fatalf("failed, err:%v", err)
		}
		if !ok {
			t.Fatalf("invalid mainDB state")
		}
		if caller.conflictNum <= 0 {
			t.Fatalf("conflict not found, conflict:%d", caller.conflictNum)
		}
	}
	// case 6: txs with dependencies, but no txdag
	case6 := func() {
		// mainDB: [1: 15, 2: 20, 3: 0]
		// txs: [1->2:10, 2->3:10]
		// txdag: nil
		// result:
		//	 mainDB: [1: 5, 2: 20, 3: 10]
		putMainDB(map[int]int{1: 15, 2: 20, 3: 0})
		allReqs := []*ParallelTxRequest{
			newTxReq(1, 2, 10),
			newTxReq(2, 3, 10),
		}
		caller := caller{txs: make(map[*ParallelTxRequest]*mockTx)}
		err := NewTxLevels(allReqs, nil).Run(caller.execute, caller.confirm)
		ok := checkMainDB(map[int]int{1: 5, 2: 20, 3: 10})
		if err != nil {
			t.Fatalf("failed, err:%v", err)
		}
		if !ok {
			t.Fatalf("invalid mainDB state")
		}
	}

	// case 7: txs with dependencies, with default txdag (deposit tx + all comment txs)
	case7 := func() {
		dag := &types.PlainTxDAG{}
		dag.SetTxDep(0, types.TxDep{TxIndexes: nil, Flags: &types.ExcludedTxFlag})
		// mainDB: [1: 15, 2: 20, 3: 0]
		// txs: [1->2:10, 2->3:10]
		// txdag: [-1, nil]
		// result:
		//	 mainDB: [1: 5, 2: 20, 3: 10]
		putMainDB(map[int]int{1: 15, 2: 20, 3: 0})
		allReqs := []*ParallelTxRequest{
			newTxReq(1, 2, 10),
			newTxReq(2, 3, 10),
		}
		caller := caller{txs: make(map[*ParallelTxRequest]*mockTx)}
		err := NewTxLevels(allReqs, dag).Run(caller.execute, caller.confirm)
		ok := checkMainDB(map[int]int{1: 5, 2: 20, 3: 10})
		if err != nil {
			t.Fatalf("failed, err:%v", err)
		}
		if !ok {
			t.Fatalf("invalid mainDB state")
		}
		if caller.conflictNum != 0 {
			t.Fatalf("conflict found, conflict:%d", caller.conflictNum)
		}

	}

	case1()
	case2()
	case3()
	case4()
	case5()
	case6()
	case7()
}

func TestNewTxLevels(t *testing.T) {
	// definition of dependencies:
	//    {-1} means dependent all txs
	//    {-2} means excluded tx
	//    nil means no dependencies
	//    {0,1} means dependent on tx[0] and tx[1]

	// case 1: empty txs
	assertEqual(levels([]uint64{}, [][]int{}), [][]uint64{}, t)

	// case 2: txs with no dependencies
	// tx[0] has no dependencies, tx[0].Nonce() == 1
	// tx[1] has no dependencies, tx[1].Nonce() == 2
	// tx[2] has no dependencies, tx[2].Nonce() == 3
	// tx[3] has no dependencies, tx[3].Nonce() == 4
	assertEqual(levels([]uint64{1, 2, 3, 4}, [][]int{nil, nil, nil, nil}), [][]uint64{{1, 2, 3, 4}}, t)

	// case 3: txs with dependencies
	// tx[0] has no dependencies, tx[0].Nonce() == 1
	// tx[1] depends on tx[0], tx[1].Nonce() == 2
	// tx[2] depends on tx[1], tx[2].Nonce() == 3
	assertEqual(levels([]uint64{1, 2, 3}, [][]int{nil, {0}, {1}}), [][]uint64{{1}, {2}, {3}}, t)

	// case 4: txs with dependencies and no dependencies
	// tx[0] has no dependencies, tx[0].Nonce() == 1
	// tx[1] has no dependencies, tx[1].Nonce() == 2
	// tx[2] dependents on t[0], tx[2].Nonce() == 3
	// tx[3] dependents on t[0], tx[3].Nonce() == 4
	// tx[4] dependents on t[2], tx[4].Nonce() == 5
	// tx[5] dependents on t[3], tx[5].Nonce() == 6
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6}, [][]int{nil, nil, {0}, {1}, {2}, {3}}), [][]uint64{{1, 2}, {3, 4}, {5, 6}}, t)

	// case 5: 1 excluded tx + n no dependencies tx
	assertEqual(levels([]uint64{1, 2, 3}, [][]int{{-1}, nil, nil}), [][]uint64{{1}, {2, 3}}, t)

	// case 6: 1 excluded tx + n no dependencies txs + n all-dependencies txs
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6}, [][]int{{-1}, nil, nil, nil, {-2}, {-2}}), [][]uint64{{1}, {2, 3, 4}, {5}, {6}}, t)

	// case 7: 1 excluded tx + n no dependencies txs + n dependencies txs + 1 all-dependencies tx
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6, 7}, [][]int{{-1}, nil, nil, nil, {0, 1}, {2}, {-2}}), [][]uint64{{1}, {2, 3}, {4, 5, 6}, {7}}, t)

	// case 8:  n no dependencies txs + n all-dependencies txs
	assertEqual(levels([]uint64{1, 2, 3, 4, 5}, [][]int{nil, nil, nil, {-2}, {-2}}), [][]uint64{{1, 2, 3}, {4}, {5}}, t)

	// case 9: loop-back txdag
	assertEqual(levels([]uint64{1, 2, 3, 4}, [][]int{{1}, nil, {0}, nil}), [][]uint64{{2}, {1}, {3, 4}}, t)
}

func levels(nonces []uint64, txdag [][]int) TxLevels {
	return NewTxLevels(nonces2txs(nonces), int2txdag(txdag))
}

func nonces2txs(nonces []uint64) []*ParallelTxRequest {
	rq := make([]*ParallelTxRequest, len(nonces))
	for i, nonce := range nonces {
		if nonce == 0 {
			rq[i] = nil
		} else {
			rq[i] = &ParallelTxRequest{tx: types.NewTransaction(nonce, common.Address{}, nil, 0, nil, nil), txIndex: i}
		}
	}
	return rq
}

func int2txdag(txdag [][]int) types.TxDAG {
	dag := types.PlainTxDAG{}
	for i, deps := range txdag {
		var dep types.TxDep
		switch true {
		case len(deps) == 0:
			dep = types.TxDep{TxIndexes: nil, Flags: nil}
		case deps[0] == -1:
			dep = types.TxDep{TxIndexes: nil, Flags: &types.ExcludedTxFlag}
		case deps[0] == -2:
			dep = types.TxDep{TxIndexes: nil, Flags: &types.NonDependentRelFlag}
		default:
			converted := make([]uint64, len(deps))
			for j, dep := range deps {
				converted[j] = uint64(dep)
			}
			dep = types.TxDep{TxIndexes: converted, Flags: nil}
		}
		if err := dag.SetTxDep(i, dep); err != nil {
			panic("wrong txdag")
		}
	}
	return &dag
}

func assertEqual(actual TxLevels, expected [][]uint64, t *testing.T) {
	if len(actual) != len(expected) {
		t.Fatalf("expected %d levels, got %d levels", len(expected), len(actual))
		return
	}
	// reverse all levels
	for i := 0; i < len(actual); i++ {
		for j := 0; j < len(actual[i])/2; j++ {
			actual[i][j], actual[i][len(actual[i])-1-j] = actual[i][len(actual[i])-1-j], actual[i][j]
		}
	}
	for i, txLevel := range actual {
		if len(txLevel) != len(expected[i]) {
			t.Fatalf("expected %d txs in level %d, got %d txs", len(expected[i]), i, len(txLevel))
			return
		}
		for j, tx := range txLevel {
			if tx.tx.Nonce() != expected[i][j] {
				t.Fatalf("expected nonce: %d, got nonce: %d", tx.tx.Nonce(), expected[i][j])
			}
		}
	}
}
