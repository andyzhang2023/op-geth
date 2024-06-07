package legacypool

import (
	"math/big"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	pendingCacheGauge     = metrics.NewRegisteredGauge("txpool/legacypool/pending/cache", nil)
	localCacheGauge       = metrics.NewRegisteredGauge("txpool/legacypool/local/cache", nil)
	pendingCacheDumpTimer = metrics.NewRegisteredTimer("txpool/legacypool/pending/cache/dump/duration", nil)
)

const (
	// pendingDumpInterval is the interval to dump pending transactions to cache.
	pendingDumpInterval = 200 * time.Millisecond
)

// copy of pending transactions
type cacheForMiner struct {
	gasTip  atomic.Pointer[big.Int]
	baseFee atomic.Pointer[big.Int]

	txLock   sync.Mutex
	pending  map[common.Address]map[*types.Transaction]struct{}
	locals   map[common.Address]bool
	addrLock sync.Mutex

	pendingWithTip    atomic.Value
	pendingWithoutTip atomic.Value

	shutdown chan struct{}
}

func newCacheForMiner() *cacheForMiner {
	cm := &cacheForMiner{
		pending:  make(map[common.Address]map[*types.Transaction]struct{}),
		locals:   make(map[common.Address]bool),
		shutdown: make(chan struct{}),
	}
	cm.gasTip.Store(big.NewInt(0))
	cm.baseFee.Store(big.NewInt(0))
	cm.pendingWithTip.Store(make(map[common.Address][]*txpool.LazyTransaction))
	cm.pendingWithoutTip.Store(make(map[common.Address][]*txpool.LazyTransaction))
	return cm
}

func (pc *cacheForMiner) Start(pool txpool.LazyResolver) {
	for {
		select {
		// Handle pool shutdown
		case <-pc.shutdown:
			return

		case <-time.After(pendingDumpInterval):
			pc.dump(pool, pc.gasTip.Load(), pc.baseFee.Load())
		}
	}
}

func (pc *cacheForMiner) Stop() {
	close(pc.shutdown)
}

func (pc *cacheForMiner) add(txs types.Transactions, signer types.Signer) {
	if len(txs) == 0 {
		return
	}
	pc.txLock.Lock()
	defer pc.txLock.Unlock()
	pendingCacheGauge.Inc(int64(len(txs)))
	for _, tx := range txs {
		addr, _ := types.Sender(signer, tx)
		slots, ok := pc.pending[addr]
		if !ok {
			slots = make(map[*types.Transaction]struct{})
			pc.pending[addr] = slots
		}
		slots[tx] = struct{}{}
	}
}

func (pc *cacheForMiner) del(txs types.Transactions, signer types.Signer) {
	if len(txs) == 0 {
		return
	}
	pc.txLock.Lock()
	defer pc.txLock.Unlock()
	for _, tx := range txs {
		addr, _ := types.Sender(signer, tx)
		slots, ok := pc.pending[addr]
		if !ok {
			continue
		}
		pendingCacheGauge.Dec(1)
		delete(slots, tx)
		if len(slots) == 0 {
			delete(pc.pending, addr)
		}
	}
}

func (pc *cacheForMiner) copy(enforceTip bool) map[common.Address][]*txpool.LazyTransaction {
	pc.txLock.Lock()
	defer pc.txLock.Unlock()
	if enforceTip {
		return pc.pendingWithTip.Load().(map[common.Address][]*txpool.LazyTransaction)
	} else {
		return pc.pendingWithoutTip.Load().(map[common.Address][]*txpool.LazyTransaction)
	}
}

func (pc *cacheForMiner) dump(pool txpool.LazyResolver, gasPrice, baseFee *big.Int) {
	defer func(t0 time.Time) {
		pendingCacheDumpTimer.UpdateSince(t0)
	}(time.Now())
	pending := make(map[common.Address]types.Transactions)
	pc.txLock.Lock()
	for addr, txlist := range pc.pending {
		pending[addr] = make(types.Transactions, 0, len(txlist))
		for tx := range txlist {
			pending[addr] = append(pending[addr], tx)
		}
	}
	pc.txLock.Unlock()

	pendingWithTip := make(map[common.Address][]*types.Transaction)
	pendingWithoutTip := make(map[common.Address][]*types.Transaction)
	for addr, txs := range pending {
		// sorted by nonce
		sort.Sort(types.TxByNonce(txs))
		pendingWithoutTip[addr] = txs
		// If the miner requests tip enforcement, cap the lists now
		if !pc.isLocal(addr) {
			for i, tx := range txs {
				if tx.EffectiveGasTipIntCmp(gasPrice, baseFee) < 0 {
					txs = txs[:i]
					break
				}
			}
		}
		pendingWithTip[addr] = txs
	}

	// convert to lazied transactions
	convert := func(pending map[common.Address][]*types.Transaction) map[common.Address][]*txpool.LazyTransaction {
		pendingLazied := make(map[common.Address][]*txpool.LazyTransaction)
		for addr, txs := range pending {
			if len(txs) > 0 {
				lazies := make([]*txpool.LazyTransaction, len(txs))
				for i, tx := range txs {
					lazies[i] = &txpool.LazyTransaction{
						Pool:      pool,
						Hash:      tx.Hash(),
						Tx:        tx,
						Time:      tx.Time(),
						GasFeeCap: tx.GasFeeCap(),
						GasTipCap: tx.GasTipCap(),
						Gas:       tx.Gas(),
						BlobGas:   tx.BlobGas(),
					}
				}
				pendingLazied[addr] = lazies
			}
		}
		return pendingLazied
	}

	pc.pendingWithTip.Store(convert(pendingWithTip))
	pc.pendingWithoutTip.Store(convert(pendingWithoutTip))
}

func (pc *cacheForMiner) markLocal(addr common.Address) {
	pc.addrLock.Lock()
	defer pc.addrLock.Unlock()
	localCacheGauge.Inc(1)
	pc.locals[addr] = true
}

func (pc *cacheForMiner) isLocal(addr common.Address) bool {
	pc.addrLock.Lock()
	defer pc.addrLock.Unlock()
	return pc.locals[addr]
}

func (pc *cacheForMiner) flattenLocals() []common.Address {
	pc.addrLock.Lock()
	defer pc.addrLock.Unlock()
	locals := make([]common.Address, 0, len(pc.locals))
	for addr := range pc.locals {
		locals = append(locals, addr)
	}
	return locals
}
