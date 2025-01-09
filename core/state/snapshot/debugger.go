package snapshot

import (
	"sync/atomic"
	"time"
)

type Debugger struct {
	count   int32
	latency int64
}

func (d *Debugger) Report() (int32, int64) {
	cnt := atomic.SwapInt32(&d.count, 0)
	dur := atomic.SwapInt64(&d.latency, 0)
	return cnt, dur
}

func (d *Debugger) Mark(latency time.Duration, debug bool) {
	if !debug {
		return
	}
	atomic.AddInt32(&d.count, 1)
	atomic.AddInt64(&d.latency, int64(latency))
}
