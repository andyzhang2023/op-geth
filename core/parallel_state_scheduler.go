package core

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

var runner chan func()

func init() {
	runner = make(chan func(), runtime.NumCPU())
	//for i := 0; i < runtime.NumCPU(); i++ {
	//	go func() {
	//		for f := range runner {
	//			f()
	//		}
	//	}()
	//}
}

func ParallelNum() int {
	return cap(runner)
}

// TxLevel contains all transactions who are independent to each other
type TxLevel []*ParallelTxRequest

func (tl TxLevel) SplitBy(chunkSize int) []TxLevel {
	if len(tl) == 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = 1
	}
	result := make([]TxLevel, 0, len(tl)/chunkSize+1)
	for i := 0; i < len(tl); i += chunkSize {
		end := i + chunkSize
		if end > len(tl) {
			end = len(tl)
		}
		result = append(result, tl[i:end])
	}
	return result
}

func (tl TxLevel) Split(chunks int) []TxLevel {
	if len(tl) == 0 {
		return nil
	}
	if chunks <= 0 {
		chunks = 1
	}
	result := make([]TxLevel, 0, chunks)
	var chunkSize int
	if len(tl)%chunks == 0 {
		chunkSize = len(tl) / chunks
	} else {
		chunkSize = len(tl)/chunks + 1
	}
	for i := 0; i < chunks; i++ {
		start := i * chunkSize
		end := min(start+chunkSize, len(tl))
		if start > len(tl)-1 || start >= end {
			break
		}
		result = append(result, tl[start:end])
	}
	return result
}

// TxLevels indicates the levels of transactions
// the levels are ordered by the dependencies, and generated by the TxDAG
type TxLevels []TxLevel

type confirmQueue struct {
	queue     []confirmation
	confirmed int // need to be set to -1 originally
}

type confirmation struct {
	result    *ParallelTxResult
	executed  error // error from execution in parallel
	confirmed error // error from confirmation in sequence (like conflict)
}

// put into the right position (txIndex)
func (cq *confirmQueue) collect(result *ParallelTxResult) error {
	if result.txReq.txIndex >= len(cq.queue) {
		// TODO add metrics
		return fmt.Errorf("txIndex outof range, req.index:%d, len(queue):%d", result.txReq.txIndex, len(cq.queue))
	}
	i := result.txReq.txIndex
	cq.queue[i].result, cq.queue[i].executed, cq.queue[i].confirmed = result, result.err, nil
	return nil
}

func (cq *confirmQueue) confirmWithTrust(level TxLevel, execute func(*ParallelTxRequest) *ParallelTxResult, confirm func(*ParallelTxResult) error) (error, int) {
	// find all able-to-confirm transactions, and try to confirm them
	for _, tx := range level {
		i := tx.txIndex
		toConfirm := cq.queue[i]
		// the tx has not been executed yet, which means the higher-index transactions can not be confirmed before it
		// so stop the loop.
		if toConfirm.result == nil {
			break
		}
		switch true {
		case toConfirm.executed != nil:
			if err := cq.rerun(i, execute, confirm); err != nil {
				// TODO add logs for err
				// rerun failed, something very wrong.
				return err, toConfirm.result.txReq.txIndex
			}

		default:
			//try the first confirm
			if err := confirm(toConfirm.result); err != nil {
				// TODO add logs for err
				if err = cq.rerun(i, execute, confirm); err != nil {
					// TODO add logs for err
					// rerun failed, something very wrong.
					return err, toConfirm.result.txReq.txIndex
				}
			}
		}
		cq.confirmed = i
	}
	return nil, 0
}

// try to confirm txs as much as possible, they will be confirmed in a sequencial order.
func (cq *confirmQueue) confirm(execute func(*ParallelTxRequest) *ParallelTxResult, confirm func(*ParallelTxResult) error) (error, int) {
	// find all able-to-confirm transactions, and try to confirm them
	for i := cq.confirmed + 1; i < len(cq.queue); i++ {
		toConfirm := cq.queue[i]
		// the tx has not been executed yet, which means the higher-index transactions can not be confirmed before it
		// so stop the loop.
		if toConfirm.result == nil {
			break
		}
		switch true {
		case toConfirm.executed != nil:
			if err := cq.rerun(i, execute, confirm); err != nil {
				// TODO add logs for err
				// rerun failed, something very wrong.
				return err, toConfirm.result.txReq.txIndex
			}

		default:
			//try the first confirm
			if err := confirm(toConfirm.result); err != nil {
				// TODO add logs for err
				if err = cq.rerun(i, execute, confirm); err != nil {
					// TODO add logs for err
					// rerun failed, something very wrong.
					return err, toConfirm.result.txReq.txIndex
				}
			}
		}
		cq.confirmed = i
	}
	return nil, 0
}

