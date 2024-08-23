package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

func TestParallelStateScheduleRun(t *testing.T) {
	// case 1: empty txs
	// case 2: txs with no dependencies, no conflicts
	// case 3: txs with no dependencies, with 1 conflict
}

func TestNewTxLevels(t *testing.T) {
	// definition of dependencies:
	//    {-1} means dependent all txs
	//    {-2} means excluded tx
	//    nil means no dependencies
	//    {0,1} means dependent on tx[0] and tx[1]

	// case 1: empty txs
	assertEqual(levels([]uint64{}, [][]int{}), [][]uint64{}, t)

	// case 2: txs with no dependencies
	// tx[0] has no dependencies, tx[0].Nonce() == 1
	// tx[1] has no dependencies, tx[1].Nonce() == 2
	// tx[2] has no dependencies, tx[2].Nonce() == 3
	// tx[3] has no dependencies, tx[3].Nonce() == 4
	assertEqual(levels([]uint64{1, 2, 3, 4}, [][]int{nil, nil, nil, nil}), [][]uint64{{1, 2, 3, 4}}, t)

	// case 3: txs with dependencies
	// tx[0] has no dependencies, tx[0].Nonce() == 1
	// tx[1] depends on tx[0], tx[1].Nonce() == 2
	// tx[2] depends on tx[1], tx[2].Nonce() == 3
	assertEqual(levels([]uint64{1, 2, 3}, [][]int{nil, {0}, {1}}), [][]uint64{{1}, {2}, {3}}, t)

	// case 4: txs with dependencies and no dependencies
	// tx[0] has no dependencies, tx[0].Nonce() == 1
	// tx[1] has no dependencies, tx[1].Nonce() == 2
	// tx[2] dependents on t[0], tx[2].Nonce() == 3
	// tx[3] dependents on t[0], tx[3].Nonce() == 4
	// tx[4] dependents on t[2], tx[4].Nonce() == 5
	// tx[5] dependents on t[3], tx[5].Nonce() == 6
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6}, [][]int{nil, nil, {0}, {0}, {2}, {3}}), [][]uint64{{1, 2}, {3, 4}, {5, 6}}, t)

	// case 5: 1 excluded tx + n no dependencies tx
	assertEqual(levels([]uint64{1, 2, 3}, [][]int{{-1}, nil, nil}), [][]uint64{{1}, {2, 3}}, t)

	// case 6: 1 excluded tx + n no dependencies txs + n all-dependencies txs
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6}, [][]int{{-1}, nil, nil, nil, {-2}, {-2}}), [][]uint64{{1}, {2, 3, 4}, {5}}, t)

	// case 7: 1 excluded tx + n no dependencies txs + n dependencies txs + 1 all-dependencies tx
	assertEqual(levels([]uint64{1, 2, 3, 4, 5, 6, 7}, [][]int{{-1}, nil, nil, nil, {0, 1}, {2}, {-2}}), [][]uint64{{1}, {2, 3, 4}, {5, 6}, {7}}, t)

	// case 8:  n no dependencies txs + n all-dependencies txs
	assertEqual(levels([]uint64{1, 2, 3, 4, 5}, [][]int{nil, nil, nil, {-2}, {-2}}), [][]uint64{{1, 2, 3}, {4}, {5}}, t)
}

func levels(nonces []uint64, txdag [][]int) TxLevels {
	return nil
}

func nonces2txs(nonces []uint64) []*ParallelTxRequest {
	return nil
}

func int2txdag(txdag [][]int) types.TxDAG {
	return nil
}

func assertEqual(actual TxLevels, expected [][]uint64, t *testing.T) {
	if len(actual) != len(expected) {
		t.Fatalf("expected %d levels, got %d levels", len(expected), len(actual))
		return
	}
	for i, txLevel := range actual {
		if len(txLevel) != len(expected[i]) {
			t.Fatalf("expected %d txs in level %d, got %d txs", len(expected[i]), i, len(txLevel))
			return
		}
		for j, tx := range txLevel {
			if tx.tx.Nonce() != expected[i][j] {
				t.Errorf("expected nonce: %d, got nonce: %d", tx.tx.Nonce(), expected[i][j])
			}
		}
	}
}
