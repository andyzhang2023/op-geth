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
		err, _ := NewTxLevels(allReqs, txdag).Run(caller.execute, caller.confirm)
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
		err, _ := NewTxLevels(allReqs, txdag).Run(caller.execute, caller.confirm)
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
		err, _ := NewTxLevels(allReqs, txdag).Run(caller.execute, caller.confirm)
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
		err, _ := NewTxLevels(allReqs, nil).Run(caller.execute, caller.confirm)
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
		err, _ := NewTxLevels(allReqs, nil).Run(caller.execute, caller.confirm)
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
		err, _ := NewTxLevels(allReqs, dag).Run(caller.execute, caller.confirm)
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
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6, 7}, [][]int{{-1}, nil, nil, nil, {0, 1}, {2}, {-2}}), [][]uint64{{1}, {4, 2, 3}, {5, 6}, {7}}, t)

	// case 8:  n no dependencies txs + n all-dependencies txs
	assertEqual(levels([]uint64{1, 2, 3, 4, 5}, [][]int{nil, nil, nil, {-2}, {-2}}), [][]uint64{{1, 2, 3}, {4}, {5}}, t)

	// case 9: loop-back txdag
	assertEqual(levels([]uint64{1, 2, 3, 4}, [][]int{{1}, nil, {0}, nil}), [][]uint64{{2, 4}, {1}, {3}}, t)
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

func TestNewLevel(t *testing.T) {
	fetchTxIndexes := func(level TxLevel) []int {
		indexes := make([]int, len(level))
		for i, tx := range level {
			indexes[i] = tx.txIndex
		}
		return indexes
	}
	dag := txDAG(txDAGData())
	fmt.Printf("txCount:%d\n", dag.TxCount())
	nonces := make([]uint64, dag.TxCount())
	for i := 0; i < dag.TxCount(); i++ {
		nonces[i] = uint64(i) + 1
	}
	txs := nonces2txs(nonces)
	fmt.Printf("txs:%d\n", len(txs))
	levels := NewTxLevels(txs, dag)
	fmt.Printf("levels:%d\n", len(levels))
	for i := 0; i < len(levels); i++ {
		fmt.Printf("level [%d]: len=%d, elements:%v\n", i, len(levels[i]), fetchTxIndexes(levels[i]))
	}
}

func txDAG(encode []byte) types.TxDAG {
	if dag, err := types.DecodeTxDAGCalldata(encode); err != nil {
		panic(err)
	} else {
		return dag
	}
}

