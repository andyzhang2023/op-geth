package core

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"math/big"
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
	runner := make(chan func())
	for i := 0; i < 16; i++ {
		go func() {
			for task := range runner {
				task()
			}
		}()
	}
	address := make([]*keypair, num)
	wait := sync.WaitGroup{}
	wait.Add(num)
	for i := 0; i < num; i++ {
		index := i
		runner <- func() {
			addr, key := randAddress()
			address[index] = &keypair{key, addr}
			wait.Done()
		}
	}
	wait.Wait()
	close(runner)
	return address
}

func BenchmarkParallel10KTxs(b *testing.B) {
	// Configure and generate a sample block chain
	var (
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		funds   = big.NewInt(1000000000000000)
		gspec   = &Genesis{
			Config:  params.TestChainConfig,
			Alloc:   GenesisAlloc{address: {Balance: funds}},
			BaseFee: big.NewInt(params.InitialBaseFee),
		}
		signer = types.LatestSigner(gspec.Config)
	)
	_, blocks, _ := GenerateChainWithGenesis(gspec, ethash.NewFaker(), 1024, func(i int, block *BlockGen) {
		block.SetCoinbase(common.Address{0x00})

		// If the block number is multiple of 3, send a few bonus transactions to the miner
		if i%3 == 2 {
			for j := 0; j < i%4+1; j++ {
				tx, err := types.SignTx(types.NewTransaction(block.TxNonce(address), common.Address{0x00}, big.NewInt(1000), params.TxGas, block.header.BaseFee, nil), signer, key)
				if err != nil {
					panic(err)
				}
				block.AddTx(tx)
			}
		}
		// If the block number is a multiple of 5, add an uncle to the block
		if i%5 == 4 {
			block.AddUncle(&types.Header{ParentHash: block.PrevBlock(i - 2).Hash(), Number: big.NewInt(int64(i))})
		}
	})
	// Import the chain as an archive node for the comparison baseline
	archiveDb := rawdb.NewMemoryDatabase()
	archive, _ := NewBlockChain(archiveDb, DefaultCacheConfigWithScheme(rawdb.PathScheme), gspec, nil, ethash.NewFaker(), vm.Config{}, nil, nil)
	defer archive.Stop()

	if n, err := archive.InsertChain(blocks); err != nil {
		panic(fmt.Sprintf("failed to process block %d: %v", n, err))
	}
}
