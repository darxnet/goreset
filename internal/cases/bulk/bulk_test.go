package bulk_test

import (
	"testing"
	"time"

	"github.com/darxnet/goreset/internal/cases/bulk"
)

func TestRequestReset(t *testing.T) { //nolint:paralleltest // Released is shared
	n := 7
	dim := &bulk.Dimension{W: 3, H: 4}
	buf := &bulk.Buffer{B: []byte("buffer")}
	timer := time.NewTimer(time.Hour)

	r := bulk.Request{
		Data:    []byte("data"),
		Names:   []string{"a", "b"},
		Ptr:     &n,
		M:       map[string]int{"k": 1},
		Ch:      make(chan int),
		Any:     "any",
		Fn:      func() {},
		N:       1,
		F:       1.5,
		OK:      true,
		S:       "s",
		Arr:     [4]byte{1, 2, 3, 4},
		When:    time.Now(),
		Dim:     bulk.Dimension{W: 1, H: 2},
		DimPtr:  dim,
		Cleared: bulk.Dimension{W: 5, H: 6},
		Buf:     buf,
		Timer:   timer,
		Mask:    1,
		Level:   9,
		Keep:    []byte("keep"),
	}
	r.Count.Store(5)
	r.Flag.Store(true)
	r.PtrA.Store(&n)

	dataCap, namesCap := cap(r.Data), cap(r.Names)
	released := bulk.Released.Load()

	r.Reset()

	assertZeroed(t, &r, dataCap, namesCap)
	assertKept(t, &r, dim, buf, timer, released)
}

// assertZeroed checks the fields the bulk assignment zeroes and the slices it gives back.
func assertZeroed(t *testing.T, r *bulk.Request, dataCap, namesCap int) {
	t.Helper()

	if len(r.Data) != 0 || cap(r.Data) != dataCap {
		t.Fatalf("Data = len %d cap %d, want len 0 cap %d", len(r.Data), cap(r.Data), dataCap)
	}

	if len(r.Names) != 0 || cap(r.Names) != namesCap {
		t.Fatalf("Names = len %d cap %d, want len 0 cap %d", len(r.Names), cap(r.Names), namesCap)
	}

	if r.Names[:1][0] != "" {
		t.Fatal("Names backing array was not cleared")
	}

	if r.Ptr != nil || r.M != nil || r.Ch != nil || r.Any != nil || r.Fn != nil {
		t.Fatal("reference fields are not nil")
	}

	if r.N != 0 || r.F != 0 || r.OK || r.S != "" || r.Arr != [4]byte{} || !r.When.IsZero() {
		t.Fatal("value fields are not zero")
	}

	if r.Dim != (bulk.Dimension{}) {
		t.Fatalf("Dim = %+v, want zero", r.Dim)
	}

	if r.Count.Load() != 0 || r.Flag.Load() || r.PtrA.Load() != nil {
		t.Fatal("atomics are not zero")
	}
}

// assertKept checks the fields whose state survives the reset: pointers with
// Reset, call= and release= targets, value= and reset:"-" tags.
func assertKept(
	t *testing.T, r *bulk.Request, dim *bulk.Dimension, buf *bulk.Buffer, timer *time.Timer, released int32,
) {
	t.Helper()

	if r.DimPtr != dim || *dim != (bulk.Dimension{}) {
		t.Fatalf("DimPtr = %p %+v, want the same pointer, reset", r.DimPtr, r.DimPtr)
	}

	if r.Cleared != (bulk.Dimension{W: 1, H: 1}) {
		t.Fatalf("Cleared = %+v, want Clear() applied", r.Cleared)
	}

	if r.Buf != nil || bulk.Released.Load() != released+1 || len(buf.B) != 0 {
		t.Fatalf("Buf = %v released %d, want nil and one more release", r.Buf, bulk.Released.Load()-released)
	}

	if r.Timer != timer || timer.Stop() {
		t.Fatal("Timer must be kept and stopped")
	}

	if r.Mask != ^uint32(0) {
		t.Fatalf("Mask = %#x, want value= expression", r.Mask)
	}

	if r.Level != bulk.DefaultLevel() {
		t.Fatalf("Level = %d, want consts.DefaultLevel", r.Level)
	}

	if string(r.Keep) != "keep" {
		t.Fatalf("Keep = %q, want untouched", r.Keep)
	}
}

func TestRequestResetZero(t *testing.T) {
	t.Parallel()

	var r bulk.Request

	r.Reset()

	if r.Data != nil || r.Names != nil || r.Buf != nil || r.Timer != nil || r.DimPtr != nil {
		t.Fatal("a zero Request must stay zero")
	}
}
