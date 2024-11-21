package legacypool

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

type parallel chan func()

func (p parallel) do(f func()) {
	p <- f
}

func (p parallel) close() {
	close(p)
}

func (p parallel) start(pnum int) {
	for i := 0; i < pnum; i++ {
		go func() {
			for f := range p {
				f()
			}
		}()
	}
}

type keypair struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func randAddress() (common.Address, *ecdsa.PrivateKey) {
	// Generate a new private key using rand.Reader
	key, err := ecdsa.GenerateKey(crypto.S256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("Failed to generate private key: %v", err))
	}
	return crypto.PubkeyToAddress(key.PublicKey), key
}

func generateAddress(num int) []*keypair {
	proc := parallel(make(chan func()))
	proc.start(runtime.GOMAXPROCS(0))
	address := make([]*keypair, num)
	wait := sync.WaitGroup{}
	wait.Add(num)
	for i := 0; i < num; i++ {
		index := i
		proc.do(func() {
			addr, key := randAddress()
			address[index] = &keypair{key, addr}
			wait.Done()
		})
	}
	wait.Wait()
	proc.close()
	return address
}

func genesisAlloc(addresses []*keypair, funds *big.Int) core.GenesisAlloc {
	alloc := core.GenesisAlloc{}
	for _, addr := range addresses {
		alloc[addr.addr] = core.GenesisAccount{Balance: funds}
	}
	return alloc
}

