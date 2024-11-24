package legacypool

import (
	"sync/atomic"
	"time"
)

// throughput is a struct that holds the throughput metrics of the transaction pool.
// it is used at all key points to measure the throughput of the whole path where a transaction comes into the pool.
type throughput struct {
	lastReset time.Time
	cost      int64 // cost in nanoseconds, need to be handled with atomic, so let's use int64
	counter   int64
}

func (t *throughput) mark(duration time.Duration, count int) {
	atomic.AddInt64(&t.cost, int64(duration))
	atomic.AddInt64(&t.counter, int64(count))
}

// avgAndReset returns the average nanoseconds of a transaction that it takes to go through the path.
// it's not accurate, but it's good enough to give a rough idea of the throughput.
// metrics data will be reset after this call.
func (t *throughput) avgAndRest(now time.Time) (avgCost time.Duration, duration time.Duration, totalCost time.Duration, count int64, tps int64) {
	totalCostI64 := atomic.LoadInt64(&t.cost)
	if t.lastReset.IsZero() {
		duration = time.Duration(0)
	} else {
		duration = now.Sub(t.lastReset)
	}
	count = atomic.LoadInt64(&t.counter)
	totalCost = time.Duration(totalCostI64)

	atomic.StoreInt64(&t.cost, 0)
	atomic.StoreInt64(&t.counter, 0)
	t.lastReset = now
	if count == 0 {
		avgCost = 0
	} else {
		avgCost = time.Duration(totalCostI64 / count)
	}

	tpsF64 := float64(time.Second) / float64(totalCostI64) * float64(count)
	tps = int64(tpsF64)
	return
}
