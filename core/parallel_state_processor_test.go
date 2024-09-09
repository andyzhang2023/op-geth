package core

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
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
	proc.start(16)
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

func genesisAlloc(addresses []*keypair, funds *big.Int) GenesisAlloc {
	alloc := GenesisAlloc{}
	for _, addr := range addresses {
		alloc[addr.addr] = GenesisAccount{Balance: funds}
	}
	return alloc
}

func BenchmarkAkaka(b *testing.B) {
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
		gspec = &Genesis{
			Config:   params.TestChainConfig,
			Alloc:    genesisAlloc,
			BaseFee:  big.NewInt(params.InitialBaseFee),
			GasLimit: 500000000,
		}
		signer = types.LatestSigner(gspec.Config)
	)

	_, blocks, _ := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 20, func(i int, block *BlockGen) {
		block.SetCoinbase(common.Address{0x00})
		txs := make([]*types.Transaction, len(addresses))
		for i, addr := range addresses {
			// borrow an address
			to := <-randomAddr
			from := addr
			tx, err := types.SignTx(types.NewTransaction(block.TxNonce(from.addr), to, big.NewInt(1000), params.TxGas, block.header.BaseFee, nil), signer, from.key)
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

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		archiveDb := rawdb.NewMemoryDatabase()
		// Import the chain as an archive node for the comparison baseline
		archive, _ := NewBlockChain(archiveDb, DefaultCacheConfigWithScheme(rawdb.PathScheme), gspec, nil, ethash.NewFaker(), vm.Config{EnableParallelExec: true}, nil, nil)
		if n, err := archive.InsertChain(blocks); err != nil {
			panic(fmt.Sprintf("failed to process block %d: %v", n, err))
		}
		archive.Stop()
	}
}

func BenchmarkSeqAndParallelRead(b *testing.B) {
	// generate a set of addresses
	// case 1: read the state sequentially
	// case 2: read them in parallel
	address := generateAddress(10000)
	cached := make(map[common.Address]uint64)
	for i := 0; i < len(address); i++ {
		cached[address[i].addr] = uint64(i)
	}
	getAddress := func(addr common.Address) uint64 {
		return cached[addr]
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// iterate all the address
		for i := 0; i < len(address); i++ {
			getAddress(address[i].addr)
		}
	}
}

func BenchmarkParallelRead(b *testing.B) {
	// generate a set of addresses
	// case 1: read the state sequentially
	// case 2: read them in parallel
	address := generateAddress(10000)
	cacheLock := sync.RWMutex{}
	cached := make(map[common.Address]uint64)
	for i := 0; i < len(address); i++ {
		cached[address[i].addr] = uint64(i)
	}
	getAddress := func(addr common.Address) uint64 {
		cacheLock.RLock()
		defer cacheLock.RUnlock()
		return cached[addr]
	}

	proc := parallel(make(chan func()))
	proc.start(runtime.NumCPU())
	wait := sync.WaitGroup{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// iterator all the address in parallel
		wait.Add(len(address))
		for i := 0; i < len(address); i++ {
			index := i
			proc.do(func() {
				getAddress(address[index].addr)
				wait.Done()
			})
		}
		wait.Wait()
	}
	proc.close()
}
