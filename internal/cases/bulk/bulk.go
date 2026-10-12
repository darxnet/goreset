// Package bulk is a goreset test case: a struct without locks, reset in one
// assignment with the kept fields restored after it.
package bulk

import (
	"sync/atomic"
	"time"

	"github.com/darxnet/goreset/internal/cases/consts"
)

//go:generate go run ../../../cmd/goreset -type=Request

// Dimension is a small value type with its own Reset: bulk mode keeps it by copy.
type Dimension struct {
	W, H uint16
}

func (d *Dimension) Reset() {
	d.W, d.H = 0, 0
}

// Clear is what `reset:"call=Clear"` calls instead of Reset.
func (d *Dimension) Clear() {
	d.W, d.H = 1, 1
}

// Buffer is handed back through PutBuffer by `reset:"release=PutBuffer"`.
type Buffer struct {
	B []byte
}

func (b *Buffer) Reset() {
	b.B = b.B[:0]
}

// Released counts the buffers PutBuffer received.
var Released atomic.Int32

func PutBuffer(b *Buffer) {
	b.Reset()
	Released.Add(1)
}

// Big is too big to keep by copy on its own; `reset:"keep"` keeps it anyway.
type Big struct {
	A [16]uint64
}

func (b *Big) Reset() {
	b.A = [16]uint64{}
}

// NotAStruct is here for the error path of Generate.
type NotAStruct int

// DefaultLevel is what a reset Request's Level is; the tag names the constant
// through import=, which is also why this package imports consts.
func DefaultLevel() uint8 {
	return consts.DefaultLevel
}

type Request struct {
	Data    []byte
	Names   []string
	Ptr     *int
	M       map[string]int
	Ch      chan int
	Any     any
	Fn      func()
	N       int
	F       float64
	OK      bool
	S       string
	Arr     [4]byte
	When    time.Time
	Dim     Dimension
	DimPtr  *Dimension
	Cleared Dimension   `reset:"call=Clear"`
	Buf     *Buffer     `reset:"release=PutBuffer"`
	Timer   *time.Timer `reset:"call=Stop"`
	Mask    uint32      `reset:"value=^uint32(0)"`
	Level   uint8       `reset:"value=consts.DefaultLevel,import=github.com/darxnet/goreset/internal/cases/consts"`
	Keep    []byte      `reset:"-"`
	BigKept Big         `reset:"keep"`
	Count   atomic.Int64
	Flag    atomic.Bool
	PtrA    atomic.Pointer[int]
}
