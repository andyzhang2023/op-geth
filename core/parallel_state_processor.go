package core

import (
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/metrics"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

const (
	parallelPrimarySlot = 0
	parallelShadowSlot  = 1
	stage2CheckNumber   = 30 // ConfirmStage2 will check this number of transaction, to avoid too busy stage2 check
	stage2AheadNum      = 3  // enter ConfirmStage2 in advance to avoid waiting for Fat Tx
)

type ParallelStateProcessor struct {
	StateProcessor
	parallelNum           int          // leave a CPU to dispatcher
	slotState             []*SlotState // idle, or pending messages
	allTxReqs             []*ParallelTxRequest
	txResultChan          chan *ParallelTxResult      // to notify dispatcher that a tx is done
	mergedTxIndex         atomic.Int32                // the latest finalized tx index
	pendingConfirmResults map[int][]*ParallelTxResult // tx could be executed several times, with several result to check
	unconfirmedResults    *sync.Map                   // for stage2 confirm, since pendingConfirmResults can not be accessed in stage2 loop
	unconfirmedDBs        *sync.Map                   // intermediate store of slotDB that is not verified
	slotDBsToRelease      []*state.ParallelStateDB
	stopSlotChan          chan struct{}
	stopConfirmChan       chan struct{}
	debugConflictRedoNum  int32

	confirmStage2Chan     chan int
	stopConfirmStage2Chan chan struct{}
	txReqExecuteRecord    map[int]int
	txReqExecuteCount     int
	inConfirmStage2       bool
	targetStage2Count     int
	nextStage2TxIndex     int
	delayGasFee           bool
}

func newParallelStateProcessor(config *params.ChainConfig, bc *BlockChain, engine consensus.Engine, parallelNum int) *ParallelStateProcessor {
	processor := &ParallelStateProcessor{
		StateProcessor: *NewStateProcessor(config, bc, engine),
		parallelNum:    parallelNum,
	}
	processor.init()
	return processor
}

type MergedTxInfo struct {
	slotDB              *state.StateDB
	StateObjectSuicided map[common.Address]struct{}
	StateChangeSet      map[common.Address]state.StateKeys
	BalanceChangeSet    map[common.Address]struct{}
	CodeChangeSet       map[common.Address]struct{}
	AddrStateChangeSet  map[common.Address]struct{}
	txIndex             int
}

type SlotState struct {
	pendingTxReqList  []*ParallelTxRequest
	primaryWakeUpChan chan struct{}
	shadowWakeUpChan  chan struct{}
	primaryStopChan   chan struct{}
	shadowStopChan    chan struct{}
	activatedType     int32 // 0: primary slot, 1: shadow slot
}

type ParallelTxResult struct {
	executedIndex int32 // record the current execute number of the tx
	slotIndex     int
	txReq         *ParallelTxRequest
	receipt       *types.Receipt
	uncommited    *state.UncommittedDB
	slotDB        *state.ParallelStateDB
	gpSlot        *GasPool
	evm           *vm.EVM
	result        *ExecutionResult
	originalNonce *uint64
	err           error
}

type ParallelTxRequest struct {
	txIndex         int
	baseStateDB     *state.StateDB
	staticSlotIndex int
	tx              *types.Transaction
	gasLimit        uint64
	msg             *Message
	block           *types.Block
	vmConfig        vm.Config
	usedGas         uint64
	curTxChan       chan int
	runnable        int32 // 0: not runnable 1: runnable - can be scheduled
	executedNum     atomic.Int32
	conflictIndex   atomic.Int32 // the conflicted mainDB index, the txs will not be executed before this number
	useDAG          bool
}

// init to initialize and start the execution goroutines
func (p *ParallelStateProcessor) init() {
	log.Info("Parallel execution mode is enabled", "Parallel Num", p.parallelNum,
		"CPUNum", runtime.NumCPU())
}

// resetState clear slot state for each block.
func (p *ParallelStateProcessor) resetState(txNum int, statedb *state.StateDB) {
	//if txNum == 0 {
	//	return
	//}
	p.mergedTxIndex.Store(-1)
	p.debugConflictRedoNum = 0
	p.inConfirmStage2 = false

	statedb.PrepareForParallel()
	p.allTxReqs = make([]*ParallelTxRequest, 0)
	p.slotDBsToRelease = make([]*state.ParallelStateDB, 0, txNum)

	stateDBsToRelease := p.slotDBsToRelease
	go func() {
		for _, slotDB := range stateDBsToRelease {
			slotDB.PutSyncPool()
		}
	}()
	for _, slot := range p.slotState {
		slot.pendingTxReqList = make([]*ParallelTxRequest, 0)
		slot.activatedType = parallelPrimarySlot
	}
	p.unconfirmedResults = new(sync.Map)
	p.unconfirmedDBs = new(sync.Map)
	p.pendingConfirmResults = make(map[int][]*ParallelTxResult, 200)
	p.txReqExecuteRecord = make(map[int]int, 200)
	p.txReqExecuteCount = 0
	p.nextStage2TxIndex = 0
}

// Benefits of StaticDispatch:
//
//	** try best to make Txs with same From() in same slot
//	** reduce IPC cost by dispatch in Unit
//	** make sure same From in same slot
//	** try to make it balanced, queue to the most hungry slot for new Address
func (p *ParallelStateProcessor) doStaticDispatch(txReqs []*ParallelTxRequest) {
	fromSlotMap := make(map[common.Address]int, 100)
	toSlotMap := make(map[common.Address]int, 100)
	for _, txReq := range txReqs {
		var slotIndex = -1
		if i, ok := fromSlotMap[txReq.msg.From]; ok {
			// first: same From goes to same slot
			slotIndex = i
		} else if txReq.msg.To != nil {
			// To Address, with txIndex sorted, could be in different slot.
			if i, ok := toSlotMap[*txReq.msg.To]; ok {
				slotIndex = i
			}
		}

		// not found, dispatch to most hungry slot
		if slotIndex == -1 {
			slotIndex = p.mostHungrySlot()
		}
		// update
		fromSlotMap[txReq.msg.From] = slotIndex
		if txReq.msg.To != nil {
			toSlotMap[*txReq.msg.To] = slotIndex
		}

		slot := p.slotState[slotIndex]
		txReq.staticSlotIndex = slotIndex // txReq is better to be executed in this slot
		slot.pendingTxReqList = append(slot.pendingTxReqList, txReq)
	}
}

func (p *ParallelStateProcessor) mostHungrySlot() int {
	var (
		workload  = len(p.slotState[0].pendingTxReqList)
		slotIndex = 0
	)
	for i, slot := range p.slotState { // can start from index 1
		if len(slot.pendingTxReqList) < workload {
			slotIndex = i
			workload = len(slot.pendingTxReqList)
		}
		// just return the first slot with 0 workload
		if workload == 0 {
			return slotIndex
		}
	}
	return slotIndex
}

// hasConflict conducts conflict check
func (p *ParallelStateProcessor) hasConflict(txResult *ParallelTxResult, isStage2 bool) bool {
	slotDB := txResult.slotDB
	if txResult.err != nil {
		log.Info("HasConflict due to err", "err", txResult.err)
		return true
	} else if slotDB.NeedsRedo() {
		log.Info("HasConflict needsRedo")
		txResult.err = errors.New("parallel needs redo")
		// if there is any reason that indicates this transaction needs to redo, skip the conflict check
		return true
	} else {
		// check whether the slot db reads during execution are correct.
		if err := slotDB.IsParallelReadsValid(isStage2); err != nil {
			txResult.err = err
			return true
		}
	}
	return false
}

func (p *ParallelStateProcessor) switchSlot(slotIndex int) {
	slot := p.slotState[slotIndex]
	if atomic.CompareAndSwapInt32(&slot.activatedType, parallelPrimarySlot, parallelShadowSlot) {
		// switch from normal to shadow slot
		if len(slot.shadowWakeUpChan) == 0 {
			slot.shadowWakeUpChan <- struct{}{}
		}
	} else if atomic.CompareAndSwapInt32(&slot.activatedType, parallelShadowSlot, parallelPrimarySlot) {
		// switch from shadow to normal slot
		if len(slot.primaryWakeUpChan) == 0 {
			slot.primaryWakeUpChan <- struct{}{}
		}
	}
}

// executeInSlot do tx execution with thread local slot.
func (p *ParallelStateProcessor) executeInSlot(blockContext vm.BlockContext, config *params.ChainConfig, cfg vm.Config, statedb *state.StateDB, txReq *ParallelTxRequest, gp *GasPool, blockNumber *big.Int, blockHash common.Hash) *ParallelTxResult {
	msg := txReq.msg
	tx := txReq.tx
	// Create a new context to be used in the EVM environment.
	txContext := NewEVMTxContext(msg)
	// init slotDB
	slotDB := state.NewUncommittedDB(statedb)
	slotDB.SetTxContext(tx.Hash(), txReq.txIndex)

	evm := vm.NewEVM(blockContext, txContext, statedb, config, cfg)
	evm.Reset(txContext, slotDB)

	nonce := tx.Nonce()
	if msg.IsDepositTx && config.IsOptimismRegolith(evm.Context.Time) {
		nonce = slotDB.GetNonce(msg.From)
	}

	// Apply the transaction to the current state (included in the env).
	// Apply the transaction to the current state (included in the env).
	var (
		result *ExecutionResult
		err    error
	)
	if p.delayGasFee {
		result, err = ApplyMessageDelayGasFee(evm, msg, gp)
	} else {
		result, err = ApplyMessage(evm, msg, gp)
	}

	if err != nil {
		return &ParallelTxResult{
			txReq:  txReq,
			err:    err,
			result: result,
		}
	}

	// Update the state with pending changes.
	var root []byte
	// we don't finalize the stateDB here, we will do it after all txs are executed
	//if config.IsByzantium(blockNumber) {
	//	statedb.Finalise(true)
	//} else {
	//	root = statedb.IntermediateRoot(config.IsEIP158(blockNumber)).Bytes()
	//}

	// Create a new receipt for the transaction, storing the intermediate root and gas used
	// by the tx.
	// @TODO calculate the CumulativeGasUsed in merging
	receipt := &types.Receipt{Type: tx.Type(), PostState: root, CumulativeGasUsed: 0}
	if result.Failed() {
		receipt.Status = types.ReceiptStatusFailed
	} else {
		receipt.Status = types.ReceiptStatusSuccessful
	}
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = result.UsedGas

	if msg.IsDepositTx && config.IsOptimismRegolith(evm.Context.Time) {
		// The actual nonce for deposit transactions is only recorded from Regolith onwards and
		// otherwise must be nil.
		receipt.DepositNonce = &nonce
		// The DepositReceiptVersion for deposit transactions is only recorded from Canyon onwards
		// and otherwise must be nil.
		if config.IsOptimismCanyon(evm.Context.Time) {
			receipt.DepositReceiptVersion = new(uint64)
			*receipt.DepositReceiptVersion = types.CanyonDepositReceiptVersion
		}
	}
	if tx.Type() == types.BlobTxType {
		receipt.BlobGasUsed = uint64(len(tx.BlobHashes()) * params.BlobTxBlobGasPerBlob)
		receipt.BlobGasPrice = evm.Context.BlobBaseFee
	}

	// If the transaction created a contract, store the creation address in the receipt.
	if msg.To == nil {
		receipt.ContractAddress = crypto.CreateAddress(evm.TxContext.Origin, nonce)
	}

	// Set the receipt logs and create the bloom filter.
	receipt.Logs = slotDB.PackLogs(blockNumber.Uint64(), blockHash)
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	receipt.BlockHash = blockHash
	receipt.BlockNumber = blockNumber
	receipt.TransactionIndex = uint(statedb.TxIndex())
	return &ParallelTxResult{
		txReq:      txReq,
		err:        nil,
		receipt:    receipt,
		uncommited: slotDB,
		result:     result,
	}
}

// toConfirmTxIndex confirm a serial TxResults with same txIndex
func (p *ParallelStateProcessor) toConfirmTxIndex(targetTxIndex int, isStage2 bool) *ParallelTxResult {
	if isStage2 {
		if targetTxIndex <= int(p.mergedTxIndex.Load())+1 {
			// `p.mergedTxIndex+1` is the one to be merged,
			// in stage2, we do likely conflict check, for these not their turn.
			return nil
		}
	}

	for {
		// handle a targetTxIndex in a loop
		var targetResult *ParallelTxResult
		if isStage2 {
			result, ok := p.unconfirmedResults.Load(targetTxIndex)
			if !ok {
				return nil
			}
			targetResult = result.(*ParallelTxResult)

			// in stage 2, don't schedule a new redo if the TxReq is:
			//  a.runnable: it will be redone
			//  b.running: the new result will be more reliable, we skip check right now
			if atomic.LoadInt32(&targetResult.txReq.runnable) == 1 {
				return nil
			}
			if targetResult.executedIndex < targetResult.txReq.executedNum.Load() {
				// skip the intermediate result that is not the latest.
				return nil
			}
		} else {
			// pop one result as target result.
			results := p.pendingConfirmResults[targetTxIndex]
			resultsLen := len(results)
			if resultsLen == 0 { // there is no pending result can be verified, break and wait for incoming results
				return nil
			}
			targetResult = results[len(results)-1]
			// last is the freshest, stack based priority
			p.pendingConfirmResults[targetTxIndex] = p.pendingConfirmResults[targetTxIndex][:resultsLen-1] // remove from the queue
		}

		valid := p.toConfirmTxIndexResult(targetResult, isStage2)
		if !valid {
			staticSlotIndex := targetResult.txReq.staticSlotIndex
			conflictBase := targetResult.slotDB.BaseTxIndex()
			conflictIndex := targetResult.txReq.conflictIndex.Load()
			if conflictIndex < int32(conflictBase) {
				if targetResult.txReq.conflictIndex.CompareAndSwap(conflictIndex, int32(conflictBase)) {
					log.Debug("Update conflict index", "conflictIndex", conflictIndex, "conflictBase", conflictBase)
				}
			}
			if isStage2 {
				atomic.CompareAndSwapInt32(&targetResult.txReq.runnable, 0, 1) // needs redo
				p.debugConflictRedoNum++
				// interrupt the slot's current routine, and switch to the other routine
				p.switchSlot(staticSlotIndex)
				return nil
			}

			if len(p.pendingConfirmResults[targetTxIndex]) == 0 { // this is the last result to check, and it is not valid
				// This means that the tx has been executed more than blockTxCount times, so it exits with the error.
				// TODO-dav: p.mergedTxIndex+2 may be more reasonable? - this is buggy for expected exit
				if targetResult.txReq.txIndex == int(p.mergedTxIndex.Load())+1 &&
					targetResult.slotDB.BaseTxIndex() == int(p.mergedTxIndex.Load()) {
					if targetResult.err != nil {
						if false { // TODO: delete the printf
							fmt.Printf("!!!!!!!!!!! Parallel execution exited with error!!!!!, txIndex:%d, err: %v\n", targetResult.txReq.txIndex, targetResult.err)
						}
						return targetResult
					} else {
						// abnormal exit with conflict error, need check the parallel algorithm
						targetResult.err = ErrParallelUnexpectedConflict
						if false {
							fmt.Printf("!!!!!!!!!!! Parallel execution exited unexpected conflict!!!!!, txIndex:%d\n", targetResult.txReq.txIndex)
						}
						return targetResult
					}
					//}
				}
				atomic.CompareAndSwapInt32(&targetResult.txReq.runnable, 0, 1) // needs redo
				p.debugConflictRedoNum++
				// interrupt its current routine, and switch to the other routine
				p.switchSlot(staticSlotIndex)
				return nil
			}
			continue
		}
		if isStage2 {
			// likely valid, but not sure, can not deliver
			return nil
		}
		return targetResult
	}
}

// to confirm one txResult, return true if the result is valid
// if it is in Stage 2 it is a likely result, not 100% sure
func (p *ParallelStateProcessor) toConfirmTxIndexResult(txResult *ParallelTxResult, isStage2 bool) bool {
	txReq := txResult.txReq
	if p.hasConflict(txResult, isStage2) {
		log.Debug(fmt.Sprintf("HasConflict!! block: %d, txIndex: %d\n", txResult.txReq.block.NumberU64(), txResult.txReq.txIndex))
		return false
	}
	if isStage2 { // not its turn
		return true // likely valid, not sure, not finalized right now.
	}

	// goroutine unsafe operation will be handled from here for safety
	gasConsumed := txReq.gasLimit - txResult.gpSlot.Gas()
	if gasConsumed != txResult.result.UsedGas {
		log.Error("gasConsumed != result.UsedGas mismatch",
			"gasConsumed", gasConsumed, "result.UsedGas", txResult.result.UsedGas)
	}

	// ok, time to do finalize, stage2 should not be parallel
	txResult.receipt, txResult.err = applyTransactionStageFinalization(txResult.evm, txResult.result,
		*txReq.msg, p.config, txResult.slotDB, txReq.block,
		txReq.tx, &txReq.usedGas, txResult.originalNonce)
	return true
}

func (p *ParallelStateProcessor) runSlotLoop(slotIndex int, slotType int32) {
}

func (p *ParallelStateProcessor) runQuickMergeSlotLoop(slotIndex int, slotType int32) {
}

func (p *ParallelStateProcessor) runConfirmStage2Loop() {
}

// wait until the next Tx is executed and its result is merged to the main stateDB
func (p *ParallelStateProcessor) confirmTxResults(result *ParallelTxResult, statedb *state.StateDB, gp *GasPool, gasUsed *uint64) *ParallelTxResult {
	if result == nil {
		return nil
	}
	if result.err != nil {
		return result
	}
	// we must check the gas limit before merge
	if err := gp.SubGas(result.receipt.GasUsed); err != nil {
		log.Error("gas limit reached", "block", result.txReq.block.Number(),
			"txIndex", result.txReq.txIndex, "GasUsed", result.receipt.GasUsed, "gp.Gas", gp.Gas())
	}
	// merge result into maindb
	if err := result.uncommited.Merge(); err != nil {
		result.err = err
		return result
	}
	// calculate the gasUsed
	*gasUsed = *gasUsed + result.receipt.GasUsed
	result.receipt.CumulativeGasUsed = *gasUsed

	var root []byte
	header := result.txReq.block.Header()

	isByzantium := p.config.IsByzantium(header.Number)
	isEIP158 := p.config.IsEIP158(header.Number)

	delayGasFee := result.result.delayFees
	// add delayed gas fee
	if delayGasFee != nil {
		if delayGasFee.TipFee != nil {
			statedb.AddBalance(delayGasFee.Coinbase, delayGasFee.TipFee)
		}
		if delayGasFee.BaseFee != nil {
			statedb.AddBalance(params.OptimismBaseFeeRecipient, delayGasFee.BaseFee)
		}
		if delayGasFee.L1Fee != nil {
			statedb.AddBalance(params.OptimismL1FeeRecipient, delayGasFee.L1Fee)
		}
	}

	// Do IntermediateRoot after mergeSlotDB.
	if isByzantium {
		statedb.Finalise(true)
	} else {
		root = statedb.IntermediateRoot(isEIP158).Bytes()
	}
	result.receipt.PostState = root

	return result
}

func (p *ParallelStateProcessor) doCleanUp() {
	// 1.clean up all slot: primary and shadow, to make sure they are stopped
	for _, slot := range p.slotState {
		slot.primaryStopChan <- struct{}{}
		slot.shadowStopChan <- struct{}{}
		<-p.stopSlotChan
		<-p.stopSlotChan
	}
	// 2.discard delayed txResults if any
	for {
		if len(p.txResultChan) > 0 {
			<-p.txResultChan
			continue
		}
		break
	}
	// 3.make sure the confirmation routine is stopped
	p.stopConfirmStage2Chan <- struct{}{}
	<-p.stopSlotChan
}

// Process implements BEP-130 Parallel Transaction Execution
func (p *ParallelStateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config) (types.Receipts, []*types.Log, uint64, error) {
	var (
		receipts types.Receipts
		usedGas  = new(uint64)
		header   = block.Header()
		gp       = new(GasPool).AddGas(block.GasLimit())
	)

	// Mutate the block and state according to any hard-fork specs
	if p.config.DAOForkSupport && p.config.DAOForkBlock != nil && p.config.DAOForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyDAOHardFork(statedb)
	}
	if p.config.PreContractForkBlock != nil && p.config.PreContractForkBlock.Cmp(block.Number()) == 0 {
		misc.ApplyPreContractHardFork(statedb)
	}

	misc.EnsureCreate2Deployer(p.config, block.Time(), statedb)

	allTxs := block.Transactions()
	p.resetState(len(allTxs), statedb)

	var (
		// with parallel mode, vmenv will be created inside of slot
		blockContext = NewEVMBlockContext(block.Header(), p.bc, nil, p.config, statedb)
		vmenv        = vm.NewEVM(blockContext, vm.TxContext{}, statedb, p.config, cfg)
		signer       = types.MakeSigner(p.bc.chainConfig, block.Number(), block.Time())
	)

	if beaconRoot := block.BeaconRoot(); beaconRoot != nil {
		ProcessBeaconBlockRoot(*beaconRoot, vmenv, statedb)
	}
	statedb.MarkFullProcessed()

	var (
		txDAG types.TxDAG
	)
	if p.bc.enableTxDAG {
		var err error
		if p.bc.txDAGReader != nil {
			// load cache txDAG from file first
			txDAG = p.bc.txDAGReader.TxDAG(block.NumberU64())

		} else {
			// load TxDAG from block
			txDAG, err = types.GetTxDAG(block)
			if err != nil {
				log.Debug("pevm decode txdag failed", "block", block.NumberU64(), "err", err)
			}
		}
		if err := types.ValidateTxDAG(txDAG, len(block.Transactions())); err != nil {
			log.Warn("pevm cannot apply wrong txdag",
				"block", block.NumberU64(), "txs", len(block.Transactions()), "err", err)
			txDAG = nil
		}
	}

	txNum, allTxsReq := len(allTxs), make([]*ParallelTxRequest, len(allTxs))
	// Iterate over and process the individual transactions
	commonTxs := make([]*types.Transaction, 0, txNum)
	// var txReqs []*ParallelTxRequest
	for i, tx := range allTxs {
		// parallel start, wrap an exec message, which will be dispatched to a slot
		txReq := &ParallelTxRequest{
			txIndex: i,
			tx:      tx,
			block:   block,
		}
		p.allTxReqs = append(p.allTxReqs, txReq)
		allTxsReq[i] = txReq
	}

	p.delayGasFee = true
	// only disable delayGasFee when txDAG tells us to do so
	if txDAG != nil && !txDAG.DelayGasFeeDistribution() {
		p.delayGasFee = false
	}

	// if txDAG == nil, we treat all txs as no-dependency ones
	runtimeDag := txDAG
	if runtimeDag == nil {
		runtimeDag = &types.PlainTxDAG{}
		runtimeDag.SetTxDep(0, types.TxDep{TxIndexes: nil, Flags: &types.ExcludedTxFlag})
	}
	txLevels := NewTxLevels(allTxsReq, runtimeDag)
	starttime := time.Now()
	var executeFailed, confirmedFailed int32 = 0, 0

	// wait until all Txs have processed.
	err, txIndex := txLevels.Run(func(ptr *ParallelTxRequest) *ParallelTxResult {
		// can be moved it into slot for efficiency, but signer is not concurrent safe
		// Parallel Execution 1.0&2.0 is for full sync mode, Nonce PreCheck is not necessary
		// And since we will do out-of-order execution, the Nonce PreCheck could fail.
		// We will disable it and leave it to Parallel 3.0 which is for validator mode
		msg, err := TransactionToMessage(ptr.tx, signer, header.BaseFee)
		if err != nil {
			return &ParallelTxResult{
				txReq: ptr,
				err:   err,
			}
		}
		ptr.msg = msg
		var blockContext vm.BlockContext = blockContext
		var config *params.ChainConfig = p.config
		var cfg vm.Config = cfg
		var statedb *state.StateDB = statedb
		var txReq *ParallelTxRequest = ptr
		//@TODO: testcase 1: tx1 consume too much gas, tx2 has not enough gas to execute
		var gp *GasPool = new(GasPool).AddGas(gp.Gas())
		var blockNumber *big.Int = block.Number()
		var blockHash common.Hash
		res := p.executeInSlot(blockContext, config, cfg, statedb, txReq, gp, blockNumber, blockHash)
		if res.err != nil {
			log.Debug("ProcessParallel execute tx failed", "block", header.Number, "txIndex", ptr.txIndex, "err", res.err)
			atomic.AddInt32(&executeFailed, 1)
			atomic.AddInt32(&p.debugConflictRedoNum, 1)
		}
		return res
	},
		func(ptr *ParallelTxResult) error {
			result := p.confirmTxResults(ptr, statedb, gp, usedGas)
			if result == nil {
				atomic.AddInt32(&p.debugConflictRedoNum, 1)
				// it should never happen
				log.Error("ProcessParallel confirm tx failed, result == nil", "block", header.Number)
				atomic.AddInt32(&confirmedFailed, 1)
				return fmt.Errorf("nil result")
			}
			if result.err != nil {
				atomic.AddInt32(&p.debugConflictRedoNum, 1)
				atomic.AddInt32(&confirmedFailed, 1)
				log.Debug("ProcessParallel confirm tx failed", ",block=", header.Number, ",txIndex=", result.txReq.txIndex, ",err=", result.err)
				return fmt.Errorf("confirmed failed, txIndex:%d, err:%s", result.txReq.txIndex, result.err)
			}
			commonTxs = append(commonTxs, result.txReq.tx)
			receipts = append(receipts, result.receipt)
			return nil
		})
	log.Info("ProcessParallel execute block done", "usedGas", *usedGas, "parallel", cap(runner), "block", header.Number, "levels", len(txLevels), "txs", len(allTxsReq), "duration", time.Since(starttime), "executeFailed", executeFailed, "confirmFailed", confirmedFailed, "txDAG", txDAG != nil)
	if err != nil {
		log.Error("ProcessParallel execution failed", "block", header.Number, "usedGas", *usedGas,
			"txIndex", txIndex,
			"err", err,
			"txNum", txNum,
			"len(commonTxs)", len(commonTxs),
			"conflictNum", p.debugConflictRedoNum,
			"gasLimit", header.GasLimit,
			"txDAG", txDAG != nil)
		return nil, nil, 0, fmt.Errorf("could not apply tx %d [%v]: %w", txIndex, allTxs[txIndex].Hash().Hex(), err)
	}

	// clean up when the block is processed
	//p.doCleanUp()

	// len(commonTxs) could be 0, such as: https://bscscan.com/block/14580486
	if len(commonTxs) > 0 && p.debugConflictRedoNum > 0 {
		log.Info("ProcessParallel tx all done", "block", header.Number, "usedGas", *usedGas,
			"txNum", txNum,
			"len(commonTxs)", len(commonTxs),
			"conflictNum", p.debugConflictRedoNum,
			"redoRate(%)", 100*(int(p.debugConflictRedoNum))/len(commonTxs),
			"txDAG", txDAG != nil)
	}
	if metrics.EnabledExpensive {
		parallelTxNumMeter.Mark(int64(len(commonTxs)))
		parallelConflictTxNumMeter.Mark(int64(p.debugConflictRedoNum))
	}

	// Fail if Shanghai not enabled and len(withdrawals) is non-zero.
	withdrawals := block.Withdrawals()
	if len(withdrawals) > 0 && !p.config.IsShanghai(block.Number(), block.Time()) {
		return nil, nil, 0, errors.New("withdrawals before shanghai")
	}
	// Finalize the block, applying any consensus engine specific extras (e.g. block rewards)
	p.engine.Finalize(p.bc, header, statedb, commonTxs, block.Uncles(), withdrawals)

	var allLogs []*types.Log
	for _, receipt := range receipts {
		allLogs = append(allLogs, receipt.Logs...)
	}
	return receipts, allLogs, *usedGas, nil
}