func fetchBlocks(from, to uint64, endpoint string) []*types.Block {
	client, err := ethclient.Dial(endpoint)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	var blocks []*types.Block
	for i := from; i <= to; i++ {
		block, err := client.BlockByNumber(context.Background(), big.NewInt(int64(i)))
		if err != nil {
			panic(err)
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func loadGenesis(jsonfile string) *core.Genesis {
	// Open the JSON file
	file, err := os.Open(jsonfile)
	if err != nil {
		panic("failed to open json file, err=" + err.Error())
	}
	defer file.Close()

	// Read the file content
	bytes, err := io.ReadAll(file)
	if err != nil {
		panic(fmt.Sprintf("failed to read genesis file: %v", err))
	}

	// Unmarshal the JSON content into the Genesis struct
	var genesis core.Genesis
	if err := json.Unmarshal(bytes, &genesis); err != nil {
		panic(fmt.Sprintf("failed to unmarshal genesis JSON: %v", err))
	}
	return &genesis
}

func buildBlockChain(genesis *core.Genesis, parallel bool) *core.BlockChain {
	archiveDb := rawdb.NewMemoryDatabase()
	// Import the chain as an archive node for the comparison baseline
	archive, err := core.NewBlockChain(archiveDb, core.DefaultCacheConfigWithScheme(rawdb.PathScheme), genesis, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if err != nil {
		panic(err)
	}
	return archive
}

func InsertChain(bc *core.BlockChain, blocks []*types.Block) error {
	_, err := bc.InsertChain(blocks)
	return err
}

func TestTxpoolP2PParallel1(t *testing.T) {
	runTxpoolCaseTps50000(t, 1)
}

func TestTxpoolP2PParallel2(t *testing.T) {
	runTxpoolCaseTps50000(t, 2)
}

func TestTxpoolP2PParallel4(t *testing.T) {
	runTxpoolCaseTps50000(t, 8)
}

func runTxpoolCaseTps50000(t *testing.T, p2pParallel int) {
	var pool *txpool.TxPool
	var randomFrom, randomTo, genesisAlloc = prepareAddress(50000)
	var (
		baseFee = big.NewInt(params.InitialBaseFee)
		gspec   = &core.Genesis{
			Config:   params.TestChainConfig,
			Alloc:    genesisAlloc,
			BaseFee:  baseFee,
			GasLimit: 5000000000,
		}
		signer   = types.LatestSigner(gspec.Config)
		targetBN = 30 // 100 blocks totally
		tps      = 50000
		txs      = make(chan types.Transactions)
		cm       = initChain(gspec)
		err      error
	)
	// init txpool
	legacyPool := New(Config{GlobalSlots: 200000, GlobalQueue: 40000}, cm)

	txPools := []txpool.SubPool{legacyPool}
	pool, err = txpool.New(1, cm, txPools)
	if err != nil {
		t.Fatalf("Failed to create txpool: %v", err)
	}
	noncer := &cachedNoncer{pool: pool, cached: make(map[common.Address]uint64)}
	var executeFailed uint64 = 0
	cm.Gen = func(i int, block *core.BlockGen) {
		txs := pool.Pending(txpool.PendingFilter{})
		for _, txlist := range txs {
			for _, tx := range txlist {
				err := block.AddTxWithError(tx.Tx)
				if err != nil {
					//fmt.Printf("[ERROR] block:%d, txs:%d, addr:%s, err:%v\n", i, len(txlist), addr.String(), err)
					atomic.AddUint64(&executeFailed, 1)
				}
			}
		}
	}
	// generate txs at rate of 5000 txs per second
	var addSleep time.Duration
	generateTxs := func(targetBN, tps int) {
		for n := 0; n < targetBN*tps; {
			t0 := time.Now()
			// split txs into 128-size chunks
			currLoop := 0
			for i := 0; i < tps; {
				batch := 128
				if i+batch > tps {
					batch = tps - i
				}
				txs <- generateTxs(batch, noncer, signer, baseFee, randomFrom, randomTo)
				i += batch
				n += batch
				currLoop += batch
			}
			sleep := time.Second - time.Since(t0)
			if sleep > 0 {
				addSleep += sleep
				time.Sleep(sleep)
				fmt.Printf("[txpool.Add]txs:%d, sleep:%s\n", currLoop, sleep)
			}
		}
		close(txs)
	}
	for i := 0; i < 8; i++ {
		go generateTxs(targetBN, tps)
	}
	// put txs into txpool
	var addFailed uint64
	parallel := func() {
		for tx := range txs {
			errs := pool.Add(tx, false, false)
			for i, err := range errs {
				sender, serr := types.Sender(signer, tx[i])
				if serr != nil {
					panic("invalid sender")
				}
				if err != nil {
					atomic.AddUint64(&addFailed, 1)
					// clear noncer
					noncer.clear(sender)
				} else {
					noncer.inc(sender)
				}
			}
		}
	}
	for i := 0; i < p2pParallel; i++ {
		go parallel()
	}
	// generate blocks from txpool
	t0 := time.Now()
	var buildBlockSleep time.Duration
	for i := 0; i < targetBN; i++ {
		tone := time.Now()
		cm.NextBlock()
		sleep := time.Second - time.Since(tone)
		// one block per second
		if sleep > 0 {
			buildBlockSleep += sleep
			time.Sleep(sleep)
		} else {
			sleep = 0
		}
		curr := cm.GetBlock(cm.CurrentBlock().Hash(), 0)
		pending, queued := legacyPool.Stats()
		fmt.Printf("[done]block:%d, txs:%d, pending:%d, queued:%d, addFailed:%d, sleep:%s\n", i, len(curr.Transactions()), pending, queued, addFailed, sleep)
	}
	cost := time.Since(t0)
	blocks, _ := cm.BlocksAndReceipts()

	// calculate the txs in the blocks, and the tps at average
	totaltxs := 0
	for i := 0; i < len(blocks); i++ {
		totaltxs += len(blocks[i].Transactions())
	}
	fmt.Printf("durations:%s, total txs: %d, tps: %f, addTxSleep:%s, buildBlockSleep:%s, executedFailed:%d\n", cost, totaltxs, float64(totaltxs)/cost.Seconds(), addSleep, buildBlockSleep/time.Duration(len(blocks)), executeFailed)

}

func prepareAddress(addrNum int) (chan *keypair, chan common.Address, types.GenesisAlloc) {
	// Configure and generate a sample block chain
	funds := big.NewInt(1000000000000000)
	addresses := generateAddress(addrNum)
	genesisAlloc := genesisAlloc(addresses, funds)
	randomTo := make(chan common.Address, addrNum)
	randomFrom := make(chan *keypair, addrNum)
	for addr := range genesisAlloc {
		randomTo <- addr
	}
	for _, addr := range addresses {
		randomFrom <- addr
	}
	return randomFrom, randomTo, genesisAlloc
}

type cachedNoncer struct {
	pool   *txpool.TxPool
	lock   sync.RWMutex
	cached map[common.Address]uint64
}

func (cn *cachedNoncer) nonce(addr common.Address) uint64 {
	cn.lock.Lock()
	defer cn.lock.Unlock()
	if _, ok := cn.cached[addr]; !ok {
		cn.cached[addr] = cn.pool.Nonce(addr)
	}
	return cn.cached[addr]
}

func (cn *cachedNoncer) inc(addr common.Address) {
	cn.lock.Lock()
	defer cn.lock.Unlock()
	cn.cached[addr]++
}

func (cn *cachedNoncer) clear(addr common.Address) {
	cn.lock.Lock()
	defer cn.lock.Unlock()
	delete(cn.cached, addr)
}

func generateTxs(num int, noncer *cachedNoncer, signer types.Signer, basefee *big.Int, randomFrom chan *keypair, randomTo chan common.Address) []*types.Transaction {
	txs := make([]*types.Transaction, num)
	for i := 0; i < num; i++ {
		// borrow an address
		to := <-randomTo
		from := <-randomFrom
		tx, err := types.SignTx(types.NewTransaction(noncer.nonce(from.addr), to, big.NewInt(10), params.TxGas, basefee, nil), signer, from.key)
		if err != nil {
			panic(err)
		}
		txs[i] = tx
		randomTo <- to
		randomFrom <- from
	}
	return txs
}

func initChain(gspec *core.Genesis) (cm *core.ChainMaker) {
	db := rawdb.NewMemoryDatabase()
	triedb := triedb.NewDatabase(db, triedb.HashDefaults)
	defer triedb.Close()
	_, err := gspec.Commit(db, triedb)
	if err != nil {
		panic(err)
	}
	cm = core.BuildChainMaker(gspec.Config, gspec.ToBlock(), ethash.NewFaker(), db)
	return cm
}

func TestTxpool(b *testing.T) {
	addrNum := 1000
	// Configure and generate a sample block chain
	funds := big.NewInt(1000000000000000)
	addresses := generateAddress(addrNum)
	genesisAlloc := genesisAlloc(addresses, funds)
	randomAddr := make(chan common.Address, addrNum)
	for addr := range genesisAlloc {
		randomAddr <- addr
	}
	var (
		gspec = &core.Genesis{
			Config:   params.TestChainConfig,
			Alloc:    genesisAlloc,
			BaseFee:  big.NewInt(params.InitialBaseFee),
			GasLimit: 500000000,
		}
		signer = types.LatestSigner(gspec.Config)
	)

	_, blocks, _ := core.GenerateChainWithGenesis(gspec, ethash.NewFaker(), 20, func(i int, block *core.BlockGen) {
		block.SetCoinbase(common.Address{0x00})
		txs := make([]*types.Transaction, len(addresses))
		for i, addr := range addresses {
			// borrow an address
			to := <-randomAddr
			from := addr
			tx, err := types.SignTx(types.NewTransaction(block.TxNonce(from.addr), to, big.NewInt(1000), params.TxGas, block.Header().BaseFee, nil), signer, from.key)
			if err != nil {
				panic(err)
			}
			txs[i] = tx
			randomAddr <- to
		}
		for i := 0; i < len(txs); i++ {
			block.AddTx(txs[i])
		}
	})

	archiveDb := rawdb.NewMemoryDatabase()
	// Import the chain as an archive node for the comparison baseline
	archive, _ := core.NewBlockChain(archiveDb, core.DefaultCacheConfigWithScheme(rawdb.PathScheme), gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	if n, err := archive.InsertChain(blocks); err != nil {
		panic(fmt.Sprintf("failed to process block %d: %v", n, err))
	}
	archive.Stop()
}

var cacheLock = sync.RWMutex{}
var cached = make(map[common.Address]uint64)

func getAddressSafe(addr common.Address) uint64 {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	return cached[addr]
}

func getAddress(addr common.Address) uint64 {
	return cached[addr]
}
