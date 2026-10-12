// Package fieldwise is a goreset test case: a struct that cannot be reset in
// one assignment (it holds a lock and a big value with its own Reset), so
// every field is reset on its own.
package fieldwise

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

//go:generate go run ../../../cmd/goreset -type=State

// Big is a value type with its own Reset that is too big to keep by copy.
type Big struct {
	A [16]uint64
}

func (b *Big) Reset() {
	b.A = [16]uint64{}
}

type Dimension struct {
	W, H uint16
}

func (d *Dimension) Reset() {
	d.W, d.H = 0, 0
}

func (d *Dimension) Clear() {
	d.W, d.H = 1, 1
}

type Buffer struct {
	B []byte
}

func (b *Buffer) Reset() {
	b.B = b.B[:0]
}

var Released atomic.Int32

func PutBuffer(b *Buffer) {
	b.Reset()
	Released.Add(1)
}

type State struct {
	Mu      sync.Mutex
	Data    []byte
	Big     Big
	When    time.Time
	Addr    netip.Addr
	Ptr     *int
	M       map[string]int
	Ch      chan int
	Any     any
	Fn      func()
	N       int
	OK      bool
	S       string
	Arr     [2]int
	Dim     Dimension
	DimPtr  *Dimension
	Cleared Dimension   `reset:"call=Clear"`
	Buf     *Buffer     `reset:"release=PutBuffer"`
	Timer   *time.Timer `reset:"call=Stop"`
	Mask    uint32      `reset:"value=^uint32(0)"`
	Keep    []byte      `reset:"-"`
	Count   atomic.Int64
	Flag    atomic.Bool
	PtrA    atomic.Pointer[int]
}
