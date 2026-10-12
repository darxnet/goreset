// Package bulk is a goreset test case: a struct without locks and with enough
// plain fields for the cost model to pick one assignment with the kept
// fields restored after it; every tag is exercised in that mode.
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

// Big is a value with Reset of 128 bytes: kept by copy like any other, the
// model charges the two copies to bulk mode.
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
	BigKept Big
	Count   atomic.Int64
	Flag    atomic.Bool
	PtrA    atomic.Pointer[int]

	// Plain fields the one assignment zeroes for free: with the kept fields
	// above costing a restore each, these are what makes bulk mode pay off.
	P00 uint8
	P01 uint8
	P02 uint8
	P03 uint8
	P04 uint8
	P05 uint8
	P06 uint8
	P07 uint8
	P08 uint8
	P09 uint8
	P10 uint8
	P11 uint8
	P12 uint8
	P13 uint8
	P14 uint8
	P15 uint8
	P16 uint8
	P17 uint8
	P18 uint8
	P19 uint8
	P20 uint8
	P21 uint8
	P22 uint8
	P23 uint8
	P24 uint8
	P25 uint8
	P26 uint8
	P27 uint8
	P28 uint8
	P29 uint8
	P30 uint8
	P31 uint8
	P32 uint8
	P33 uint8
	P34 uint8
	P35 uint8
	P36 uint8
	P37 uint8
	P38 uint8
	P39 uint8
	P40 uint8
	P41 uint8
	P42 uint8
	P43 uint8
	P44 uint8
	P45 uint8
	P46 uint8
	P47 uint8
}
