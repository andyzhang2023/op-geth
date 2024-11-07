package core

import (
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/core/types"
)

type PEVMJob struct {
	res      *PEVMTxResult
	tx       *PEVMTxRequest
	dag      types.TxDAG
	running  int32
	executed bool
	merged   bool
}

type PEVMScheduler struct {
	all []*PEVMJob
}

func newPEVMScheduler(allTx []*PEVMTxRequest) *PEVMScheduler {
	all := make([]*PEVMJob, len(allTx))
	for i, tx := range allTx {
		all[i] = &PEVMJob{tx: tx}
	}
	return &PEVMScheduler{all: all}
}

func (ps *PEVMScheduler) Run(execute func(*PEVMTxRequest) *PEVMTxResult, confirm func(*PEVMTxResult) error) (failed error, failedTxIndex int) {
	var merged int32 = -1
	var finished int32 = 0
	var allNum = int32(len(ps.all))
	var mergeLock int32 = 0
	var merge = func() (int, error) {
		// lock the merge
		if !atomic.CompareAndSwapInt32(&mergeLock, 0, 1) {
			return 0, nil
		}
		defer atomic.CompareAndSwapInt32(&mergeLock, 1, 0)
		for merged < allNum-1 {
			i := merged + 1
			job := ps.all[i]
			if !job.executed {
				return 0, nil
			}
			// skip the merged job
			if job.merged {
				continue
			}
			// do the merge
			if err := confirm(job.res); err != nil {
				// maybe conflict, rerun the job
				if err := confirm(execute(job.tx)); err != nil {
					return int(i), err
				}
			}
			atomic.AddInt32(&merged, 1)
			atomic.AddInt32(&finished, 1)
		}
		return 0, nil
	}
	// run all transactions in parallel
	var parallel int = 8
	var execErrorCount int32 = 0
	var wait = sync.WaitGroup{}
	wait.Add(parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			defer wait.Done()
			// it ends when the last tx is merged
			for finished < int32(len(ps.all)) && execErrorCount < int32(parallel)*3 && failed == nil {
				for jIdx := merged + 1; jIdx < allNum; jIdx++ {
					job := ps.all[jIdx]
					if job.executed {
						continue
					}
					// all dependiences is merged
					deps, execluded := job.dependencies()
					// the execluted one
					if execluded && merged < jIdx-1 {
						// should wait all txs[:jIdx-1] to be merged
						continue
					} else if len(deps) != 0 && !ps.allMerged(deps) {
						// should wait all dependiences to be merged
						continue
					}

					if job.lock() {
						// execute the job
						result := execute(job.tx)
						if result.err != nil {
							// if the result is not confirmed, retry the job
							atomic.AddInt32(&execErrorCount, 1)
							job.unlock()
							continue
						}
						job.executed = true
						job.res = result
						job.unlock()
						if txIndex, err := merge(); err != nil {
							// set the failed error, to informs other goroutines to stop
							failedTxIndex, failed = txIndex, err
							return
						}
					}
				}

			}
		}()
	}
	wait.Wait()
	return
}

func (ps *PEVMScheduler) allMerged(txs []int) bool {
	for _, i := range txs {
		if !ps.all[i].merged {
			return false
		}
	}
	return true
}

func (pj *PEVMJob) lock() bool {
	return atomic.CompareAndSwapInt32(&pj.running, 0, 1)
}

func (pj *PEVMJob) unlock() {
	atomic.CompareAndSwapInt32(&pj.running, 1, 0)
}

func (pj *PEVMJob) dependencies() (txs []int, all bool) {
	//return all dependencies of this job
	return nil, false
}
