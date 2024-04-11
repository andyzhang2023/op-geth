package legacypool

import (
	"math/big"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	pendingCacheGauge = metrics.NewRegisteredGauge("txpool/legacypool/pending/cache", nil)
	localCacheGauge   = metrics.NewRegisteredGauge("txpool/legacypool/local/cache", nil)
)

// copy of pending transactions
type cacheForMiner struct {
	txLock             sync.Mutex
	pending            map[common.Address]map[*types.Transaction]struct{}
	locals             map[common.Address]bool
	pendingWithoutTips atomic.Value
	pendingWithTips    atomic.Value
	addrLock           sync.Mutex

	// waitForPromote is used to reduce the "nonce too low" transactions in the pending list.
	// we use mutex instead of channel to avoid potential too much waiting time when txpool is
	// too busy to send the signal
	waitForDemote sync.Mutex
}

func newCacheForMiner() *cacheForMiner {
	cm := &cacheForMiner{
		pending: make(map[common.Address]map[*types.Transaction]struct{}),
		locals:  make(map[common.Address]bool),
	}
	lazyPendingWithTips, lazyPendingWithoutTips := make(map[common.Address][]*txpool.LazyTransaction), make(map[common.Address][]*txpool.LazyTransaction)
	cm.pendingWithTips.Store(lazyPendingWithTips)
	cm.pendingWithoutTips.Store(lazyPendingWithoutTips)
	return cm
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

func (pc *cacheForMiner) dump(pool txpool.LazyResolver, gasPrice, baseFee *big.Int) {
	pending := make(map[common.Address]types.Transactions)
	pc.txLock.Lock()
	for addr, txlist := range pc.pending {
		pending[addr] = make(types.Transactions, 0, len(txlist))
		for tx := range txlist {
			pending[addr] = append(pending[addr], tx)
		}
	}
	pc.txLock.Unlock()
	// sorted by nonce
	for addr := range pending {
		sort.Sort(types.TxByNonce(pending[addr]))
	}
	pendingWithTips := make(map[common.Address]types.Transactions)
	for addr, txs := range pending {
		// If the miner requests tip enforcement, cap the lists now
		if !pc.isLocal(addr) {
			for i, tx := range txs {
				if tx.EffectiveGasTipIntCmp(gasPrice, baseFee) < 0 {
					txs = txs[:i]
					break
				}
			}
		}
		if len(txs) > 0 {
			pendingWithTips[addr] = txs
		}
	}

	// convert into LazyTransaction
	lazyPendingWithTips, lazyPendingWithoutTips := make(map[common.Address][]*txpool.LazyTransaction), make(map[common.Address][]*txpool.LazyTransaction)
	for addr, txs := range pending {
		for i, tx := range txs {
			lazyTx := &txpool.LazyTransaction{
				Pool:      pool,
				Hash:      tx.Hash(),
				Tx:        tx,
				Time:      tx.Time(),
				GasFeeCap: tx.GasFeeCap(),
				GasTipCap: tx.GasTipCap(),
				Gas:       tx.Gas(),
				BlobGas:   tx.BlobGas(),
			}
			lazyPendingWithoutTips[addr] = append(lazyPendingWithoutTips[addr], lazyTx)
			if len(pendingWithTips[addr]) > i {
				lazyPendingWithTips[addr] = append(lazyPendingWithTips[addr], lazyTx)
			}
		}
	}

	// store pending
	pc.pendingWithTips.Store(lazyPendingWithTips)
	pc.pendingWithoutTips.Store(lazyPendingWithoutTips)
}

func (pc *cacheForMiner) pendingTxs(enforceTips bool) map[common.Address][]*txpool.LazyTransaction {
	pc.waitForDemote.Lock()
	defer pc.waitForDemote.Unlock()
	if enforceTips {
		return pc.pendingWithTips.Load().(map[common.Address][]*txpool.LazyTransaction)
	} else {
		return pc.pendingWithoutTips.Load().(map[common.Address][]*txpool.LazyTransaction)
	}
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