func applyTransactionStageExecution(msg *Message, gp *GasPool, statedb *state.ParallelStateDB, evm *vm.EVM, delayGasFee bool) (*vm.EVM, *ExecutionResult, error) {
	// Create a new context to be used in the EVM environment.
	txContext := NewEVMTxContext(msg)
	evm.Reset(txContext, statedb)

	// Apply the transaction to the current state (included in the env).
	var (
		result *ExecutionResult
		err    error
	)
	if delayGasFee {
		result, err = ApplyMessageDelayGasFee(evm, msg, gp)
	} else {
		result, err = ApplyMessage(evm, msg, gp)
	}

	if err != nil {
		return nil, nil, err
	}

	return evm, result, err
}

func applyTransactionStageFinalization(evm *vm.EVM, result *ExecutionResult, msg Message, config *params.ChainConfig, statedb *state.ParallelStateDB, block *types.Block, tx *types.Transaction, usedGas *uint64, nonce *uint64) (*types.Receipt, error) {

	*usedGas += result.UsedGas
	// Create a new receipt for the transaction, storing the intermediate root and gas used by the tx.
	receipt := &types.Receipt{Type: tx.Type(), PostState: nil, CumulativeGasUsed: *usedGas}
	if result.Failed() {
		receipt.Status = types.ReceiptStatusFailed
	} else {
		receipt.Status = types.ReceiptStatusSuccessful
	}
	receipt.TxHash = tx.Hash()
	receipt.GasUsed = result.UsedGas

	if msg.IsDepositTx && config.IsOptimismRegolith(evm.Context.Time) {
		// The actual nonce for deposit transactions is only recorded from Regolith onwards and
		// otherwise must be nil.
		receipt.DepositNonce = nonce
		// The DepositReceiptVersion for deposit transactions is only recorded from Canyon onwards
		// and otherwise must be nil.
		if config.IsOptimismCanyon(evm.Context.Time) {
			receipt.DepositReceiptVersion = new(uint64)
			*receipt.DepositReceiptVersion = types.CanyonDepositReceiptVersion
		}
	}
	if tx.Type() == types.BlobTxType {
		receipt.BlobGasUsed = uint64(len(tx.BlobHashes()) * params.BlobTxBlobGasPerBlob)
		receipt.BlobGasPrice = evm.Context.BlobBaseFee
	}
	// If the transaction created a contract, store the creation address in the receipt.
	if msg.To == nil {
		receipt.ContractAddress = crypto.CreateAddress(evm.TxContext.Origin, *nonce)
	}
	// Set the receipt logs and create the bloom filter.
	receipt.Logs = statedb.GetLogs(tx.Hash(), block.NumberU64(), block.Hash())
	receipt.Bloom = types.CreateBloom(types.Receipts{receipt})
	receipt.BlockHash = block.Hash()
	receipt.BlockNumber = block.Number()
	receipt.TransactionIndex = uint(statedb.TxIndex())
	return receipt, nil
}
