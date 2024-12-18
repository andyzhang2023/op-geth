package legacypool

import (
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

var (
	_ pendingCache = (*noneCacheForMiner)(nil)
)

type noneCacheForMiner struct {
	pool *LegacyPool
}

func newNoneCacheForMiner(pool *LegacyPool) *noneCacheForMiner {
	return &noneCacheForMiner{pool: pool}
}

func (nc *noneCacheForMiner) add(txs types.Transactions, signer types.Signer) {
	// do nothing
}

func (nc *noneCacheForMiner) del(txs types.Transactions, signer types.Signer) {
	// do nothing
}

func (nc *noneCacheForMiner) dump(filtered bool) map[common.Address][]*txpool.LazyTransaction {
	// dump all pending transactions from the pool
	nc.pool.mu.RLock()
	pending := make(map[common.Address]types.Transactions)
	for addr, txlist := range nc.pool.pending {
		pending[addr] = txlist.Flatten()
	}
	nc.pool.mu.RUnlock()
	filteredLazy := make(map[common.Address][]*txpool.LazyTransaction)
	allLazy := make(map[common.Address][]*txpool.LazyTransaction)
	for addr, txs := range pending {
		// sorted by nonce
		sort.Sort(types.TxByNonce(txs))
		filterd := nc.pool.pendingFilter(txs, addr)
		if len(txs) > 0 {
			lazies := make([]*txpool.LazyTransaction, len(txs))
			for i, tx := range txs {
				lazies[i] = &txpool.LazyTransaction{
					Pool:      nc.pool,
					Hash:      tx.Hash(),
					Tx:        tx,
					Time:      tx.Time(),
					GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
					GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
					Gas:       tx.Gas(),
					BlobGas:   tx.BlobGas(),
				}
			}
			allLazy[addr] = lazies
			filteredLazy[addr] = lazies[:len(filterd)]
		}
	}
	if filtered {
		return filteredLazy
	} else {
		return allLazy
	}
}

func (nc *noneCacheForMiner) markLocal(addr common.Address) {
	// do nothing
}

func (nc *noneCacheForMiner) IsLocal(addr common.Address) bool {
	nc.pool.mu.RLock()
	defer nc.pool.mu.RUnlock()
	return nc.pool.locals.contains(addr)
}

func (nc *noneCacheForMiner) flattenLocals() []common.Address {
	// return a copy of pool.locals
	nc.pool.mu.RLock()
	defer nc.pool.mu.RUnlock()
	var locals []common.Address = make([]common.Address, 0, len(nc.pool.locals.accounts))
	for addr := range nc.pool.locals.accounts {
		locals = append(locals, addr)
	}
	return locals
}

func (nc *noneCacheForMiner) sync2cache(pool txpool.LazyResolver, filter func(txs types.Transactions, addr common.Address) types.Transactions) {
	// do nothing
}
