package core

import (
	"fmt"
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
	var allNum = int32(len(ps.all))
	var mergeLock sync.Mutex
	var merge = func() {
		// lock the merge
		mergeLock.Lock()
		defer mergeLock.Unlock()
		for merged < allNum-1 {
			i := merged + 1
			job := ps.all[i]
			executed := job.executed
			if !executed {
				return
			}
			// skip the merged job
			if job.merged {
				continue
			}
			res := job.res
			if res == nil {
				fmt.Printf("------------- GOD Damit -------------\n")
				fmt.Printf("res:%+v\n", res)
				fmt.Printf("job.res:%+v\n", job.res)
				fmt.Printf("job.tx:%+v\n", job.tx)
				fmt.Printf("job.dag:%v\n", job.dag)
				fmt.Printf("job.running:%v\n", job.running)
				fmt.Printf("job.executed:%v\n", job.executed)
				fmt.Printf("executed:%v\n", executed)
				fmt.Printf("job.merged:%v\n", job.merged)
				panic("here")
			}
			if err := confirm(job.res); err != nil {
				// try to rerun the job
				if err := confirm(execute(job.tx)); err != nil {
					// @TODO panic here
					panic("failed to confirm the job")
				}
			}
			job.merged = true
			merged = i
		}
	}
	// run all transactions in parallel
	var parallel int = 8
	var wait = sync.WaitGroup{}
	wait.Add(parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			defer wait.Done()
			// it ends when the last tx is merged
			for merged < int32(len(ps.all))-1 && failed == nil {
				for jIdx := merged + 1; jIdx < allNum; jIdx++ {
					job := ps.all[jIdx]
					job.execute(execute)
					merge()
				}
			}
		}()
	}
	wait.Wait()
	return
}

func allMerged(all []*PEVMJob, txs []int) bool {
	for _, i := range txs {
		if !all[i].merged {
			return false
		}
	}
	return true
}

func (job *PEVMJob) lock() bool {
	return atomic.CompareAndSwapInt32(&job.running, 0, 1)
}

func (job *PEVMJob) unlock() {
	atomic.CompareAndSwapInt32(&job.running, 1, 0)
}

func (job *PEVMJob) dependencies() (txs []int, all bool) {
	//return all dependencies of this job
	return nil, false
}

func (job *PEVMJob) readyToExecute(all []*PEVMJob) bool {
	// all dependiences is merged
	deps, execluded := job.dependencies()
	// the execluted one
	if execluded {
		// @TODO
		// should wait all txs[:jIdx-1] to be merged
		return false
	} else if len(deps) != 0 && !allMerged(all, deps) {
		// should wait all dependiences to be merged
		return false
	}
	return true
}

func (job *PEVMJob) execute(execute func(*PEVMTxRequest) *PEVMTxResult) bool {
	if !job.lock() {
		return false
	}
	defer job.unlock()
	if !job.executed && job.readyToExecute(nil) {
		// execute the job
		job.res = execute(job.tx)
		job.executed = true
	}
	return true
}
