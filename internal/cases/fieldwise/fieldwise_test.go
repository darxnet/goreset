package fieldwise_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/darxnet/goreset/internal/cases/fieldwise"
)

func TestStateReset(t *testing.T) { //nolint:paralleltest // Released is shared
	n := 7
	dim := &fieldwise.Dimension{W: 3, H: 4}
	buf := &fieldwise.Buffer{B: []byte("buffer")}
	timer := time.NewTimer(time.Hour)

	s := fieldwise.State{
		Data:    []byte("data"),
		Big:     fieldwise.Big{A: [16]uint64{1}},
		When:    time.Now(),
		Addr:    netip.MustParseAddr("10.0.0.1"),
		Ptr:     &n,
		M:       map[string]int{"k": 1},
		Ch:      make(chan int),
		Any:     "any",
		Fn:      func() {},
		N:       1,
		OK:      true,
		S:       "s",
		Arr:     [2]int{1, 2},
		Dim:     fieldwise.Dimension{W: 1, H: 2},
		DimPtr:  dim,
		Cleared: fieldwise.Dimension{W: 5, H: 6},
		Buf:     buf,
		Timer:   timer,
		Mask:    1,
		Keep:    []byte("keep"),
	}
	s.Count.Store(5)
	s.Flag.Store(true)
	s.PtrA.Store(&n)
	s.Mu.Lock()

	dataCap := cap(s.Data)
	released := fieldwise.Released.Load()

	s.Reset()

	if s.Mu.TryLock() {
		t.Fatal("Mu must be left locked: sync types are not touched")
	}

	s.Mu.Unlock()

	if len(s.Data) != 0 || cap(s.Data) != dataCap {
		t.Fatalf("Data = len %d cap %d, want len 0 cap %d", len(s.Data), cap(s.Data), dataCap)
	}

	if s.Big != (fieldwise.Big{}) || !s.When.IsZero() || s.Addr.IsValid() {
		t.Fatal("struct fields are not zero")
	}

	if s.Ptr != nil || s.M != nil || s.Ch != nil || s.Any != nil || s.Fn != nil {
		t.Fatal("reference fields are not nil")
	}

	if s.N != 0 || s.OK || s.S != "" || s.Arr != [2]int{} {
		t.Fatal("value fields are not zero")
	}

	if s.Dim != (fieldwise.Dimension{}) || s.DimPtr != dim || *dim != (fieldwise.Dimension{}) {
		t.Fatal("Dim and DimPtr must be reset through Reset")
	}

	if s.Cleared != (fieldwise.Dimension{W: 1, H: 1}) {
		t.Fatalf("Cleared = %+v, want Clear() applied", s.Cleared)
	}

	if s.Buf != nil || fieldwise.Released.Load() != released+1 {
		t.Fatal("Buf must be released and set to nil")
	}

	if s.Timer != timer || timer.Stop() {
		t.Fatal("Timer must be kept and stopped")
	}

	if s.Mask != ^uint32(0) || string(s.Keep) != "keep" {
		t.Fatal("tagged fields are wrong")
	}

	if s.Count.Load() != 0 || s.Flag.Load() || s.PtrA.Load() != nil {
		t.Fatal("atomics are not zero")
	}
}

func TestStateResetZero(t *testing.T) {
	t.Parallel()

	var s fieldwise.State

	s.Reset()

	if s.Data != nil || s.Buf != nil || s.Timer != nil || s.DimPtr != nil {
		t.Fatal("a zero State must stay zero")
	}
}