// rerun executes the transaction of index 'i', and confirms it.
func (cq *confirmQueue) rerun(i int, execute func(*ParallelTxRequest) *ParallelTxResult, confirm func(*ParallelTxResult) error) error {
	//reset the result
	cq.queue[i].result.err, cq.queue[i].executed, cq.queue[i].confirmed = nil, nil, nil
	// failed, rerun and reconfirm, the rerun should alway success.
	rerun := execute(cq.queue[i].result.txReq)
	if rerun.err != nil {
		// TODO add metrics, add error logs.
		return rerun.err
	}
	cq.queue[i].result, cq.queue[i].executed, cq.queue[i].confirmed = rerun, nil, confirm(rerun)
	if cq.queue[i].confirmed != nil {
		// TODO add metrics, add error logs.
		return cq.queue[i].confirmed
	}
	return nil
}

// run runs the transactions in parallel
// execute must return a non-nil result, otherwise it panics.
func (tls TxLevels) Run(execute func(*ParallelTxRequest) *ParallelTxResult, confirm func(*ParallelTxResult) error) (error, int) {
	toConfirm := &confirmQueue{
		queue:     make([]confirmation, tls.txCount()),
		confirmed: -1,
	}

	trustDAG := false

	// execute all transactions in parallel
	for _, txLevel := range tls {
		wait := sync.WaitGroup{}
		//wait.Add(len(txLevel))
		//trunks := txLevel.Split(runtime.NumCPU())
		trunks := TxLevels{txLevel}
		wait.Add(len(trunks))
		//fmt.Printf("total:%d, trunks:%d, parallelNum:%d\n", len(txLevel), len(trunks), ParallelNum())
		// split tx into chunks, to save the cost of channel communication
		for _, txs := range trunks {
			// execute the transactions in parallel
			temp := txs
			go func() {
				for _, tx := range temp {
					res := execute(tx)
					toConfirm.collect(res)
				}
				wait.Done()
			}()
		}
		wait.Wait()
		// all transactions of current level are executed, now try to confirm.
		if trustDAG {
			if err, txIndex := toConfirm.confirmWithTrust(txLevel, execute, confirm); err != nil {
				// something very wrong, stop the process
				return err, txIndex
			}
		} else {
			if err, txIndex := toConfirm.confirm(execute, confirm); err != nil {
				// something very wrong, stop the process
				return err, txIndex
			}
		}
	}
	return nil, 0
}

func (tls TxLevels) txCount() int {
	count := 0
	for _, txlevel := range tls {
		count += len(txlevel)
	}
	return count
}

// predictTxDAG predicts the TxDAG by their from address and to address, and generates the levels of transactions
func (tl TxLevel) predictTxDAG(dag types.TxDAG) {
	marked := make(map[common.Address]int, len(tl))
	for _, tx := range tl {
		var deps []uint64
		var tfrom, tto = -1, -1
		if ti, ok := marked[tx.msg.From]; ok {
			tfrom = ti
		}
		if ti, ok := marked[*tx.msg.To]; ok {
			tto = ti
		}
		if tfrom >= 0 && tto >= 0 && tfrom > tto {
			// keep deps ordered by the txIndex
			tfrom, tto = tto, tfrom
		}
		if tfrom >= 0 {
			deps = append(deps, uint64(tfrom))
		}
		if tto >= 0 {
			deps = append(deps, uint64(tto))
		}
		dag.SetTxDep(tx.txIndex, types.TxDep{TxIndexes: deps})
		marked[tx.msg.From] = tx.txIndex
		marked[*tx.msg.To] = tx.txIndex
	}
}

func NewTxLevels2(all []*ParallelTxRequest, dag types.TxDAG) TxLevels {
	var levels TxLevels = make(TxLevels, 0, 8)
	var currLevel int = 0

	var enlargeLevelsIfNeeded = func(currLevel int, levels *TxLevels) {
		if len(*levels) <= currLevel {
			for i := len(*levels); i <= currLevel; i++ {
				*levels = append(*levels, TxLevel{})
			}
		}
	}

	if len(all) == 0 {
		return nil
	}
	if dag == nil {
		return TxLevels{all}
	}

	marked := make(map[int]int, len(all))
	for _, tx := range all {
		dep := dag.TxDep(tx.txIndex)
		switch true {
		case dep != nil && dep.CheckFlag(types.ExcludedTxFlag),
			dep != nil && dep.CheckFlag(types.NonDependentRelFlag):
			// excluted tx, occupies the whole level
			// or dependent-to-all tx, occupies the whole level, too
			levels = append(levels, TxLevel{tx})
			marked[tx.txIndex], currLevel = len(levels)-1, len(levels)

		case dep == nil || len(dep.TxIndexes) == 0:
			// dependent on none
			enlargeLevelsIfNeeded(currLevel, &levels)
			levels[currLevel] = append(levels[currLevel], tx)
			marked[tx.txIndex] = currLevel

		case dep != nil && len(dep.TxIndexes) > 0:
			// dependent on others
			// findout the correct level that the tx should be put
			prevLevel := -1
			for _, txIndex := range dep.TxIndexes {
				if pl, ok := marked[int(txIndex)]; ok && pl > prevLevel {
					prevLevel = pl
				}
			}
			if prevLevel < 0 {
				// broken DAG, just ignored it
				enlargeLevelsIfNeeded(currLevel, &levels)
				levels[currLevel] = append(levels[currLevel], tx)
				marked[tx.txIndex] = currLevel
				continue
			}
			enlargeLevelsIfNeeded(prevLevel+1, &levels)
			levels[prevLevel+1] = append(levels[prevLevel+1], tx)
			// record the level of this tx
			marked[tx.txIndex] = prevLevel + 1

		default:
			panic("unexpected case")
		}
	}
	return levels
}

