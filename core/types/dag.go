package types

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"golang.org/x/exp/slices"
)

// TxDAGType Used to extend TxDAG and customize a new DAG structure
const (
	EmptyTxDAGType byte = iota
	PlainTxDAGType
)

type TxDAG interface {
	// Type return TxDAG type
	Type() byte

	// Inner return inner instance
	Inner() interface{}

	// DelayGasDistribution check if delay the distribution of GasFee
	DelayGasDistribution() bool

	// TxDep query TxDeps from TxDAG
	TxDep(int) TxDep

	// TxCount return tx count
	TxCount() int
}

func EncodeTxDAG(dag TxDAG) ([]byte, error) {
	if dag == nil {
		return nil, errors.New("input nil TxDAG")
	}
	var buf bytes.Buffer
	buf.WriteByte(dag.Type())
	if err := rlp.Encode(&buf, dag.Inner()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodeTxDAG(enc []byte) (TxDAG, error) {
	if len(enc) <= 1 {
		return nil, errors.New("too short TxDAG bytes")
	}

	switch enc[0] {
	case EmptyTxDAGType:
		return NewEmptyTxDAG(), nil
	case PlainTxDAGType:
		dag := new(PlainTxDAG)
		if err := rlp.DecodeBytes(enc[1:], dag); err != nil {
			return nil, err
		}
		return dag, nil
	default:
		return nil, errors.New("unsupported TxDAG bytes")
	}
}

// EmptyTxDAG indicate that execute txs in sequence
// It means no transactions or need timely distribute transaction fees
// it only keep partial serial execution when tx cannot delay the distribution or just execute txs in sequence
type EmptyTxDAG struct {
}

func NewEmptyTxDAG() TxDAG {
	return &EmptyTxDAG{}
}

func (d *EmptyTxDAG) Type() byte {
	return EmptyTxDAGType
}

func (d *EmptyTxDAG) Inner() interface{} {
	return d
}

func (d *EmptyTxDAG) DelayGasDistribution() bool {
	return false
}

func (d *EmptyTxDAG) TxDep(int) TxDep {
	return TxDep{
		Relation:  1,
		TxIndexes: nil,
	}
}

func (d *EmptyTxDAG) TxCount() int {
	return 0
}

func (d *EmptyTxDAG) String() string {
	return "None"
}

// PlainTxDAG indicate how to use the dependency of txs, and delay the distribution of GasFee
type PlainTxDAG struct {
	// Tx Dependency List, the list index is equal to TxIndex
	TxDeps []TxDep
}

func (d *PlainTxDAG) Type() byte {
	return PlainTxDAGType
}

func (d *PlainTxDAG) Inner() interface{} {
	return d
}

func (d *PlainTxDAG) DelayGasDistribution() bool {
	return true
}

func (d *PlainTxDAG) TxDep(i int) TxDep {
	return d.TxDeps[i]
}

func (d *PlainTxDAG) TxCount() int {
	return len(d.TxDeps)
}

func NewPlainTxDAG(txLen int) *PlainTxDAG {
	return &PlainTxDAG{
		TxDeps: make([]TxDep, txLen),
	}
}

func (d *PlainTxDAG) String() string {
	builder := strings.Builder{}
	exePaths := travelExecutionPaths(d)
	for _, path := range exePaths {
		builder.WriteString(fmt.Sprintf("%v\n", path))
	}
	return builder.String()
}

func (d *PlainTxDAG) Size() int {
	enc, err := EncodeTxDAG(d)
	if err != nil {
		return 0
	}
	return len(enc)
}

func travelExecutionPaths(d TxDAG) [][]uint64 {
	txCount := d.TxCount()
	deps := make([]TxDep, txCount)
	for i := 0; i < txCount; i++ {
		dep := d.TxDep(i)
		if dep.Relation == 0 {
			deps[i] = dep
		}

		// recover to relation 0
		for j := 0; j < i; j++ {
			if !dep.Exist(j) {
				deps[i].AppendDep(j)
			}
		}
	}

	exePaths := make([][]uint64, 0)
	// travel tx deps with BFS
	for i := uint64(0); i < uint64(txCount); i++ {
		exePaths = append(exePaths, travelTargetPath(deps, i))
	}
	return exePaths
}

// TxDep store the current tx dependency relation with other txs
type TxDep struct {
	// It describes the Relation with below txs
	// 0: this tx depends on below txs
	// 1: this transaction does not depend on below txs, all other previous txs depend on
	Relation  uint8
	TxIndexes []uint64
}

func (d *TxDep) AppendDep(i int) {
	d.TxIndexes = append(d.TxIndexes, uint64(i))
}

func (d *TxDep) Exist(i int) bool {
	for _, index := range d.TxIndexes {
		if index == uint64(i) {
			return true
		}
	}

	return false
}

var (
	longestTimeTimer = metrics.NewRegisteredTimer("dag/longesttime", nil)
	longestGasTimer  = metrics.NewRegisteredTimer("dag/longestgas", nil)
	serialTimeTimer  = metrics.NewRegisteredTimer("dag/serialtime", nil)
	totalTxMeter     = metrics.NewRegisteredMeter("dag/txcnt", nil)
	totalNoDepMeter  = metrics.NewRegisteredMeter("dag/nodepcntcnt", nil)
	total2DepMeter   = metrics.NewRegisteredMeter("dag/2depcntcnt", nil)
	total4DepMeter   = metrics.NewRegisteredMeter("dag/4depcntcnt", nil)
	total8DepMeter   = metrics.NewRegisteredMeter("dag/8depcntcnt", nil)
	total16DepMeter  = metrics.NewRegisteredMeter("dag/16depcntcnt", nil)
	total32DepMeter  = metrics.NewRegisteredMeter("dag/32depcntcnt", nil)
)

func EvaluateTxDAGPerformance(dag TxDAG, stats map[int]*ExeStat) string {
	if len(stats) != dag.TxCount() || dag.TxCount() == 0 {
		return ""
	}
	sb := strings.Builder{}
	//sb.WriteString("TxDAG:\n")
	//for i, dep := range dag.TxDeps {
	//	if stats[i].mustSerialFlag {
	//		continue
	//	}
	//	sb.WriteString(fmt.Sprintf("%v: %v\n", i, dep.TxIndexes))
	//}
	//sb.WriteString("Parallel Execution Path:\n")
	paths := travelExecutionPaths(dag)
	// Attention: this is based on best schedule, it will reduce a lot by executing previous txs in parallel
	// It assumes that there is no parallel thread limit
	txCount := dag.TxCount()
	var (
		maxGasIndex     int
		maxGas          uint64
		maxTimeIndex    int
		maxTime         time.Duration
		txTimes         = make([]time.Duration, txCount)
		txGases         = make([]uint64, txCount)
		txReads         = make([]int, txCount)
		noDepdencyCount int
	)

	totalTxMeter.Mark(int64(txCount))
	for i, path := range paths {
		if stats[i].mustSerialFlag {
			continue
		}
		if len(path) <= 1 {
			noDepdencyCount++
			totalNoDepMeter.Mark(1)
		}
		if len(path) <= 3 {
			total2DepMeter.Mark(1)
		}
		if len(path) <= 5 {
			total4DepMeter.Mark(1)
		}
		if len(path) <= 9 {
			total8DepMeter.Mark(1)
		}
		if len(path) <= 17 {
			total16DepMeter.Mark(1)
		}
		if len(path) <= 33 {
			total32DepMeter.Mark(1)
		}

		// find the biggest cost time from dependency txs
		for j := 0; j < len(path)-1; j++ {
			prev := path[j]
			if txTimes[prev] > txTimes[i] {
				txTimes[i] = txTimes[prev]
			}
			if txGases[prev] > txGases[i] {
				txGases[i] = txGases[prev]
			}
			if txReads[prev] > txReads[i] {
				txReads[i] = txReads[prev]
			}
		}
		txTimes[i] += stats[i].costTime
		txGases[i] += stats[i].usedGas
		txReads[i] += stats[i].readCount

		//sb.WriteString(fmt.Sprintf("Tx%v, %.2fms|%vgas|%vreads\npath: %v\n", i, float64(txTimes[i].Microseconds())/1000, txGases[i], txReads[i], path))
		//sb.WriteString(fmt.Sprintf("%v: %v\n", i, path))
		// try to find max gas
		if txGases[i] > maxGas {
			maxGas = txGases[i]
			maxGasIndex = i
		}
		if txTimes[i] > maxTime {
			maxTime = txTimes[i]
			maxTimeIndex = i
		}
	}

	sb.WriteString(fmt.Sprintf("LargestGasPath: %.2fms|%vgas|%vreads\npath: %v\n", float64(txTimes[maxGasIndex].Microseconds())/1000, txGases[maxGasIndex], txReads[maxGasIndex], paths[maxGasIndex]))
	sb.WriteString(fmt.Sprintf("LongestTimePath: %.2fms|%vgas|%vreads\npath: %v\n", float64(txTimes[maxTimeIndex].Microseconds())/1000, txGases[maxTimeIndex], txReads[maxTimeIndex], paths[maxTimeIndex]))
	longestTimeTimer.Update(txTimes[maxTimeIndex])
	longestGasTimer.Update(txTimes[maxGasIndex])
	// serial path
	var (
		sTime time.Duration
		sGas  uint64
		sRead int
		sPath []int
	)
	for i, stat := range stats {
		if stat.mustSerialFlag {
			continue
		}
		sPath = append(sPath, i)
		sTime += stat.costTime
		sGas += stat.usedGas
		sRead += stat.readCount
	}
	if sTime == 0 {
		return ""
	}
	sb.WriteString(fmt.Sprintf("SerialPath: %.2fms|%vgas|%vreads\npath: %v\n", float64(sTime.Microseconds())/1000, sGas, sRead, sPath))
	maxParaTime := txTimes[maxTimeIndex]
	sb.WriteString(fmt.Sprintf("Estimated saving: %.2fms, %.2f%%, %.2fX, noDepCnt: %v|%.2f%%\n",
		float64((sTime-maxParaTime).Microseconds())/1000, float64(sTime-maxParaTime)/float64(sTime)*100,
		float64(sTime)/float64(maxParaTime), noDepdencyCount, float64(noDepdencyCount)/float64(txCount)*100))
	serialTimeTimer.Update(sTime)
	return sb.String()
}

func travelTargetPath(deps []TxDep, from uint64) []uint64 {
	q := make([]uint64, 0, len(deps))
	path := make([]uint64, 0, len(deps))

	q = append(q, from)
	path = append(path, from)
	for len(q) > 0 {
		t := make([]uint64, 0, len(deps))
		for _, i := range q {
			for _, dep := range deps[i].TxIndexes {
				if !slices.Contains(path, dep) {
					path = append(path, dep)
					t = append(t, dep)
				}
			}
		}
		q = t
	}
	slices.Sort(path)
	return path
}

// StateVersion record specific TxIndex & TxIncarnation
// if TxIndex equals to -1, it means the state read from DB.
type StateVersion struct {
	TxIndex int
	// TODO(galaio): used for multi ver state
	TxIncarnation int
}

// ReadRecord keep read value & its version
type ReadRecord struct {
	StateVersion
	Val interface{}
}

// WriteRecord keep latest state value & change count
type WriteRecord struct {
	Val interface{}
}

// RWSet record all read & write set in txs
// Attention: this is not a concurrent safety structure
type RWSet struct {
	ver      StateVersion
	readSet  map[RWKey]*ReadRecord
	writeSet map[RWKey]*WriteRecord

	// some flags
	mustSerial bool
}

func NewRWSet(ver StateVersion) *RWSet {
	return &RWSet{
		ver:      ver,
		readSet:  make(map[RWKey]*ReadRecord),
		writeSet: make(map[RWKey]*WriteRecord),
	}
}

func (s *RWSet) RecordRead(key RWKey, ver StateVersion, val interface{}) {
	// only record the first read version
	if _, exist := s.readSet[key]; exist {
		return
	}
	s.readSet[key] = &ReadRecord{
		StateVersion: ver,
		Val:          val,
	}
}

func (s *RWSet) RecordWrite(key RWKey, val interface{}) {
	wr, exist := s.writeSet[key]
	if !exist {
		s.writeSet[key] = &WriteRecord{
			Val: val,
		}
		return
	}
	wr.Val = val
}

func (s *RWSet) Version() StateVersion {
	return s.ver
}

func (s *RWSet) ReadSet() map[RWKey]*ReadRecord {
	return s.readSet
}

func (s *RWSet) WriteSet() map[RWKey]*WriteRecord {
	return s.writeSet
}

func (s *RWSet) WithSerialFlag() *RWSet {
	s.mustSerial = true
	return s
}

func (s *RWSet) String() string {
	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("tx: %v, inc: %v\nreadSet: [", s.ver.TxIndex, s.ver.TxIncarnation))
	i := 0
	for key, _ := range s.readSet {
		if i > 0 {
			builder.WriteString(fmt.Sprintf(", %v", key.String()))
			continue
		}
		builder.WriteString(fmt.Sprintf("%v", key.String()))
		i++
	}
	builder.WriteString("]\nwriteSet: [")
	i = 0
	for key, _ := range s.writeSet {
		if i > 0 {
			builder.WriteString(fmt.Sprintf(", %v", key.String()))
			continue
		}
		builder.WriteString(fmt.Sprintf("%v", key.String()))
		i++
	}
	builder.WriteString("]\n")
	return builder.String()
}

const (
	AccountStatePrefix = 'a'
	StorageStatePrefix = 's'
)

type RWKey [1 + common.AddressLength + common.HashLength]byte

type AccountState byte

const (
	AccountSelf AccountState = iota
	AccountNonce
	AccountBalance
	AccountCodeHash
	AccountSuicide
)

func AccountStateKey(account common.Address, state AccountState) RWKey {
	var key RWKey
	key[0] = AccountStatePrefix
	copy(key[1:], account.Bytes())
	key[1+common.AddressLength] = byte(state)
	return key
}

func StorageStateKey(account common.Address, state common.Hash) RWKey {
	var key RWKey
	key[0] = StorageStatePrefix
	copy(key[1:], account.Bytes())
	copy(key[1+common.AddressLength:], state.Bytes())
	return key
}

func (key *RWKey) IsAccountState() (bool, AccountState) {
	return AccountStatePrefix == key[0], AccountState(key[1+common.AddressLength])
}

func (key *RWKey) IsAccountSelf() bool {
	ok, s := key.IsAccountState()
	if !ok {
		return false
	}
	return s == AccountSelf
}

func (key *RWKey) IsAccountSuicide() bool {
	ok, s := key.IsAccountState()
	if !ok {
		return false
	}
	return s == AccountSuicide
}

func (key *RWKey) ToAccountSelf() RWKey {
	return AccountStateKey(key.Addr(), AccountSelf)
}

func (key *RWKey) IsStorageState() bool {
	return StorageStatePrefix == key[0]
}

func (key *RWKey) String() string {
	return hex.EncodeToString(key[:])
}

func (key *RWKey) Addr() common.Address {
	return common.BytesToAddress(key[1 : 1+common.AddressLength])
}

type PendingWrite struct {
	Ver StateVersion
	Val interface{}
}

func NewPendingWrite(ver StateVersion, wr *WriteRecord) *PendingWrite {
	return &PendingWrite{
		Ver: ver,
		Val: wr.Val,
	}
}

func (w *PendingWrite) TxIndex() int {
	return w.Ver.TxIndex
}

func (w *PendingWrite) TxIncarnation() int {
	return w.Ver.TxIncarnation
}

type PendingWrites struct {
	list []*PendingWrite
}

func NewPendingWrites() *PendingWrites {
	return &PendingWrites{
		list: make([]*PendingWrite, 0),
	}
}

func (w *PendingWrites) Append(pw *PendingWrite) {
	if i, found := w.SearchTxIndex(pw.TxIndex()); found {
		w.list[i] = pw
		return
	}

	w.list = append(w.list, pw)
	for i := len(w.list) - 1; i > 0; i-- {
		if w.list[i].TxIndex() > w.list[i-1].TxIndex() {
			break
		}
		w.list[i-1], w.list[i] = w.list[i], w.list[i-1]
	}
}

func (w *PendingWrites) SearchTxIndex(txIndex int) (int, bool) {
	n := len(w.list)
	i, j := 0, n
	for i < j {
		h := int(uint(i+j) >> 1)
		// i ≤ h < j
		if w.list[h].TxIndex() < txIndex {
			i = h + 1
		} else {
			j = h
		}
	}
	return i, i < n && w.list[i].TxIndex() == txIndex
}

func (w *PendingWrites) FindLastWrite(txIndex int) *PendingWrite {
	var i, _ = w.SearchTxIndex(txIndex)
	for j := i - 1; j >= 0; j-- {
		if w.list[j].TxIndex() < txIndex {
			return w.list[j]
		}
	}

	return nil
}

type MVStates struct {
	rwSets          map[int]*RWSet
	pendingWriteSet map[RWKey]*PendingWrites

	// dependency map cache for generating TxDAG
	// depsCache[i].exist(j) means j->i, and i > j
	depsCache map[int]TxDepMap

	// execution stat infos
	stats map[int]*ExeStat
	lock  sync.RWMutex
}

func NewMVStates(txCount int) *MVStates {
	return &MVStates{
		rwSets:          make(map[int]*RWSet, txCount),
		pendingWriteSet: make(map[RWKey]*PendingWrites, txCount*8),
		depsCache:       make(map[int]TxDepMap, txCount),
		stats:           make(map[int]*ExeStat, txCount),
	}
}

func (s *MVStates) RWSets() map[int]*RWSet {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.rwSets
}

func (s *MVStates) Stats() map[int]*ExeStat {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.stats
}

func (s *MVStates) RWSet(index int) *RWSet {
	s.lock.RLock()
	defer s.lock.RUnlock()
	if index >= len(s.rwSets) {
		return nil
	}
	return s.rwSets[index]
}

// ReadState TODO(galaio): read state from MVStates
func (s *MVStates) ReadState(key RWKey) (interface{}, bool) {
	return nil, false
}

// FulfillRWSet it can execute as async, and rwSet & stat must guarantee read-only
// TODO(galaio): try to generate TxDAG, when fulfill RWSet
// TODO(galaio): support flag to stat execution as optional
func (s *MVStates) FulfillRWSet(rwSet *RWSet, stat *ExeStat) error {
	log.Debug("FulfillRWSet", "s.len", len(s.rwSets), "cur", rwSet.ver.TxIndex, "reads", len(rwSet.readSet), "writes", len(rwSet.writeSet))
	s.lock.Lock()
	defer s.lock.Unlock()
	index := rwSet.ver.TxIndex
	if s := s.rwSets[index]; s != nil {
		return errors.New("refill a exist RWSet")
	}
	if stat != nil {
		if stat.txIndex != index {
			return errors.New("wrong execution stat")
		}
		s.stats[index] = stat
	}

	// analysis dep, if the previous transaction is not executed/validated, re-analysis is required
	if _, ok := s.depsCache[index]; !ok {
		s.depsCache[index] = NewTxDeps(0)
	}
	for prev := 0; prev < index; prev++ {
		// if there are some parallel execution or system txs, it will fulfill in advance
		// it's ok, and try re-generate later
		if _, ok := s.rwSets[prev]; !ok {
			continue
		}
		if checkDependency(s.rwSets[prev].writeSet, rwSet.readSet) {
			s.depsCache[index].add(prev)
			// clear redundancy deps compared with prev
			for dep := range s.depsCache[index] {
				if s.depsCache[prev].exist(dep) {
					s.depsCache[index].remove(dep)
				}
			}
		}
	}

	// append to pending write set
	for k, v := range rwSet.writeSet {
		// TODO(galaio): this action is only for testing, it can be removed in production mode.
		// ignore no changed write record
		checkRWSetInconsistent(index, k, rwSet.readSet, rwSet.writeSet)
		if _, exist := s.pendingWriteSet[k]; !exist {
			s.pendingWriteSet[k] = NewPendingWrites()
		}
		s.pendingWriteSet[k].Append(NewPendingWrite(rwSet.ver, v))
	}
	s.rwSets[index] = rwSet
	return nil
}

func checkRWSetInconsistent(index int, k RWKey, readSet map[RWKey]*ReadRecord, writeSet map[RWKey]*WriteRecord) bool {
	var (
		readOk  bool
		writeOk bool
		r       *WriteRecord
	)

	if k.IsAccountSuicide() {
		_, readOk = readSet[k.ToAccountSelf()]
	} else {
		_, readOk = readSet[k]
	}

	r, writeOk = writeSet[k]
	if readOk != writeOk {
		// check if it's correct? read nil, write non-nil
		log.Info("checkRWSetInconsistent find inconsistent", "tx", index, "k", k.String(), "read", readOk, "write", writeOk, "val", r.Val)
		return true
	}

	return false
}

func isEqualRWVal(key RWKey, src interface{}, compared interface{}) bool {
	if ok, state := key.IsAccountState(); ok {
		switch state {
		case AccountBalance:
			if src != nil && compared != nil {
				return equalUint256(src.(*uint256.Int), compared.(*uint256.Int))
			}
			return src == compared
		case AccountNonce:
			return src.(uint64) == compared.(uint64)
		case AccountCodeHash:
			if src != nil && compared != nil {
				return slices.Equal(src.([]byte), compared.([]byte))
			}
			return src == compared
		}
		return false
	}

	if src != nil && compared != nil {
		return src.(common.Hash) == compared.(common.Hash)
	}
	return src == compared
}

func equalUint256(s, c *uint256.Int) bool {
	if s != nil && c != nil {
		return s.Eq(c)
	}

	return s == c
}

// ResolveTxDAG generate TxDAG from RWSets
func (s *MVStates) ResolveTxDAG() TxDAG {
	rwSets := s.RWSets()
	txDAG := NewPlainTxDAG(len(rwSets))
	for i := len(rwSets) - 1; i >= 0; i-- {
		txDAG.TxDeps[i].TxIndexes = []uint64{}
		if rwSets[i].mustSerial {
			txDAG.TxDeps[i].Relation = 1
			continue
		}
		if s.depsCache[i] != nil {
			txDAG.TxDeps[i].TxIndexes = s.depsCache[i].toArray()
			continue
		}
		readSet := rwSets[i].ReadSet()
		// TODO: check if there are RW with system address
		// check if there has written op before i
		for j := 0; j < i; j++ {
			if checkDependency(rwSets[j].writeSet, readSet) {
				txDAG.TxDeps[i].AppendDep(j)
			}
		}
	}

	return txDAG
}

func checkDependency(writeSet map[RWKey]*WriteRecord, readSet map[RWKey]*ReadRecord) bool {
	// check tx dependency, only check key, skip version
	for k, _ := range writeSet {
		// check suicide, add read address flag, it only for check suicide quickly, and cannot for other scenarios.
		if k.IsAccountSuicide() {
			if _, ok := readSet[k.ToAccountSelf()]; ok {
				return true
			}
			continue
		}
		if _, ok := readSet[k]; ok {
			return true
		}
	}

	return false
}

type ExeStat struct {
	txIndex   int
	usedGas   uint64
	readCount int
	startTime time.Time
	costTime  time.Duration
	// TODO: consider system tx, gas fee issues, may need to use different flag
	mustSerialFlag bool
}

func NewExeStat(txIndex int) *ExeStat {
	return &ExeStat{
		txIndex: txIndex,
	}
}

func (s *ExeStat) Begin() *ExeStat {
	s.startTime = time.Now()
	return s
}

func (s *ExeStat) Done() *ExeStat {
	s.costTime = time.Since(s.startTime)
	return s
}

func (s *ExeStat) WithSerialFlag() *ExeStat {
	s.mustSerialFlag = true
	return s
}

func (s *ExeStat) WithGas(gas uint64) *ExeStat {
	s.usedGas = gas
	return s
}

func (s *ExeStat) WithRead(rc int) *ExeStat {
	s.readCount = rc
	return s
}

type TxDepMap map[int]struct{}

func NewTxDeps(cap int) TxDepMap {
	return make(map[int]struct{}, cap)
}

func (m TxDepMap) add(index int) {
	m[index] = struct{}{}
}

func (m TxDepMap) exist(index int) bool {
	_, ok := m[index]
	return ok
}

func (m TxDepMap) toArray() []uint64 {
	ret := make([]uint64, 0, len(m))
	for index := range m {
		ret = append(ret, uint64(index))
	}
	slices.Sort(ret)
	return ret
}

func (m TxDepMap) remove(index int) {
	delete(m, index)
}
