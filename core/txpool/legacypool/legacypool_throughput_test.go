package legacypool

import (
	"testing"
	"time"
)

func TestThroughputAvg(t *testing.T) {
	tp := throughput{
		lastReset: time.Now(),
		cost:      0,
		counter:   0,
	}
	tp.mark(200*time.Millisecond, 4)
	tp.mark(50*time.Millisecond, 1)
	avg, _, total, count, tps := tp.avgAndRest(tp.lastReset.Add(500 * time.Millisecond))
	if avg != 50*time.Millisecond {
		t.Errorf("expected avg to be 50ms, got %v", avg)
	}
	if total != 250*time.Millisecond {
		t.Errorf("expected total to be 250ms, got %v", total)
	}
	if count != 5 {
		t.Errorf("expected count to be 5, got %v", count)
	}
	if tps != 20 {
		t.Errorf("expected tps to be 20, got %v", tps)
	}

	tp = throughput{}
	tp.lastReset = time.Now()
	tp.mark(200*time.Millisecond, 0)
	avg, _, total, count, tps = tp.avgAndRest(tp.lastReset.Add(500 * time.Millisecond))
	if avg != 0 {
		t.Errorf("expected avg to be 0, got %v", avg)
	}
	if total != 200*time.Millisecond {
		t.Errorf("expected total to be 200ms, got %v", total)
	}
	if count != 0 {
		t.Errorf("expected count to be 0, got %v", count)
	}
	if tps != 0 {
		t.Errorf("expected tps to be 0, got %v", tps)
	}

}
