package legacypool

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type TestPool struct {
	txs map[common.Hash]*types.Transaction
}

func (tp *TestPool) Get(hash common.Hash) *types.Transaction {
	return tp.txs[hash]
}

func TestCacheForMinerStartAndStop(t *testing.T) {
	// 1. test start and stop
	pool := &TestPool{
		txs: make(map[common.Hash]*types.Transaction),
	}
	signer := types.LatestSigner(params.OPBNBMainNetConfig)
	tx1 := types.NewTx(&types.LegacyTx{Nonce: 0})
	cm := newCacheForMiner()
	wait := sync.WaitGroup{}
	wait.Add(1)
	go func() {
		cm.Start(pool)
		wait.Done()
	}()
	cm.add(types.Transactions{tx1}, signer)
	pendingWithTip, pendingWithoutTip := cm.copy(true), cm.copy(false)
	if len(pendingWithTip) != 0 {
		t.Fatalf("pendingWithTip length is not 0")
	}
	if len(pendingWithoutTip) != 0 {
		t.Fatalf("pendingWithoutTip length is not 0")
	}
	time.Sleep(pendingDumpInterval + 10*time.Millisecond)
	pendingWithTip, pendingWithoutTip = cm.copy(true), cm.copy(false)
	if len(pendingWithTip) != 1 {
		t.Fatalf("pendingWithTip length is not 1")
	}
	if len(pendingWithoutTip) != 1 {
		t.Fatalf("pendingWithoutTip length is not 1")
	}
	cm.Stop()
	wait.Wait()
}

func TestCacheForMinerAdd(t *testing.T) {
	t.Parallel()
	// 1. load the initialed cache
	cm := newCacheForMiner()
	pendingWithTip, pendingWithoutTip := cm.copy(true), cm.copy(false)
	if pendingWithTip == nil {
		t.Fatalf("pendingWithTip is nil")
	}
	if pendingWithoutTip == nil {
		t.Fatalf("pendingWithoutTip is nil")
	}

	// 2. add a transaction to the cache
	pool := &TestPool{
		txs: make(map[common.Hash]*types.Transaction),
	}
	signer := types.LatestSigner(params.OPBNBMainNetConfig)
	tx1 := types.NewTx(&types.LegacyTx{Nonce: 0})
	cm.add(types.Transactions{tx1}, signer)
	go cm.dump(pool, big.NewInt(1), big.NewInt(1))
	time.Sleep(10 * time.Millisecond)

	if len(cm.pending) != 1 {
		t.Fatalf("pending length is not 1")
	}
	// test the pendingWithTip and pendingWithoutTip without local address
	pendingWithTip, pendingWithoutTip = cm.copy(true), cm.copy(false)
	if len(pendingWithTip) != 0 {
		t.Fatalf("pendingWithTip length is not 1")
	}
	if len(pendingWithoutTip) != 1 {
		t.Fatalf("pendingWithoutTip length is not 0")
	}

	sender, _ := types.Sender(signer, tx1)
	cm.markLocal(sender)
	go cm.dump(pool, big.NewInt(0), big.NewInt(0))
	time.Sleep(10 * time.Millisecond)
	// test the pendingWithTip and pendingWithoutTip with one local address
	pendingWithTip, pendingWithoutTip = cm.copy(true), cm.copy(false)
	if len(pendingWithTip) != 1 {
		t.Fatalf("pendingWithTip length is not 1")
	}
	if len(pendingWithoutTip) != 1 {
		t.Fatalf("pendingWithoutTip length is not 1")
	}
}