func txDAGData() []byte {
	data := string("0x5517ed8c000000000000000000000000000000000000000000000000000000000000002000000000000000000000000000000000000000000000000000000000000012bd01f912b9f912b6c2c002c1c0c1c0c2c102c2c103c2c104c2c105c2c106c2c107c2c108c2c109c2c10ac2c10bc2c10cc2c10dc2c10ec2c10fc2c110c2c111c2c112c2c113c2c114c2c115c2c116c2c117c2c118c2c119c2c11ac2c11bc2c11cc2c11dc2c11ec2c11fc2c120c2c121c2c122c2c123c2c124c2c125c2c126c2c127c2c128c2c129c2c12ac2c12bc2c12cc2c12dc2c12ec2c12fc2c130c2c131c2c132c2c133c2c134c2c135c2c136c2c137c2c138c2c139c2c13ac2c13bc2c13cc2c13dc2c13ec2c13fc2c140c2c141c2c142c2c143c2c144c2c145c2c146c2c147c2c148c2c149c2c14ac2c14bc2c14cc2c14dc2c14ec2c14fc2c150c2c151c2c152c2c153c2c154c2c155c2c156c2c157c2c158c2c159c2c15ac2c15bc2c15cc2c15dc2c15ec2c15fc2c160c2c161c2c162c2c163c2c164c2c165c2c166c2c167c2c168c2c169c2c16ac2c16bc2c16cc2c16dc2c16ec2c16fc2c170c2c171c2c172c2c173c2c174c2c175c2c176c2c177c2c178c2c179c2c17ac2c17bc2c17cc2c17dc2c17ec2c17fc3c28180c3c28181c3c28182c3c28183c3c28184c3c28185c3c28186c3c28187c3c28188c3c28189c3c2818ac3c2818bc3c2818cc3c2818dc3c2818ec3c2818fc3c28190c3c28191c3c28192c3c28193c3c28194c3c28195c3c28196c3c28197c3c28198c3c28199c3c2819ac3c2819bc3c2819cc3c2819dc3c2819ec3c2819fc3c281a0c3c281a1c3c281a2c3c281a3c3c281a4c3c281a5c3c281a6c3c281a7c3c281a8c3c281a9c3c281aac3c281abc3c281acc3c281adc3c281aec3c281afc3c281b0c3c281b1c3c281b2c3c281b3c3c281b4c3c281b5c3c281b6c3c281b7c3c281b8c3c281b9c3c281bac3c281bbc3c281bcc3c281bdc3c281bec3c281bfc3c281c0c3c281c1c3c281c2c3c281c3c3c281c4c3c281c5c3c281c6c3c281c7c3c281c8c3c281c9c3c281cac3c281cbc3c281ccc3c281cdc3c281cec3c281cfc3c281d0c3c281d1c3c281d2c3c281d3c3c281d4c3c281d5c3c281d6c3c281d7c3c281d8c3c281d9c3c281dac3c281dbc3c281dcc3c281ddc3c281dec3c281dfc3c281e0c3c281e1c3c281e2c3c281e3c3c281e4c3c281e5c3c281e6c3c281e7c3c281e8c3c281e9c3c281eac3c281ebc3c281ecc3c281edc3c281eec3c281efc3c281f0c3c281f1c3c281f2c3c281f3c3c281f4c3c281f5c3c281f6c3c281f7c3c281f8c3c281f9c3c281fac3c281fbc3c281fcc3c281fdc3c281fec3c281ffc4c3820100c4c3820101c4c3820102c4c3820103c4c3820104c4c3820105c4c3820106c4c3820107c4c3820108c4c3820109c4c382010ac4c382010bc4c382010cc4c382010dc4c382010ec4c382010fc4c3820110c4c3820111c4c3820112c4c3820113c4c3820114c4c3820115c4c3820116c4c3820117c4c3820118c4c3820119c4c382011ac4c382011bc4c382011cc4c382011dc4c382011ec4c382011fc4c3820120c4c3820121c4c3820122c4c3820123c4c3820124c4c3820125c4c3820126c4c3820127c4c3820128c4c3820129c4c382012ac4c382012bc4c382012cc4c382012dc4c382012ec4c382012fc4c3820130c4c3820131c4c3820132c4c3820133c4c3820134c4c3820135c4c3820136c4c3820137c4c3820138c4c3820139c4c382013ac4c382013bc4c382013cc4c382013dc4c382013ec4c382013fc4c3820140c4c3820141c4c3820142c4c3820143c4c3820144c4c3820145c4c3820146c4c3820147c4c3820148c4c3820149c4c382014ac4c382014bc4c382014cc4c382014dc4c382014ec4c382014fc4c3820150c4c3820151c4c3820152c4c3820153c4c3820154c4c3820155c4c3820156c4c3820157c4c3820158c4c3820159c4c382015ac4c382015bc4c382015cc4c382015dc4c382015ec4c382015fc4c3820160c4c3820161c4c3820162c4c3820163c4c3820164c4c3820165c4c3820166c4c3820167c4c3820168c4c3820169c4c382016ac4c382016bc4c382016cc4c382016dc4c382016ec4c382016fc4c3820170c4c3820171c4c3820172c4c3820173c4c3820174c4c3820175c4c3820176c4c3820177c4c3820178c4c3820179c4c382017ac4c382017bc4c382017cc4c382017dc4c382017ec4c382017fc4c3820180c4c3820181c4c3820182c4c3820183c4c3820184c4c3820185c4c3820186c4c3820187c4c3820188c4c3820189c4c382018ac4c382018bc4c382018cc4c382018dc4c382018ec4c382018fc4c3820190c4c3820191c4c3820192c4c3820193c4c3820194c4c3820195c4c3820196c4c3820197c4c3820198c4c3820199c4c382019ac4c382019bc4c382019cc4c382019dc4c382019ec4c382019fc4c38201a0c4c38201a1c4c38201a2c4c38201a3c4c38201a4c4c38201a5c4c38201a6c4c38201a7c4c38201a8c4c38201a9c4c38201aac4c38201abc4c38201acc4c38201adc4c38201aec4c38201afc4c38201b0c4c38201b1c4c38201b2c4c38201b3c4c38201b4c4c38201b5c4c38201b6c4c38201b7c4c38201b8c4c38201b9c4c38201bac4c38201bbc4c38201bcc4c38201bdc4c38201bec4c38201bfc4c38201c0c4c38201c1c4c38201c2c4c38201c3c4c38201c4c4c38201c5c4c38201c6c4c38201c7c4c38201c8c4c38201c9c4c38201cac4c38201cbc4c38201ccc4c38201cdc4c38201cec4c38201cfc4c38201d0c4c38201d1c4c38201d2c4c38201d3c4c38201d4c4c38201d5c4c38201d6c4c38201d7c4c38201d8c4c38201d9c4c38201dac4c38201dbc4c38201dcc4c38201ddc4c38201dec4c38201dfc4c38201e0c4c38201e1c4c38201e2c4c38201e3c4c38201e4c4c38201e5c4c38201e6c4c38201e7c4c38201e8c4c38201e9c4c38201eac4c38201ebc4c38201ecc4c38201edc4c38201eec4c38201efc4c38201f0c4c38201f1c4c38201f2c4c38201f3c4c38201f4c4c38201f5c4c38201f6c4c38201f7c4c38201f8c4c38201f9c4c38201fac4c38201fbc4c38201fcc4c38201fdc4c38201fec4c38201ffc4c3820200c4c3820201c4c3820202c4c3820203c4c3820204c4c3820205c4c3820206c4c3820207c4c3820208c4c3820209c4c382020ac4c382020bc4c382020cc4c382020dc4c382020ec4c382020fc4c3820210c4c3820211c4c3820212c4c3820213c4c3820214c4c3820215c4c3820216c4c3820217c4c3820218c4c3820219c4c382021ac4c382021bc4c382021cc4c382021dc4c382021ec4c382021fc4c3820220c4c3820221c4c3820222c4c3820223c4c3820224c4c3820225c4c3820226c4c3820227c4c3820228c4c3820229c4c382022ac4c382022bc4c382022cc4c382022dc4c382022ec4c382022fc4c3820230c4c3820231c4c3820232c4c3820233c4c3820234c4c3820235c4c3820236c4c3820237c4c3820238c4c3820239c4c382023ac4c382023bc4c382023cc4c382023dc4c382023ec4c382023fc4c3820240c4c3820241c4c3820242c4c3820243c4c3820244c4c3820245c4c3820246c4c3820247c4c3820248c4c3820249c4c382024ac4c382024bc4c382024cc4c382024dc4c382024ec4c382024fc4c3820250c4c3820251c4c3820252c4c3820253c4c3820254c4c3820255c4c3820256c4c3820257c4c3820258c4c3820259c4c382025ac4c382025bc4c382025cc4c382025dc4c382025ec4c382025fc4c3820260c4c3820261c4c3820262c4c3820263c4c3820264c4c3820265c4c3820266c4c3820267c4c3820268c4c3820269c4c382026ac4c382026bc4c382026cc4c382026dc4c382026ec4c382026fc4c3820270c4c3820271c4c3820272c4c3820273c4c3820274c4c3820275c4c3820276c4c3820277c4c3820278c4c3820279c4c382027ac4c382027bc4c382027cc4c382027dc4c382027ec4c382027fc4c3820280c4c3820281c4c3820282c4c3820283c4c3820284c4c3820285c4c3820286c4c3820287c4c3820288c4c3820289c4c382028ac4c382028bc4c382028cc4c382028dc4c382028ec4c382028fc4c3820290c4c3820291c4c3820292c4c3820293c4c3820294c4c3820295c4c3820296c4c3820297c4c3820298c4c3820299c4c382029ac4c382029bc4c382029cc4c382029dc4c382029ec4c382029fc4c38202a0c4c38202a1c4c38202a2c4c38202a3c4c38202a4c4c38202a5c4c38202a6c4c38202a7c4c38202a8c4c38202a9c4c38202aac4c38202abc4c38202acc4c38202adc4c38202aec4c38202afc4c38202b0c4c38202b1c4c38202b2c4c38202b3c4c38202b4c4c38202b5c4c38202b6c4c38202b7c4c38202b8c4c38202b9c4c38202bac4c38202bbc4c38202bcc4c38202bdc4c38202bec4c38202bfc4c38202c0c4c38202c1c4c38202c2c4c38202c3c4c38202c4c4c38202c5c4c38202c6c4c38202c7c4c38202c8c4c38202c9c4c38202cac4c38202cbc4c38202ccc4c38202cdc4c38202cec4c38202cfc4c38202d0c4c38202d1c4c38202d2c4c38202d3c4c38202d4c4c38202d5c4c38202d6c4c38202d7c4c38202d8c4c38202d9c4c38202dac4c38202dbc4c38202dcc4c38202ddc4c38202dec4c38202dfc4c38202e0c4c38202e1c4c38202e2c4c38202e3c4c38202e4c4c38202e5c4c38202e6c4c38202e7c4c38202e8c4c38202e9c4c38202eac4c38202ebc4c38202ecc4c38202edc4c38202eec4c38202efc4c38202f0c4c38202f1c4c38202f2c4c38202f3c4c38202f4c4c38202f5c4c38202f6c4c38202f7c4c38202f8c4c38202f9c4c38202fac4c38202fbc4c38202fcc4c38202fdc4c38202fec4c38202ffc4c3820300c4c3820301c4c3820302c4c3820303c4c3820304c4c3820305c4c3820306c4c3820307c4c3820308c4c3820309c4c382030ac4c382030bc4c382030cc4c382030dc4c382030ec4c382030fc4c3820310c4c3820311c4c3820312c4c3820313c4c3820314c4c3820315c4c3820316c4c3820317c4c3820318c4c3820319c4c382031ac4c382031bc4c382031cc4c382031dc4c382031ec4c382031fc4c3820320c4c3820321c4c3820322c4c3820323c4c3820324c4c3820325c4c3820326c4c3820327c4c3820328c4c3820329c4c382032ac4c382032bc4c382032cc4c382032dc4c382032ec4c382032fc4c3820330c4c3820331c4c3820332c4c3820333c4c3820334c4c3820335c4c3820336c4c3820337c4c3820338c4c3820339c4c382033ac4c382033bc4c382033cc4c382033dc4c382033ec4c382033fc4c3820340c4c3820341c4c3820342c4c3820343c4c3820344c4c3820345c4c3820346c4c3820347c4c3820348c4c3820349c4c382034ac4c382034bc4c382034cc4c382034dc4c382034ec4c382034fc4c3820350c4c3820351c4c3820352c4c3820353c4c3820354c4c3820355c4c3820356c4c3820357c4c3820358c4c3820359c4c382035ac4c382035bc4c382035cc4c382035dc4c382035ec4c382035fc4c3820360c4c3820361c4c3820362c4c3820363c4c3820364c4c3820365c4c3820366c4c3820367c4c3820368c4c3820369c4c382036ac4c382036bc4c382036cc4c382036dc4c382036ec4c382036fc4c3820370c4c3820371c4c3820372c4c3820373c4c3820374c4c3820375c4c3820376c4c3820377c4c3820378c4c3820379c4c382037ac4c382037bc4c382037cc4c382037dc4c382037ec4c382037fc4c3820380c4c3820381c4c3820382c4c3820383c4c3820384c4c3820385c4c3820386c4c3820387c4c3820388c4c3820389c4c382038ac4c382038bc4c382038cc4c382038dc4c382038ec4c382038fc4c3820390c4c3820391c4c3820392c4c3820393c4c3820394c4c3820395c4c3820396c4c3820397c4c3820398c4c3820399c4c382039ac4c382039bc4c382039cc4c382039dc4c382039ec4c382039fc4c38203a0c4c38203a1c4c38203a2c4c38203a3c4c38203a4c4c38203a5c4c38203a6c4c38203a7c4c38203a8c4c38203a9c4c38203aac4c38203abc4c38203acc4c38203adc4c38203aec4c38203afc4c38203b0c4c38203b1c4c38203b2c4c38203b3c4c38203b4c4c38203b5c4c38203b6c4c38203b7c4c38203b8c4c38203b9c4c38203bac4c38203bbc4c38203bcc4c38203bdc4c38203bec4c38203bfc4c38203c0c4c38203c1c4c38203c2c4c38203c3c4c38203c4c4c38203c5c4c38203c6c4c38203c7c4c38203c8c4c38203c9c4c38203cac4c38203cbc4c38203ccc4c38203cdc4c38203cec4c38203cfc4c38203d0c4c38203d1c4c38203d2c4c38203d3c4c38203d4c4c38203d5c4c38203d6c4c38203d7c4c38203d8c4c38203d9c4c38203dac4c38203dbc4c38203dcc4c38203ddc4c38203dec4c38203dfc4c38203e0c4c38203e1c4c38203e2c4c38203e3c4c38203e4c4c38203e5c4c38203e6c4c38203e7c4c38203e8c4c38203e9c4c38203eac4c38203ebc4c38203ecc4c38203edc4c38203eec4c38203efc4c38203f0c4c38203f1c4c38203f2c4c38203f3c4c38203f4c4c38203f5c4c38203f6c4c38203f7c4c38203f8c4c38203f9c4c38203fac4c38203fbc4c38203fcc4c38203fdc4c38203fec4c38203ffc4c3820400c4c3820401c4c3820402c4c3820403c4c3820404c4c3820405c4c3820406c4c3820407c4c3820408c4c3820409c2c001000000")
	return common.Hex2Bytes(data[2:])
}