// NewTxLevels generates the levels of transactions
// all is the transactions to be executed, who are ordered by the txIndex
func NewTxLevels(all []*ParallelTxRequest, dag types.TxDAG) TxLevels {
	defaultLevels := func(all []*ParallelTxRequest) TxLevels {
		if len(all) == 0 {
			return nil
		}
		return TxLevels{all}
	}
	if dag == nil {
		return defaultLevels(all)
	}

	collected := make(map[int]bool, len(all))
	execlutedLevels := make([]TxLevel, 0, 1)
	appendExecuted := func(tx *ParallelTxRequest) {
		if collected[tx.txIndex] {
			return
		}
		// it occupies the whole level
		execlutedLevels = append(execlutedLevels, TxLevel{tx})
		collected[tx.txIndex] = true
	}

	levels := make([]TxLevel, 0, 8)
	appendNormal := func(tx *ParallelTxRequest, level int) {
		if collected[tx.txIndex] {
			return
		}
		if len(levels) <= level {
			for i := len(levels); i <= level; i++ {
				//@TODO risk of OOM, need to be optimized
				levels = append(levels, make(TxLevel, 0, len(all)/2))
			}
		}
		levels[level] = append(levels[level], tx)
		collected[tx.txIndex] = true
	}

	nodependies := make(TxLevel, 0, 8)
	appendNodependies := func(tx *ParallelTxRequest) {
		if collected[tx.txIndex] {
			return
		}
		nodependies = append(nodependies, tx)
		collected[tx.txIndex] = true
	}

	dep2all := make([]TxLevel, 0, 8)
	appendDepAll := func(tx *ParallelTxRequest) {
		if collected[tx.txIndex] {
			return
		}
		dep2all = append(dep2all, TxLevel{tx})
		collected[tx.txIndex] = true
	}

	var putCurrentLevel func(tx *ParallelTxRequest, level int) error

	putCurrentLevel = func(tx *ParallelTxRequest, level int) error {
		if collected[tx.txIndex] {
			return nil
		}
		var dep *types.TxDep
		if dag.TxCount() > tx.txIndex {
			dep = dag.TxDep(tx.txIndex)
		}
		if dep == nil {
			// treated as no dependency
			appendNormal(tx, level)
			return nil
		}

		switch true {
		case dep.CheckFlag(types.ExcludedTxFlag):
			//occupies the whole level
			appendExecuted(tx)

		case dep.CheckFlag(types.NonDependentRelFlag):
			// append to the dep2all levels
			appendDepAll(tx)

		default:
			if len(dep.TxIndexes) != 0 {
				appendNormal(tx, level)
				// we expect the dependencies ordered by the txIndex, like: [1,2,4]
				// so we handle the dependencies in a reverse order, in an order like:
				//		putCurrentLevel(4), putCurrentLevel(2), putCurrentLevel(1)
				for i := len(dep.TxIndexes) - 1; i >= 0; i-- {
					txIndex := dep.TxIndexes[i]
					if int(txIndex) >= len(all) || all[txIndex] == nil {
						// TODO add logs
						// broken DAG, just ignored it
						return fmt.Errorf("broken DAG, txIndex:%d, len(all):%d", txIndex, len(all))
					}
					putCurrentLevel(all[txIndex], level+1)
				}
			} else {
				// no dependencies
				appendNodependies(tx)
			}
		}
		return nil
	}

	for i := len(all) - 1; i >= 0; i-- {
		tx := all[i]
		if tx == nil {
			continue
		}
		if err := putCurrentLevel(tx, 0); err != nil {
			// TODO add logs
			// something very wrong, just return the default levels
			return defaultLevels(all)
		}
	}

	// merge the normal levels and executed levels
	// 1. run the executed levels first
	// 2. then the "nodepnencies" levels
	// 3. then the normal levels(who have dependencies)
	// 4. then the dep2all levels(who depend on all)
	final := make(TxLevels, 0, len(levels)+len(execlutedLevels)+len(dep2all))
	for i := len(execlutedLevels) - 1; i >= 0; i-- {
		final = append(final, execlutedLevels[i])
	}
	if len(nodependies) > 0 {
		final = append(final, nodependies)
	}
	for i := len(levels) - 1; i >= 0; i-- {
		final = append(final, levels[i])
	}
	for i := len(dep2all) - 1; i >= 0; i-- {
		final = append(final, dep2all[i])
	}
	return final
}
