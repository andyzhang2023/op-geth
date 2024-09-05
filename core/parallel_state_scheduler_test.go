package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
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

func getBlockTransactions(url string, blockNumber int64) (types.Transactions, error) {
	client, err := ethclient.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to the Ethereum client: %v", err)
	}

	block, err := client.BlockByNumber(context.Background(), big.NewInt(blockNumber))
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %v", err)
	}

	return block.Transactions(), nil
}

func loadChainConfigFromGenesis(filePath string) (*params.ChainConfig, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open genesis file: %v", err)
	}
	defer file.Close()

	byteValue, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read genesis file: %v", err)
	}

	var genesis Genesis
	if err := json.Unmarshal(byteValue, &genesis); err != nil {
		return nil, fmt.Errorf("failed to unmarshal genesis file: %v", err)
	}

	return genesis.Config, nil
}

func TestShowDAG(t *testing.T) {
	// Example usage
	genesisFilePath := "/Users/awen/Desktop/Nodereal/aweneagle_projects/chain-infra/qa/gitops/qa-us/opbnb-qanet-ec-5/contracts-info/genesis.json"
	chainConfig, err := loadChainConfigFromGenesis(genesisFilePath)
	if err != nil {
		log.Fatalf("Failed to load chain config: %v", err)
	}
	signer := types.LatestSigner(chainConfig)
	_, dag, err := readTxDAGItemFromLine(block340670())
	if err != nil {
		panic(err)
	}
	txs, err := getBlockTransactions("https://opbnb-qanet-ec-5-seq-pevm-2.bk.nodereal.cc", 340670)
	if err != nil {
		panic(err)
	}
	//check dag[0]; it should be a deposit tx, and should have a exclude flag
	if !dag.TxDep(0).CheckFlag(types.ExcludedTxFlag) {
		panic("broken dag, deposit tx should have exclude flag")
	}
	var dependTo = func(a *types.Transaction, b *types.Transaction) bool {
		if a.Nonce() <= b.Nonce() {
			return false
		}
		aFromAddr, err := types.Sender(signer, a)
		if err != nil {
			panic(err)
		}
		bFromAddr, err := types.Sender(signer, b)
		if err != nil {
			panic(err)
		}
		aToAddr, bToAddr := *(a.To()), *(b.To())

		if aFromAddr.Cmp(bFromAddr) == 0 || aFromAddr.Cmp(bToAddr) == 0 {
			return true
		}
		return aToAddr.Cmp(bFromAddr) == 0 || aToAddr.Cmp(bToAddr) == 0
	}
	fmt.Printf("total tx:%d, dag tx:%d\n", len(txs), dag.TxCount())
	depCorrect, depWrong := 1, 0
	for i := 1; i < len(txs); i++ {
		var deps = map[int]struct{}{}
		// no need to add tx[0], must be dependent on tx[0]
		for j := 1; j < i; j++ {
			if dependTo(txs[i], txs[j]) {
				deps[j] = struct{}{}
			}
		}
		ofrom, _ := types.Sender(signer, txs[i])
		oto := txs[i].To()
		temp := dag.TxDep(i).TxIndexes
		// filter out 0
		originDeps := make([]uint64, 0, len(temp))
		for _, j := range originDeps {
			if j != 0 {
				originDeps = append(originDeps, j)
			}
		}
		passed := true
		if len(deps) != len(originDeps) {
			fmt.Printf("tx[%d:%s] has %d deps, but dag has %d deps\n", i, txs[i].Hash().String(), len(deps), len(originDeps))
			passed = false
			//expected:
			for ti := range originDeps {
				from, _ := types.Sender(signer, txs[ti])
				to := txs[ti].To()
				fmt.Printf("expected tindex=%d, from=%s, to=%s => tindex=%d, from=%s, to=%s\n", i, ofrom, oto, ti, from, to)
			}
			//actual:
			if len(deps) > 0 {
				for ti := range deps {
					from, _ := types.Sender(signer, txs[ti])
					to := txs[ti].To()
					fmt.Printf("tx[%d] actual from=%s, to=%s,  tindex=%d, from=%s, to=%s txhash=%s dag:%v, dagFlag:%d\n", i, ofrom, oto, ti, from, to, txs[ti].Hash().String(), dag.TxDep(ti).TxIndexes, dag.TxDep(ti).Flags)
				}
			} else {
				fmt.Printf("actual tindex=%d, from=%s, to=%s => no txs\n", i, ofrom, oto)
			}
		} else {
			for _, j := range originDeps {
				if _, ok := deps[int(j)]; !ok {
					fmt.Printf("tx[%d:%s] has %d deps, but dag has %d deps\n", i, txs[i].Hash().String(), len(deps), len(originDeps))
					passed = false
				}
			}
			if !passed {
				//expected:
				for ti := range originDeps {
					from, _ := types.Sender(signer, txs[ti])
					to := txs[ti].To()
					fmt.Printf("expected tindex=%d, from=%s, to=%s => tindex=%d, from=%s, to=%s\n", i, ofrom, oto, ti, from, to)
				}
				//actual:
				if len(deps) > 0 {
					for ti := range deps {
						from, _ := types.Sender(signer, txs[ti])
						to := txs[ti].To()
						fmt.Printf("tx[%d] actual from=%s, to=%s,  tindex=%d, from=%s, to=%s txhash=%s dag:%v, dagFlag:%d\n", i, ofrom, oto, ti, from, to, txs[ti].Hash().String(), dag.TxDep(ti).TxIndexes, dag.TxDep(ti).Flags)
					}
				} else {
					fmt.Printf("actual tindex=%d, from=%s, to=%s => no txs\n", i, ofrom, oto)
				}
			}
		}
		if passed {
			depCorrect++
			fmt.Printf("tx[%d:%s] passed =====\n", i, txs[i].Hash().String())
		} else {
			fmt.Printf("tx[%d:%s] failed =====\n", i, txs[i].Hash().String())
			depWrong++
		}
	}

	fmt.Printf("total tx:%d, total dep:%d, depCorrect:%d, depWrong:%d\n", len(txs), dag.TxCount(), depCorrect, depWrong)
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

func block340670() string {
	return string("340670,01f948a4f948a1c2c002c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c10ec1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c119c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c16ac1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c3c28195c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c144c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c111c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c132c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c170c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c12ec1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c126c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c12fc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820134c1c0c3c281b0c1c0c1c0c1c0c4c382017bc2c144c1c0c3c281d8c1c0c1c0c3c281c5c3c28187c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382012ec1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382019ac1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820156c1c0c1c0c1c0c1c0c1c0c1c0c2c14bc1c0c1c0c2c115c1c0c3c281d3c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382010fc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38201a2c1c0c1c0c1c0c1c0c2c178c3c281e4c1c0c1c0c1c0c4c382018ec1c0c2c124c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820176c1c0c1c0c1c0c2c170c1c0c1c0c1c0c1c0c4c382021ac3c28197c1c0c1c0c4c38201bec1c0c1c0c7c68201c2820215c5c40c820165c1c0c1c0c2c15bc1c0c1c0c3c281a0c1c0c1c0c1c0c1c0c1c0c4c382011ac1c0c1c0c4c38201b1c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382019dc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382017ac1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38201e4c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c167c1c0c1c0c1c0c2c156c1c0c1c0c1c0c1c0c4c38201eac1c0c3c281efc4c382017fc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820244c4c382022dc1c0c4c38201b7c1c0c1c0c1c0c4c382013ac1c0c1c0c1c0c4c382017ec1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38201edc1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c153c1c0c4c3820141c1c0c4c3820164c1c0c1c0c1c0c4c38201d5c1c0c2c13bc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38201acc1c0c4c382012dc1c0c1c0c1c0c1c0c1c0c2c15ac1c0c1c0c1c0c1c0c1c0c3c281a6c1c0c1c0c1c0c1c0c1c0c1c0c4c382018fc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c3c281e2c2c130c1c0c1c0c4c3820111c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c3c2819ec1c0c1c0c1c0c4c382026ec4c3820170c6c581bd820198c2c12cc1c0c4c3820173c1c0c1c0c1c0c1c0c1c0c1c0c1c0c3c281afc1c0c1c0c1c0c1c0c1c0c4c382014bc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820298c1c0c1c0c4c3820104c1c0c1c0c1c0c1c0c1c0c3c2818cc3c281fdc1c0c1c0c1c0c1c0c1c0c1c0c4c382011fc1c0c4c382016fc1c0c1c0c1c0c4c3820313c1c0c1c0c1c0c1c0c1c0c4c38201bcc2c171c4c3820228c1c0c1c0c1c0c1c0c1c0c1c0c3c281eac1c0c1c0c1c0c1c0c1c0c1c0c4c3820216c6c581c1820213c1c0c1c0c1c0c1c0c2c10fc1c0c1c0c4c382034dc1c0c2c124c1c0c1c0c4c382024dc4c3820272c4c382029ec1c0c1c0c1c0c2c16dc1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38202c1c1c0c2c163c1c0c1c0c1c0c1c0c1c0c1c0c3c281b2c1c0c1c0c1c0c1c0c4c38202eec1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c115c1c0c1c0c2c16ac1c0c1c0c4c3820190c4c3820270c4c382011cc4c382022fc1c0c3c28184c1c0c4c38201d6c1c0c1c0c4c3820283c4c38201adc1c0c1c0c4c3820247c4c3820347c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c14dc1c0c1c0c1c0c1c0c1c0c2c130c1c0c1c0c3c281b8c1c0c1c0c1c0c1c0c1c0c4c382035dc1c0c1c0c1c0c4c3820344c1c0c1c0c1c0c1c0c1c0c1c0c4c38201bdc1c0c1c0c4c3820301c4c382015ec4c3820303c4c382014ac7c68201788201b5c1c0c1c0c1c0c1c0c1c0c4c3820153c1c0c4c3820219c1c0c1c0c4c3820244c1c0c4c38201fec1c0c1c0c1c0c3c2819ec1c0c1c0c1c0c2c142c1c0c1c0c1c0c4c382027cc1c0c4c3820237c1c0c3c28193c1c0c1c0c1c0c7c68202bc8203aac1c0c4c3820143c4c382028fc4c3820290c1c0c3c281c5c4c382010fc1c0c1c0c4c3820186c4c382036bc1c0c1c0c3c281c1c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c12bc4c3820120c1c0c1c0c1c0c1c0c1c0c2c17ec4c38202b1c1c0c4c3820253c2c11bc1c0c1c0c4c382016fc1c0c1c0c1c0c4c3820373c1c0c1c0c4c382016cc1c0c1c0c1c0c1c0c1c0c1c0c4c3820257c1c0c1c0c1c0c1c0c3c28188c4c382021dc4c3820280c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38203f1c4c382023ec2c17fc4c38202b4c1c0c4c38203d5c1c0c1c0c1c0c1c0c1c0c1c0c2c154c1c0c1c0c1c0c1c0c1c0c1c0c4c38203dac1c0c1c0c4c3820416c4c3820386c4c38201f5c4c3820277c2c112c1c0c1c0c1c0c1c0c1c0c2c108c4c38201dcc1c0c4c3820403c3c2819fc1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38203cbc1c0c2c15ac4c3820409c1c0c1c0c1c0c4c382025fc1c0c1c0c4c3820155c1c0c1c0c1c0c1c0c1c0c1c0c4c382023dc4c38201dac1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38201b5c4c3820232c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c13ec1c0c1c0c1c0c3c281ccc1c0c1c0c1c0c4c3820267c1c0c1c0c1c0c1c0c1c0c1c0c4c38202d1c4c3820205c2c178c1c0c1c0c1c0c5c481bc81ebc7c682018c820325c4c38201e5c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820298c4c3820163c5c44682011ac4c3820318c4c3820221c1c0c4c3820372c1c0c1c0c3c281a7c1c0c1c0c1c0c1c0c2c129c1c0c4c38202d7c1c0c2c12bc1c0c1c0c1c0c1c0c1c0c4c382046fc1c0c1c0c7c682014c820394c1c0c4c38202f5c1c0c1c0c1c0c1c0c1c0c2c13ac1c0c4c382018cc1c0c1c0c3c281b7c4c382011dc4c38203d8c1c0c4c38203a6c1c0c1c0c4c38203d2c1c0c1c0c4c3820194c4c38204bec7c68201058202acc1c0c3c281d5c1c0c1c0c7c682028182037fc2c171c1c0c1c0c4c38203c6c1c0c1c0c1c0c1c0c4c38201fcc4c3820439c1c0c1c0c4c3820463c4c3820457c4c3820110c4c3820188c1c0c4c3820351c1c0c1c0c1c0c4c38202aec1c0c1c0c1c0c2c102c1c0c4c382037bc1c0c7c6820144820482c1c0c1c0c1c0c1c0c1c0c3c281cfc4c3820113c4c3820484c4c3820257c4c38202a6c3c2819bc1c0c4c33581a2c1c0c4c38202b6c1c0c1c0c4c3820388c4c3820488c1c0c1c0c4c3820423c1c0c1c0c1c0c1c0c4c38201b9c1c0c1c0c3c281e3c4c38203cdc4c3820232c1c0c1c0c4c3820388c4c382042fc4c382050cc1c0c1c0c1c0c3c281f2c4c3820385c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382037bc1c0c1c0c1c0c3c281fbc1c0c1c0c1c0c1c0c1c0c1c0c1c0c7c68201f48203cec1c0c1c0c1c0c1c0c1c0c7c682025682048ec1c0c7c68201678202d2c1c0c7c682033c8204a2c1c0c1c0c1c0c4c3820359c4c38201f7c1c0c4c3820406c1c0c4c38202b9c1c0c4c3820455c1c0c1c0c4c3820526c4c3820442c1c0c1c0c1c0c6c581b38202f6c4c3820114c1c0c1c0c1c0c4c38204d0c1c0c1c0c1c0c1c0c4c3820226c4c382033dc4c38203f4c1c0c1c0c3c281bdc1c0c1c0c3c281a2c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c3c281eec1c0c1c0c1c0c2c125c1c0c4c38201c4c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38203c1c1c0c4c382043dc4c38204a9c1c0c4c382055fc1c0c4c38202d1c1c0c1c0c4c382056dc4c3820559c1c0c1c0c1c0c4c382035fc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c7c682011882013cc1c0c1c0c4c3820241c1c0c1c0c4c382053cc1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820107c1c0c4c3820122c1c0c4c382059dc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382020cc2c110c4c38201f0c4c3820363c1c0c2c159c1c0c1c0c1c0c1c0c4c38205a0c1c0c1c0c1c0c1c0c7c68201ae8205adc1c0c1c0c4c38202abc1c0c1c0c4c382055ec4c382013dc3c2818fc1c0c1c0c1c0c1c0c4c382021ec4c382058cc4c382026dc4c3820263c1c0c1c0c3c281a4c1c0c4c3820466c1c0c1c0c1c0c1c0c1c0c4c3820229c4c3820414c1c0c1c0c1c0c1c0c7c682046b8205a3c4c3820168c1c0c1c0c1c0c4c3820315c4c3820179c1c0c1c0c4c3820558c1c0c1c0c1c0c2c149c4c382033bc1c0c4c38204dcc1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38205ccc1c0c4c3820485c1c0c4c38204c0c4c38202e5c4c38202b8c1c0c1c0c1c0c7c68203d78204c4c4c382022bc1c0c4c3820469c1c0c4c3820594c4c382014dc6c581b2820319c1c0c1c0c4c382012ec1c0c4c382046ac1c0c1c0c1c0c1c0c1c0c1c0c4c38202fec1c0c1c0c1c0c4c3820121c4c3820404c3c281dac4c38205d1c4c3820142c1c0c4c3820218c1c0c1c0c4c38205e7c1c0c1c0c1c0c1c0c4c38204cfc4c38203b5c4c3820147c1c0c4c38201f3c1c0c1c0c1c0c1c0c3c281d2c1c0c2c139c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382048ac1c0c1c0c1c0c1c0c1c0c4c3820494c1c0c1c0c2c141c1c0c2c13cc1c0c4c382027cc4c38203e2c1c0c1c0c1c0c1c0c1c0c1c0c4c3820135c2c166c4c382054cc1c0c1c0c1c0c4c38204b7c4c382063fc1c0c1c0c1c0c1c0c1c0c4c38205a8c1c0c1c0c4c38205e5c4c3820597c1c0c1c0c1c0c3c281cfc4c38201d4c4c382024bc1c0c1c0c2c17cc3c281e6c1c0c4c3820370c1c0c4c38202cbc1c0c1c0c4c38202c1c3c281e8c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38203bec1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c382012dc4c3820142c1c0c1c0c1c0c4c38203eac1c0c1c0c1c0c1c0c4c36681e2c1c0c4c3820166c4c3820147c1c0c4c3820307c4c38205c9c3c281efc1c0c4c38204f5c1c0c1c0c1c0c1c0c1c0c4c38203afc1c0c1c0c1c0c4c3820529c1c0c1c0c1c0c3c281fbc4c38203cfc1c0c1c0c4c38205bbc1c0c4c3820413c2c112c1c0c1c0c4c382058bc1c0c1c0c1c0c4c38201ecc1c0c4c38203ddc4c38204e6c4c38205ffc1c0c4c382018fc1c0c1c0c1c0c1c0c1c0c1c0c5c4078203a7c4c3820220c4c38204efc1c0c1c0c1c0c4c38203bec1c0c4c3820266c1c0c1c0c1c0c4c38202b7c1c0c1c0c1c0c1c0c4c3820639c1c0c4c3820514c1c0c1c0c4c3820472c6c581f9820316c4c38206cac1c0c1c0c7c6820196820656c1c0c4c382047dc7c682016e82045dc1c0c4c38205b7c4c382033ec1c0c4c3820544c1c0c4c38203dbc2c165c1c0c4c3820293c1c0c4c38205f0c4c38204c1c4c38202d5c1c0c1c0c2c122c1c0c4c38201dfc4c38204fdc4c382041cc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820598c1c0c4c382030dc4c38201e2c4c382068cc1c0c4c38202bac4c3820693c1c0c4c38201e1c1c0c1c0c1c0c1c0c1c0c1c0c4c382063ac1c0c1c0c4c38204b2c1c0c4c3820593c4c382066dc4c382029fc1c0c7c68201cd820489c1c0c1c0c4c3820676c1c0c1c0c1c0c1c0c1c0c4c3820581c4c38204b0c1c0c7c68203398205afc1c0c4c3820400c4c3820243c7c68201fa8202f9c1c0c1c0c1c0c1c0c4c3820527c1c0c4c382042fc1c0c1c0c1c0c1c0c4c38203a4c4c382032ec1c0c4c38206fac1c0c7c68202808204b9c1c0c1c0c1c0c7c68201f382061ec4c38202f2c1c0c4c382055bc4c38205a4c1c0c1c0c4c38206b5c4c3820279c4c3820728c4c3820714c4c3820239c1c0c1c0c1c0c1c0c4c382072dc4c3820271c1c0c1c0c1c0c1c0c3c281f7c1c0c1c0c4c3820233c1c0c4c38203bbc4c38204a5c1c0c4c3820289c4c38204bcc1c0c1c0c4c38203d3c1c0c1c0c1c0c4c382065cc4c382075bc1c0c4c38205ccc3c281afc1c0c1c0c1c0c1c0c1c0c7c6820240820704c1c0c6c581fc8206b6c1c0c1c0c1c0c1c0c4c382051fc1c0c4c38205b3c6c5819482027ec7c682010d820273c4c382036dc7c68201288202dcc1c0c1c0c1c0c1c0c1c0c7c682032482074bc1c0c1c0c4c3820430c4c3820129c4c382070bc4c3820192c4c3820391c1c0c4c382068bc1c0c1c0c1c0c1c0c4c3820405c4c38203b9c7c682043182045bc1c0c4c3820674c1c0c1c0c4c3820305c4c3820571c1c0c1c0c1c0c4c382058ac1c0c1c0c4c38203edc1c0c4c3820132c1c0c1c0c4c38205c8c1c0c1c0c4c38205d0c1c0c5c448820690c4c3820783c1c0c4c38205dec4c3820522c1c0c4c3820166c4c3820537c4c382033bc4c38207a7c4c3820620c4c3820705c1c0c4c3820587c1c0c4c38205e4c1c0c1c0c4c382051bc4c38203e0c1c0c1c0c1c0c1c0c4c382014ec1c0c4c3820340c4c382067ec1c0c1c0c1c0c2c10bc1c0c4c38207bac1c0c1c0c4c3820634c1c0c2c10dc1c0c1c0c1c0c1c0c1c0c4c3820138c1c0c1c0c1c0c1c0c4c3820127c4c382010dc1c0c1c0c4c3820624c4c38202a7c1c0c1c0c6c581f18203f5c1c0c1c0c1c0c4c382042dc1c0c1c0c1c0c1c0c7c68201e18207bcc4c3820433c4c38203d2c1c0c1c0c1c0c1c0c1c0c1c0c5c4388207f0c1c0c1c0c1c0c4c3820564c1c0c1c0c3c2818ec1c0c4c3820772c7c682014682075cc4c38203f6c1c0c1c0c1c0c1c0c1c0c4c3820543c4c382079ac4c3820499c1c0c4c3820312c3c28198c4c3820589c4c38201f8c1c0c1c0c1c0c4c3820302c4c38202dcc4c382064ec4c382067fc1c0c4c382032dc4c382047cc1c0c7c682031d820749c1c0c1c0c4c382050dc1c0c1c0c1c0c1c0c7c68203bd82080cc4c38206d2c7c68202e9820425c1c0c1c0c4c3820786c1c0c4c38207d2c1c0c7c6820390820421c1c0c1c0c1c0c4c38207cbc1c0c4c38201c9c4c3820805c4c38202a7c1c0c1c0c1c0c1c0c1c0c1c0c4c3820789c4c3820460c4c3820456c1c0c4c38202ebc4c3820791c1c0c4c38202e8c4c38207b4c4c38205dbc1c0c1c0c1c0c1c0c1c0c4c382028cc1c0c4c3820296c1c0c1c0c4c3820465c4c382023ec1c0c1c0c1c0c1c0c4c38202fdc4c38205cac1c0c1c0c1c0c4c38203edc4c382045ac1c0c4c3820758c1c0c1c0c1c0c1c0c1c0c7c682031c820732c1c0c4c3820339c4c382080ec4c3820211c4c38207e4c4c3820781c1c0c4c3820288c1c0c1c0c1c0c1c0c1c0c4c38201afc4c3820721c4c3820199c4c3820458c1c0c1c0c4c38204c2c1c0c1c0c1c0c1c0c1c0c1c0c4c382071fc4c38203e3c1c0c4c38202cec1c0c4c382020cc1c0c1c0c4c38206bfc1c0c1c0c1c0c1c0c4c3820645c4c38202c0c1c0c4c382047ec7c682053282060fc1c0c4c38206c9c1c0c2c103c7c6820491820573c4c3820810c1c0c7c682048b8205cec1c0c4c38206bac4c3820254c1c0c1c0c1c0c1c0c1c0c1c0c1c0c2c154c4c382017bc4c382040fc4c38203bdc4c3820195c4c3820840c7c682036582063cc4c38204fbc1c0c7c6820575820661c4c38205f5c4c382044dc1c0c1c0c1c0c1c0c1c0c4c3820292c4c38203f9c1c0c4c3820876c2c15dc2c119c4c38206acc4c3820328c1c0c4c38204b5c4c382050dc1c0c1c0c4c38204d2c1c0c4c38205bfc1c0c4c382068ac1c0c2c14ac4c3820277c2c173c4c3820323c2c167c4c38204adc4c382026cc4c38202a9c1c0c4c3820692c4c382032ec1c0c4c382020bc1c0c1c0c1c0c1c0c4c3820292c1c0c1c0c1c0c1c0c4c3820131c2c16fc2c114c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820666c1c0c1c0c1c0c1c0c4c38207b5c1c0c1c0c4c3820360c1c0c4c3820577c4c3820578c1c0c1c0c1c0c1c0c4c3820643c4c382083fc4c38207c4c1c0c4c3820600c1c0c4c382073ac1c0c1c0c4c38205adc1c0c4c382059bc1c0c1c0c1c0c7c682017982052bc1c0c1c0c4c382038ac1c0c2c134c7c6820452820733c4c38207c8c4c38202cfc4c3820603c4c3820520c4c382089ac1c0c1c0c1c0c7c682017c820572c1c0c6c58183820791c1c0c1c0c4c38205e0c2c11dc4c382042ac1c0c4c382028bc1c0c4c3820515c4c3820853c1c0c4c38203c0c1c0c1c0c1c0c4c38203c7c1c0c4c38206e7c1c0c1c0c1c0c1c0c4c3820243c4c3820730c1c0c4c382016cc1c0c4c38202f9c4c382021bc4c3820555c4c38205d1c4c38204c2c4c38205a1c1c0c1c0c1c0c4c382029dc4c3820379c4c3820850c7c68201aa8205a6c7c68201d18205e6c1c0c7c68204c18204c6c4c3820527c1c0c4c382075ac4c382061cc1c0c3c2819bc1c0c4c3820169c7c68206ba82091bc1c0c4c3820605c2c12cc1c0c4c3820408c4c38201fdc1c0c1c0c4c3820482c4c3820856c4c3820424c5c4748205f8c1c0c4c38205b0c1c0c4c3820677c4c382030dc4c382051ec4c3820157c4c38204c5c7c68201a28204f6c1c0c1c0c1c0c4c382070cc1c0c1c0c1c0c4c3820867c1c0c4c382085fc7c68206af82079fc4c38207d0c4c38207a1c2c11ec4c382052ac7c682034f8204f0c4c3820445c4c3820908c4c3820943c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38208e9c1c0c1c0c1c0c1c0c4c3820230c1c0c1c0c1c0c1c0c4c38205e2c4c382042ec1c0c2c164c1c0c1c0c4c382074bc1c0c4c38207c6c1c0c4c382010cc4c3820381c4c3820696c4c38201b8c4c38207dec1c0c2c108c4c38203e7c4c382047ac4c38204eac4c3820104c4c382073cc7c682096c82098dc4c38208abc1c0c1c0c7c68201a082034ec4c38205d9c7c6820347820505c1c0c4c38208abc1c0c4c3820464c4c3820506c1c0c1c0c1c0c1c0c1c0c1c0c4c38202f4c2c128c1c0c4c38202f7c4c3820972c1c0c1c0c1c0c7c68204b3820768c1c0c4c3820886c1c0c4c382062dc1c0c4c38208f3c4c38206f9c4c38202a0c4c3820294c7c6820608820657c1c0c1c0c4c38205f3c4c382096cc4c3820560c4c3820621c1c0c3c281dec1c0c1c0c7c68201138205fec4c38201a1c4c38202a5c1c0c2c168c4c3820756c1c0c4c3820451c4c382043cc3c281dbc4c38206f8c1c0c7c6820177820259c7c6820439820985c1c0c4c38208aac1c0c4c38206b5c1c0c1c0c4c38209bac1c0c4c3820969c1c0c1c0c4c38202bbc1c0c4c3820968c1c0c4c382016bc4c38207efc4c3820497c4c3820464c1c0c1c0c1c0c1c0c1c0c7c68206b98208a3c1c0c1c0c1c0c1c0c1c0c1c0c4c38209c0c7c68203fb820797c4c38203c7c7c68201d8820203c4c3820269c1c0c4c38207b8c1c0c7c682037f8205e3c1c0c4c382019bc1c0c1c0c4c38209acc7c68207dc820864c1c0c4c382049ec1c0c1c0c1c0c1c0c1c0c1c0c7c6820429820622c1c0c4c38202d0c4c38201ebc7c6820572820973c1c0c4c382087ac3c281dfc4c382065bc1c0c4c38208d5c1c0c1c0c4c3820997c7c682056c82070fc1c0c1c0c7c68205f4820a00c1c0c4c38201b7c1c0c1c0c6c581ca820467c1c0c1c0c5c40d820640c1c0c4c382010cc4c382050bc4c382098fc1c0c4c382063bc1c0c1c0c1c0c7c68202bf8205e5c1c0c4c38202aec1c0c1c0c4c382028ec4c3820156c1c0c1c0c1c0c4c382051dc1c0c4c38209b9c4c38209a5c4c38209cbc3c2818dc1c0c1c0c4c38206ddc4c382061ac1c0c1c0c4c3820829c1c0c4c3820918c7c6820621820715c4c38206a0c2c11cc7c68208428208b9c1c0c1c0c1c0c1c0c4c3820473c4c3820849c1c0c1c0c4c3820420c1c0c4c3820404c4c3820970c4c382029bc3c281e8c4c38202e2c1c0c1c0c4c3820515c4c382022bc4c382023ac1c0c4c38201c7c4c3820121c4c38209f5c5c41e820212c1c0c1c0c4c38209d6c1c0c1c0c4c382090dc4c3820521c1c0c4c3820630c1c0c4c382065dc1c0c4c38204dec1c0c1c0c1c0c4c38206bfc4c3820435c7c682082a8209c4c4c38207ddc5c4278207acc4c3820a23c4c3820562c4c38201b4c4c38205ddc1c0c4c3820500c1c0c1c0c1c0c4c38209dbc1c0c4c38207edc1c0c4c38208bdc1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38204d6c1c0c1c0c4c3820548c4c382066bc4c382064fc4c3820534c4c38202fbc4c3820158c1c0c4c38209f4c4c3820282c3c281b7c7c682014c82075dc4c38208a2c1c0c4c38202ccc4c38201cbc1c0c4c38208f9c4c382037ac2c105c4c382079fc1c0c1c0c4c38207ffc7c68204e68209c5c4c3820979c4c38203a9c4c38203d0c1c0c4c3820261c1c0c4c38209fdc4c38208d4c1c0c7c682036f8205d8c1c0c4c3820964c4c38204dac4c3820855c1c0c1c0c1c0c7c682026e8203b3c1c0c4c38205c2c1c0c4c38204e8c1c0c4c3820902c7c682054d8206e2c5c4488202e3c4c382024ac1c0c7c682021b8207b9c4c3820914c4c3820a03c3c281bbc4c382069cc4c3820794c7c682036c82039ec1c0c4c382049dc4c38208e6c3c281bac4c38203b8c1c0c4c3820569c4c382017dc1c0c2c152c4c3820a1cc4c38203d9c7c68203a0820501c4c3820a6dc1c0c7c682024f8204c9c4c3820a01c7c682063282098cc4c382058bc1c0c4c3820223c7c68202ef820427c4c38207ddc1c0c7c682062e820672c1c0c1c0c4c3820554c4c382066ec4c3820acac4c3820480c4c382037dc4c3820570c1c0c4c38206ebc4c38206b1c4c382081fc1c0c1c0c4c3820633c5c40e82058cc4c3820546c6c581e7820190c1c0c4c382076dc7c682026f8209cdc1c0c1c0c4c3820508c1c0c1c0c1c0c7c68207d482099bc1c0c4c3820687c1c0c1c0c7c6820923820a71c4c38208f6c4c382039cc4c38202e6c7c68207bd820866c1c0c5c436820a92c1c0c1c0c1c0c4c382085cc4c382095ac4c382013cc1c0c4c382045bc4c3820516c7c68208698208d5c4c382022ac4c3820390c4c3820419c1c0c1c0c1c0c4c3820601c1c0c1c0c2c142c1c0c4c38208efc4c38209f1c3c281fec7c68209578209aec1c0c4c382056ec1c0c1c0c4c382029bc4c38208acc1c0c1c0c1c0c1c0c1c0c1c0c4c38204bbc4c382045ec1c0c1c0c4c38206a2c4c3820518c1c0c7c68203ee820a72c2c15cc4c38207c1c4c38203b1c4c382025dc1c0c4c3820109c1c0c7c68206d082080fc4c3820447c1c0c1c0c4c382048fc7c6820159820b32c4c3820738c1c0c4c382076ac1c0c1c0c4c38207e2c4c3820b11c7c68208b28209f9c1c0c7c682039a820825c4c3820348c7c6820507820875c1c0c4c3820229c1c0c4c3820a89c1c0c4c3820ae5c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c38205bec1c0c2c16ec4c3820145c4c382031fc1c0c4c38206bdc1c0c4c3820590c4c3820a9bc4c3820b0bc4c38203f8c4c3820b1bc7c682062b8207e7c7c68204bd8209a8c1c0c4c382081bc4c382047cc1c0c4c382019bc4c3820a95c4c3820553c7c6820255820397c4c38206c1c4c38206e9c1c0c5c458820835c1c0c4c3820a09c1c0c4c38201b6c3c281c0c4c3820595c4c3820547c1c0c1c0c4c382028dc1c0c4c382071ec4c3820167c1c0c4c3820a45c1c0c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820a84c4c3820b82c7c6820773820852c1c0c1c0c4c3820807c4c3820adcc1c0c1c0c2c172c1c0c4c38204e2c1c0c1c0c1c0c7c6820106820927c4c38207e7c4c382096bc4c3820a47c7c68201bb820948c4c38205c1c1c0c1c0c6c581f28202f3c4c3820ae7c4c382093ec1c0c1c0c4c3820246c4c38205cdc4c3820945c4c38202ebc7c6820423820730c7c6820295820975c4c38207cec1c0c4c3820599c7c68205c382071ac4c3820443c7c6820502820890c7c68203b88208ddc1c0c4c382090bc4c3820a2dc1c0c4c3820316c4c3820b57c4c38202a4c7c68204de8207c0c3c281eac4c38209e3c1c0c1c0c1c0c1c0c1c0c3c281b6c4c3820682c4c38203f3c3c281d1c4c3820644c1c0c7c68209d1820a06c4c38208e4c1c0c4c38205afc4c3820432c1c0c1c0c7c6820382820a87c7c682010e820ac5c1c0c1c0c4c38203dbc1c0c4c3820b68c1c0c7c682034582081dc1c0c4c3820999c1c0c2c17ac4c3820a25c4c38204f1c4c38202adc1c0c1c0c4c38204afc1c0c1c0c4c382058ec7c68205ae820b6cc7c682059682097bc7c68201e0820370c1c0c4c382094ec7c68203a78206f7c4c3820513c4c382088ec1c0c1c0c4c38209cfc4c3820aa9c7c68209fa820b64c4c3820ae0c7c682077b820874c4c3820b19c4c3820936c1c0c1c0c4c3820221c4c3820336c4c3820899c4c382074ac1c0c1c0c4c38201b1c7c6820279820a81c1c0c1c0c4c38209f4c1c0c4c382088bc4c38207a5c1c0c7c68201d382075bc4c3820ad6c1c0c1c0c1c0c2c105c1c0c4c3820877c1c0c1c0c3c281f8c7c6820236820874c4c38209b5c1c0c1c0c1c0c1c0c1c0c1c0c4c382031bc4c38205dfc4c3820752c1c0c1c0c1c0c1c0c4c3820544c7c682067e82074dc7c68201ca820380c3c281c6c4c3820b84c1c0c4c38208d1c7c68204348209b6c1c0c1c0c4c3820af0c1c0c4c382052bc7c68206bb82072bc4c3820a8cc1c0c1c0c1c0c4c3820a7bc4c3820b5ec1c0c7c68208ee820b73c5c47a8201e3c4c38207d6c4c3820a1fc7c6820870820a07c1c0c1c0c7c6820422820616c3c28196c4c3820b3ac1c0c4c38203ccc6c581dd82051bc5c4778207e8c1c0c1c0c4c3820abbc7c68204e0820615c1c0c4c38206fec1c0c4c38203c5c4c38201afc7c68202888206fbc4c38204f3c1c0c1c0c4c38202b8c1c0c1c0c4c3820749c2c120c1c0c1c0c4c38202cac4c3820a52c1c0c4c3820701c4c3820c62c1c0c4c382076fc3c281bec4c3820a75c1c0c4c3820293c1c0c4c38204e7c7c682096e820bfcc4c382073cc4c3820187c1c0c3c28191c1c0c4c3820b8cc2c175c4c3820b32c1c0c4c382021fc4c3820b21c4c382020ec4c38206e1c7c682065f8206d4c1c0c2c113c4c38205cdc4c382068fc7c6820476820991c4c382071cc1c0c1c0c4c3820beec1c0c4c38209c1c1c0c4c3820a53c1c0c1c0c1c0c4c38208e1c7c682067f820afec7c682059b820889c4c3820662c4c3820642c4c38201fbc4c3820b59c4c38204dfc4c3820585c4c3820150c5c47482045fc1c0c1c0c4c3820bd0c6c581d7820bc7c4c382055dc4c38203dcc1c0c4c38206a5c4c3820aacc1c0c1c0c4c38208e7c4c3820226c4c3820932c1c0c1c0c1c0c4c3820c6ec4c3820b77c4c382032cc1c0c4c382045cc3c281c3c7c6820636820b31c1c0c4c3820b24c3c281c2c1c0c4c3820b45c4c3820b66c4c38208eac7c682070e820a14c4c38208f0c4c3820b23c1c0c1c0c4c3820ac1c4c38209d0c1c0c7c6820101820524c7c682083a8209e1c1c0c1c0c4c3820c9cc1c0c7c68208ae820a6dc4c38203abc7c68204ca820c03c4c3820605c4c3820a4fc4c3820986c4c3820c0bc1c0c1c0c1c0c1c0c4c3820bd3c4c3820a68c4c3820b04c1c0c1c0c7c6820932820c86c4c3820c74c1c0c4c38208cac1c0c1c0c1c0c7c68206b8820a61c1c0c1c0c4c3820b2cc4c3820399c4c382040dc1c0c7c68209bf820a36c4c3820cadc1c0c7c68205db820c2fc1c0c7c6820a8a820aeec4c3820628c4c3820217c4c3820916c4c3820686c4c38209d7c4c3820607c7c6820675820965c1c0c7c68202ed8208b6c4c3820affc1c0c4c382083bc4c38202a2c4c3820181c1c0c4c3820111c4c38205b6c7c6820449820652c4c3820c4bc1c0c7c6820ab1820addc4c3820358c1c0c4c38203a1c1c0c4c38206eec4c3820ceec7c68207e0820bcfc4c3820353c1c0c6c5819c8205c5c7c682052f820baec4c3820a62c4c38203ebc1c0c7c682020282050ac4c382042bc1c0c1c0c4c3820311c4c382032bc4c382084dc1c0c1c0c1c0c1c0c7c682064c8208dbc4c3820646c4c38202f2c1c0c1c0c1c0c1c0c1c0c1c0c1c0c4c3820958c4c3820561c1c0c4c3820a40c4c3820cccc4c3820358c7c6820392820649c4c382068fc5c46b82077ac4c3820709c1c0c4c3820ba2c4c3820330c1c0c4c38208ebc4c3820996c7c68207e48208cdc1c0c1c0c7c68205408207fdc6c581f58208a9c1c0c7c68205b582069dc4c3820803c4c382041ec1c0c1c0c1c0c7c682038e8207b2c7c682040e820549c1c0c4c3820cf2c4c38202d8c4c3820441c4c3820c1cc4c3820abec1c0c4c3820c97c4c382092cc1c0c4c3820105c7c68209f9820aa1c4c3820d05c4c38206ffc4c3820945c4c3820c0cc1c0c4c38207bfc1c0c1c0c2c17cc1c0c4c382011dc1c0c4c3820706c7c6820897820a91c1c0c4c3820c20c4c3820645c4c38207bec1c0c4c3820993c1c0c1c0c1c0c3c281dbc4c3820bf9c7c68207d8820b53c4c3820453c7c68203ef820504c7c6820584820b7fc4c3820b0dc4c3820637c1c0c4c3820b50c4c3820a22c4c382079cc7c6820503820a4ac4c3820b74c4c38203e6c1c0c1c0c4c3820921c1c0c4c3820920c4c3820b52c4c382095dc4c3820d5cc6c58189820d4bc4c382044cc1c0c1c0c4c3820c1dc7c6820123820306c4c3820a26c7c68204f0820af9c1c0c4c3820402c1c0c4c38201c9c4c3820386c7c68203108208d2c1c0c4c38207f4c1c0c4c3820606c4c3820412c7c682035a8204e8c4c38208d2c4c3820c3fc4c3820d89c4c3820278c4c3820a78c4c3820d26c7c68202a5820b41c1c0c7c6820a14820d5fc4c38209fec4c3820c51c4c3820cddc1c0c1c0c1c0c4c3820265c4c38206d2c4c382088dc4c38203f3c1c0c7c68203cb820748c4c38206c7c4c38209d3c4c3820959c4c382039fc1c0c1c0c1c0c1c0c4c3820b78c1c0c4c3820454c4c3820636c4c3820727c4c3820affc1c0c1c0c4c38207f6c4c3820c47c1c0c7c68201f2820630c4c3820981c1c0c4c382012ac3c28192c7c68205da8209fcc1c0c7c68201b8820c0cc1c0c4c3820711c4c382053dc1c0c4c38205f4c1c0c4c3820847c1c0c4c3820a48c7c682024c8206b7c1c0c7c68207fc820ad5c4c382073fc7c68205bd8207f5c4c38204d4c1c0c4c3820350c4c38207a4c4c3820b3fc7c682013682038ac4c3820b22c4c38206d5c4c3820adac3c281bac4c3820183c1c0c7c682044a820a85c4c382094ac1c0c1c0c1c0c7c6820ac0820d68c4c38208ccc4c38204aec4c3820a10c7c6820bab820d46c4c3820da9c4c3820311c4c3820c6ec4c38207f2c1c0c3c281e5c7c68202bf820dcdc7c6820802820c5fc1c0c4c3820d56c1c0c7c6820bcd820c53c1c0c1c0c4c38204afc7c68207a3820c1ac4c3820869c1c0c5c44382041ac4c3820b0bc1c0c1c0c1c0c4c3820c52c1c0c7c68201ee8202a4c1c0c7c68206688209e2c4c382044dc4c382041dc4c3820d9dc4c3820a1bc7c682058d820d4cc1c0c1c0c4c3820df4c4c3820565c1c0c1c0c7c6820225820747c4c38202c7c4c382056bc6c581e1820d4fc1c0c4c3820591c4c3820c87c1c0c4c3820a5ec1c0c4c3820745c4c382076bc7c6820969820c4ac4c38205a3c4c38204d3c7c68206e3820c19c7c68204c7820982c7c682064b820a5cc4c38204cbc1c0c1c0c7c682065e820b04c1c0c7c682085b8209e5c4c382059ac1c0c4c38209c4c4c3820821c1c0c1c0c1c0c4c3820a59c1c0c1c0c4c3820b8ec4c3820e07c1c0c7c682036e820612c1c0c4c3820b4dc4c3820938c4c3820d7ec4c3820abac4c3820470c4c3820aabc4c3820552c4c3820803c3c281dcc4c3820898c4c38201b0c4c38209d2c4c38208c9c7c6820ca6820cb9c1c0c1c0c1c0c4c382063dc1c0c7c682028b820c3ac4c38208f7c4c3820b20c4c3820750c1c0c1c0c4c3820c4bc7c6820141820681c1c0c4c3820dbac7c6820539820a18c4c38207afc4c3820b4bc7c68207d4820ddcc7c6820100820846c4c3820284c4c3820461c4c3820be7c7c68208ba820a58c4c3820919c4c3820d7ec4c3820799c7c6820c7f820dd9c1c0c4c3820968c1c0c4c38201bac4c3820bc8c1c0c7c68209d5820dfec4c38202e7c7c68203d4820bfec4c3820542c1c0c1c0c4c382062ec4c3820a1ac4c3820209c4c382070ac2c123c4c38204eec4c382041bc4c382069cc1c0c4c3820643c1c0c7c682090c820a67c4c3820d76c4c3820d36c4c382047ec4c38209e4c4c3820792c1c0c1c0c4c3820684c1c0c4c382019cc4c3820c89c4c3820bbcc7c6820583820cecc4c3820c8ac7c6820245820787c1c0c7c68206be82073dc1c0c4c382052ec4c38202c9c1c0c7c68204fc8208d3c4c3820b53c1c0c4c3820e5ac7c68207d7820856c1c0c6c581f9820d98c7c6820530820cc0c1c0c4c3820a74c7c68203308208d9c4c3820cc6c4c3820bedc7c6820887820a8cc1c0c4c38207acc1c0c4c3820a99c4c3820db6c7c68205d0820e47c1c0c7c6820524820939c4c3820822c7c682051f820ad9c4c3820959c7c682057b8207b6c1c0c7c682013b8207a9c1c0c4c3820983c4c38209a7c4c3820aedc2c11cc1c0c4c3820bbac4c3820175c7c68207de820858c7c68205b4820a66c1c0c4c3820262c1c0c1c0c1c0c4c3820498c1c0c6c581b0820be9c4c3820b69c4c3820df3c4c3820495c7c6820627820ae9c4c3820a04c4c3820ad8c7c68203df820b83c7c6820535820b06c1c0c4c3820b12c4c3820c9ec1c0c1c0c1c0c4c38201b2c1c0c1c0c4c3820d86c4c3820d6bc4c382012bc7c6820d15820df1c4c3820a6ec4c3820df6c1c0c4c3820c73c1c0c4c3820116c1c0c4c3820248c7c6820697820d32c1c0c1c0c7c68202b0820ebac1c0c1c0c7c6820442820e49c7c68204b48207f8c1c0c4c382041cc4c3820c66c4c3820d4cc7c682053f820721c1c0c4c3820ed5c7c6820bc9820d9ec4c3820534c1c0c7c682015a820ae6c7c6820357820c83c1c0c7c6820774820e57c3c281d9c4c3820726c4c3820185c1c0c1c0c4c382086ec1c0c1c0c4c3820c3ec1c0c7c682057482089ec4c3820ee9c7c6820477820a16c7c68208ce820c78c4c38203e5c4c3820685c1c0c1c0c7c68208e5820af7c4c3820bbec7c68201f6820a95c7c6820395820ab2c1c0c1c0c7c6820a30820bb7c4c3820646c2c160c1c0c4c382097bc4c3820d5ec7c68204e1820e5cc7c6820e7a820ec1c7c6820b35820d84c1c0c4c38209eec4c3820182c4c3820be3c4c3820b60c1c0c7c6820c7b820c85c4c3820a4dc7c68202b2820ecec4c382016dc4c38205a9c1c0c4c382031ac4c3820b28c4c3820e03c6c58190820acbc1c0c1c0c7c68205b98209b7c7c6820d21820d69c1c0c7c6820249820ee4c1c0c4c3820872c4c38202dac1c0c4c38201cac4c3820f03c1c0c1c0c4c3820c05c1c0c4c382048cc7c68209d5820c94c4c3820490c1c0c4c3820c8dc4c382023bc4c3820f21c1c0c4c3820619c1c0c4c3820839c1c0c4c3820ad1c7c6820389820702c1c0c7c6820450820768c1c0c1c0c1c0c7c6820165820e0cc4c38202dec1c0c4c3820e11c4c3820e85c1c0c7c6820ed2820f23c7c6820a64820cc7c4c3820a4ec4c3820667c1c0c7c682042c820b59c1c0c1c0c4c3820df0c4c3820c28c4c3820d09c4c3820e97c4c3820c13c4c38203d8c1c0c4c3820da7c4c3820cb3c1c0c4c38209d8c7c68206df820a3fc4c3820510c7c68202cd820d2ec4c3820304c4c3820c24c7c682067082095ec4c38202cfc7c68206ce820d60c7c6820c07820d0fc7c68204d4820638c1c0c4c3820a7ec4c3820623c1c0c7c6820505820f1ec4c3820863c4c3820433c7c682082f820efdc7c6820708820e61c4c3820e61c4c3820b9dc4c38207a3c5c43b8208fac4c382051ac4c38208c7c4c3820d2fc4c3820222c4c3820739c4c3820881c4c3820591c4c3820bd8c4c3820ad2c4c3820c8ec1c0c7c6820a5b820ce9c5c42d8204a2c1c0c4c38205efc1c0c7c68206d7820892c7c68208d8820b54c1c0c4c3820815c7c6820353820b1ac7c6820d1d820dbac4c3820d10c4c3820e5bc4c3820ce8c4c38209bac4c3820bf4c7c682065582098ec4c3820eafc1c0c7c6820b4a820c5ac4c3820f01c1c0c4c3820880c6c581be8201d1c4c382098bc4c3820342c1c0c4c3820b6fc7c6820dd1820f3cc4c3820d19c4c3820d3fc7c6820c06820f21c4c3820dbdc4c3820ee3c1c0c7c68207e1820e81c7c68204268207fcc4c38207c1c1c0c1c0c1c0c4c3820249c7c6820a9d820ad0c4c38204b8c4c3820cacc4c3820372c4c3820778c1c0c1c0c1c0c4c3820393c4c3820f9bc4c38207cfc7c682033f820712c4c3820579c1c0c7c68201b68207cac7c68207b1820d73c1c0c4c3820c00c7c6820832820952c4c3820154c4c3820fb6c4c3820b01c4c3820f82c7c6820556820d31c7c68202b38209d0c1c0c4c3820948c4c3820773c1c0c4c38205d7c4c3820f1cc7c6820b9d820f3dc7c68205dc8208a3c4c3820fa8c4c38205bac1c0c1c0c1c0c4c3820f07c1c0c1c0c1c0c4c3820af0c4c38204abc4c38208c5c4c3820f4ac7c682072b8209c6c1c0c4c3820cc5c4c3820a4ac4c3820dddc4c38209a9c4c3820e4ec4c3820d41c4c382076ec7c6820bd6820c48c1c0c4c3820414c7c6820c12820e37c4c3820653c7c68202e1820c61c1c0c1c0c7c6820aa1820cffc5c47282062cc7c68209af820a83c7c68208778209bbc4c382023ac1c0c1c0c4c3820f22c4c38201b4c1c0c7c68208f1820b7ec4c38201c1c1c0c4c3820671c4c3820bc2c1c0c4c38202e0c4c382027fc4c3820bebc4c3820a13c4c3820c9ac7c6820ab3820ee1c1c0c4c382054bc4c38209e9c4c3820571c5c447820f0bc4c3820300c2c169c7c682078b820a5cc1c0c4c3820e24c4c3820c41c5c45d820854c4c382010bc4c3820833c1c0c4c3820a73c4c3820821c1c0c4c3820313c7c6820eb7820fbcc1c0c1c0c4c3820f3ec4c3820461c4c3820f64c4c3820186c7c68202f1820905c4c3820550c7c682076c820912c3c281aac4c38203cec4c3820635c4c38209a4c7c68207c2820881c4c3820348c4c382084fc4c3820a2bc4c3820f69c4c3820800c4c38209f0c4c3820827c4c3820377c4c3820a61c1c0c4c3820287c4c38204cec1c0c4c3820f6ec4c3820148c1c0c1c0c1c0c4c3820ba1c4c3820e01c7c682056982080cc1c0c1c0c7c68202008209f7c1c0c4c3820aa6c1c0c1c0c1c0c4c3820ab0c4c3820ffdc1c0c7c682041e820782c4c382093cc4c382044bc4c382098ac1c0c4c3820b19c4c3820b2fc1c0c4c3820f25c4c3820567c1c0c4c3820331c4c3820f7fc4c38208c6c4c3820c31c4c38201a4c1c0c4c38207a8c7c6820765820d07c5c40c820c88c1c0c7c6820523820631c4c3820880c3c28199c4c382078dc4c382048bc3c28189c2c153c4c3820286c4c3820b23c1c0c4c3820722c1c0c4c3820f63c7c682067c820e27c4c38203a8c7c6820178820c35c7c682012c820f52c1c0c4c3820b7dc4c38206a7c4c3820e74c7c682065a82105ac2c111c4c3820804c6c581ae820e06c4c3820397c7c68207e3820dccc7c6820714820903c4c3820a7ac4c3820a19c1c0c4c382054ac7c682046c820606c7c68206c5820e58c4c3820c99c1c0c4c3821052c4c3820be0c1c0c4c382092ec3c2818ec4c38202f1c1c0c7c682061c820f40c1c0c4c3820db2c4c3820c15c4c3820d42c4c3820eebc4c3820dafc7c6820ee2820fa7c4c3820b94c1c0c4c3820951c4c3820a4dc4c3820888c4c3820535c7c68207be82107dc4c382031ec7c68206d9821004c4c3820170c1c0c4c382060fc4c38204c8c1c0c1c0c4c3821009c7c6820854820b0ec4c3820995c4c3820e2ec4c382074cc4c3820440c3c28196c4c382093dc7c6820b9e820be5c4c3820e9ac4c3820caec4c3820f86c7c6820a5f820ec6c4c38204cac7c682051d82084cc7c682021e820a1fc4c3820c7ac1c0c4c38205f1c4c3820c73c1c0c4c3820ba0c2c102c1c0c1c0c1c0c7c682095c8209abc7c68202108207f9c7c6820e0f820f9fc4c3820577c1c0c4c382094cc7c6820c56820ff8c4c3820eaec7c68203de8205a8c7c68206248208e4c4c38206d6c4c38205a7c4c3820296c4c38209cac1c0c4c3820a2fc1c0c4c382106bc4c3820e2ac7c6820e18820f05c4c382087cc7c6820c17820de5c4c3821030c6c581ed820f4cc4c382091ac1c0c1c0c7c682070082109cc3c281cac4c38204ddc7c6820912821000c4c3820f28c4c38201b3c4c38201a7c7c68205848208b0c1c0c1c0c4c3820b95c2c14ac1c0c7c6820827820edbc7c6820e22820f4dc4c3820b37c4c3820598c4c382087ec7c682093f820c01c4c38207d1c4c382057dc7c68206fc820fe7c7c682016d8206a8c1c0c4c38209ccc4c38207efc4c3821007c4c3820b25c7c682077d820a3dc7c6820462820dc1c7c68201c6820747c1c0c4c3820cbec1c0c4c382017cc1c0c4c3821031c1c0c1c0c4c38206abc4c3820df9c4c3820f68c4c3821091c1c0c1c0c4c3820de8c4c382084ec4c38202d6c4c3820278c4c3820aaac4c3820d57c1c0c7c6820e53821059c4c382106ec5c4098205acc4c3821049c4c3820ec5c1c0c4c3820a4ec1c0c4c382064ac3c281e7c7c682020d820da1c7c6820c92820e77c7c68204e9820b70c4c38205f6c4c3820ea3c4c38204f1c1c0c7c68205f0820e5ec1c0c4c3820b1fc1c0c1c0c1c0c1c0c4c3820a81c7c68202ab82072fc4c382048ec4c3820e98c1c0c1c0c7c6820369820e4dc1c0c7c6820e97820edec4c3820acdc4c38205f5c4c3820fd1c1c0c7c682067d8210f9c7c68201498208a1c4c3820337c1c0c7c682018d8206dac7c6820556820740c7c68204a5820ff1c7c6820b51820eb5c1c0c1c0c4c3820d22c3c281e6c4c3820ce1c4c3820efcc4c3821027c4c3820c36c4c3820125c7c68203d1820fb8c7c6820837820c77c4c3820f53c4c3820ffbc7c68202b7820bf0c4c3820f11c6c5819d820900c7c682078a8209ecc1c0c6c58182820d58c1c0c4c3820ba2c1c0c1c0c4c3820940c1c0c1c0c4c382082dc1c0c4c38210fbc1c0c4c38202f6c7c6820743820a87c1c0c4c382023cc4c3820b44c4c38206e0c7c6820ce082112cc4c38209adc4c3820d3bc1c0c7c6820c82821088c4c3821097c4c3820b05c4c3820608c1c0c4c3820abfc4c38208c3c7c6820144821095c7c6820213820d30c7c68206df820febc7c68202ff820d43c4c382063ec4c3820332c4c3820e9ec4c3820c5bc4c3820db9c4c3820ed3c4c3820ffdc1c0c7c6820bf7820ecfc4c382104ac7c68205a2821111c7c68203838207a2c4c3820e70c4c38206d1c4c38205d2c7c6820c5c820e16c4c3820cd7c3c28192c4c382056ac4c3820383c4c3820716c7c6820dac8210f3c1c0c4c382107cc4c3820f8cc4c38204d6c7c68203ec820b7cc4c3820f31c7c682018082038bc1c0c4c3820840c4c3820f85c4c3820117c4c38202b1c4c382030ac7c6820dc382110dc1c0c1c0c7c68207cd820feac4c38201f1c4c38204dbc4c3820e94c7c6820ba7820f73c4c382102bc1c0c4c3820ed2c1c0c1c0c4c3820baec3c281b1c3c28182c4c3820943c4c3820647c4c38202b9c7c6820a90820cd6c7c6820708820e91c7c6820c59820fa1c4c38209f7c1c0c7c682020e82071dc4c3820d71c4c3820a58c4c38209c9c7c6820764820cbdc4c3820fa0c7c6820a05821077c4c382015ec4c3820c3dc7c6820271820d51c4c3820b6dc4c382066ac7c6820c0982117cc1c0c4c3820f0ac4c3820e41c4c3820c93c4c3820e5dc5c421820de2c7c6820deb820f09c4c3820f27c4c3820146c7c68207ec820916c4c3820a17c4c3820c6fc4c3820a22c4c3820710c4c3820251c4c3820c43c4c38204aec4c3820446c4c38209c7c1c0c6c581c38203adc7c6820ca882117fc4c3820c57c4c3820304c4c3820981c7c6820dc482104fc4c3820460c4c382112ac4c3820941c7c6820c3f820e4bc7c6820660820896c1c0c4c3820cafc7c682040182113cc4c3821145c4c382086ac4c38207c2c1c0c4c3820f70c4c3821082c1c0c4c382067bc4c3821066c1c0c4c3820424c1c0c7c6820bcc8210f9c1c0c4c38211cbc1c0c7c68202178210b1c7c68204138210e2c7c6820edf82119dc1c0c5c45f820c14c4c38209fcc4c3820fbdc7c6820273820fa1c4c3820c5ec7c68207b3820c31c5c41b820a0ac7c6820242820e66c5c44f820641c4c3821024c1c0c4c3820361c4c3821150c4c38204f2c4c38201e0c4c3820873c4c3820fc7c4c3820e6ec7c6820d3f82119ac7c68205c2820f59c7c68208b8820cf3c4c38202c3c4c3821060c4c3820951c7c682071c820ff2c1c0c4c3820b74c5c418820543c4c3820327c1c0c4c38204c7c4c3820cdbc1c0c1c0c4c38210ddc4c38202f8c4c3820afec4c38208edc4c3820ea2c1c0c1c0c4c3820e09c4c3820ff9c4c38210c7c7c6820c0a820cf9c1c0c7c6820edc8210cfc7c6820c32820ed1c4c3820ed6c1c0c4c3820954c4c38202c6c4c3820e72c4c3820ec8c1c0c4c3820b26c4c3820e48c4c3820159c4c38209bdc7c6820dca8211c7c7c6820e9c821028c7c6820581820b29c4c38207ffc7c682110a8211d7c4c3820767c7c6820528820dd0c7c6820f34820f45c4c382118ac1c0c7c6820b93821169c4c382060ac7c6820254820b8ac1c0c1c0c4c3820e7fc7c68205298208d6c4c3820741c4c3820a21c1c0c4c3820ffcc7c682101c82105dc4c3820f7dc1c0c4c38209c3c4c38204a0c3c281f8c4c3820600c7c68201608202cac4c3820dfec7c68205a782118fc1c0c1c0c4c38210b8c4c3821050c1c0c7c6820276820a25c7c6820af8820c29c1c0c4c382011fc7c68209758210dcc4c3820616c4c3820d78c1c0c4c382046cc4c3820cb5c3c281f4c2c14bc1c0c1c0c1c0c7c6820823820c90c1c0c4c3820a83c7c6820b7382109ec7c682101f821121c1c0c7c68201dd820592c4c3820cf0c1c0c4c38204fdc4c3820b9ec4c38203b2c4c382086fc4c382029ac1c0c7c6820b2e820b9cc1c0c1c0c4c38206c6c4c3821161c4c3820b39c1c0c4c3820a43c4c3821153c4c38211aac7c6820d18820e26c4c3820fa4c4c3820c94c1c0c7c6820bef820df5c7c6820aca820fc1c7c6820e2d82124ec4c3820be4c4c3820f54c7c6820268820d85c4c3820a86c7c6820911820f99c7c682030c820992c4c3820425c4c38211fdc4c382093ac1c0c4c3820798c1c0c4c3820bf3c3c281a1c7c6820cc0820f65c4c38210cdc4c38205b8c4c3820d40c7c682067a820b30c7c682077e820b2bc1c0c7c6820a97820bfac4c38208f2c7c68201a8820d3ac4c38201bec4c38211d8c4c3820375c4c3820c48c1c0c4c38204fec5c4518204d0c4c38203f7c4c3820328c7c68202b28211b6c7c6820bfe821280c4c3820f89c4c3820df7c4c382033ac4c3821264c4c3820317c4c3820988c4c3820c29c1c0c1c0c4c3820740c7c68204f4821183c1c0c4c38208fcc4c3820ac2c1c0c4c3820e13c4c3820c2dc7c68205888205d9c4c3821241c4c3820804c7c682109d8210edc4c3820c10c4c3821116c4c3821207c4c3821033c4c3820568c4c3820e7bc4c3820d54c4c382102fc1c0c4c3820615c4c38207ebc1c0c4c382055ac4c3820b81c7c6820f92820fe0c2c145c4c3820a68c1c0c7c6820467821122c1c0c4c3820486c1c0c7c68202ee820681c4c3820795c4c3820509c4c38206bcc4c38212b2c1c0c4c3820d5cc4c38209e9c4c382061bc1c0c4c382102bc1c0c4c3820fddc1c0c4c3820d50c4c3820289c7c682048f820de9c4c3820cabc4c3820a24c3c28188c7c68207448211dec4c38203b5c4c3820825c1c0c4c3820601c4c3820e04c1c0c4c38208b1c4c382081cc4c3820ca3c4c3820658c4c38208a7c4c38201dec7c68201c282109fc1c0c4c3820299c1c0c4c3820500c4c38204aac1c0c1c0c1c0c1c0c7c68208bb8212bcc7c68205d6820f20c7c6820338820a8fc1c0c7c68202af82099dc7c68206958210e5c1c0c1c0c4c3820edac7c6820bec820dc1c4c3820946c1c0c7c68203e582060bc7c6820cd282101bc4c3820a4bc4c3820d35c7c68208e38209a2c1c0c1c0c4c3820f30c4c3820ebbc4c3820bd2c4c382080bc4c38210cbc7c68208b78208c0c4c3820b54c4c38202ddc4c382065bc4c3820b49c7c6820b6682116ac4c3820d64c4c382090bc4c3820ec1c6c581b4820489c7c6820335820a54c4c3821034c7c68201a98203fec7c68203ba821010c7c68203e7821186c1c0c4c382089fc4c3820b6dc7c6820612821196c7c6820f51821022c4c38211e1c3c281abc4c3820d27c4c3820d2fc4c3820860c4c38208fac4c3820c34c4c3820781c7c682032b820ecbc4c3820f2cc5c420820bd9c4c3820583c7c682086b820ae4c4c3820e44c7c6820274820ab0c7c6820d6d821233c1c0c7c6820d508212d4c7c6820b178211abc7c6820a02820bc6c2c13dc4c38210f4c7c6820437820dadc7c68204a8820862c1c0c1c0c7c68205fb821280c1c0c1c0c7c6820a8d8210abc4c38210a6c4c3820c93c4c3820921c4c38203e4c1c0c7c6820e6f821231c1c0c4c3820ee8c1c0c4c3820970c4c3820f8ec4c3820780c4c3820c22c1c0c1c0c4c3821311c4c38212fac4c38210dbc4c3820ba6c4c3820739c1c0c1c0c4c38205edc1c0c4c3820f82c1c0c7c68203748204d8c7c68204e4820d1ac4c3820c99c4c382096dc4c3821194c7c6820826820aa3c1c0c4c3820e5dc1c0c7c6820977820ff0c1c0c4c38202aac1c0c4c38205c6c1c0c1c0c7c6820a5a820dc6c5c47b820f8ec4c3820b34c4c3820673c7c6820fad821217c1c0c4c3821038c7c6820746820d44c4c382111fc4c382046ac4c3820a6ec4c3820ad9c7c6820be8820d53c7c6820634820a52c7c6820bf0820cddc1c0c4c38208f6c4c3820949")
}
