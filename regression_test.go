package goreset_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darxnet/goreset"
)

// checkGenerated writes a throwaway module, generates Reset for typeNames
// into it and runs go vet and go test there: the generated code must compile,
// pass vet (copylocks among others) and behave as the module's own tests say.
// It returns the generated sources by type name.
func checkGenerated(t *testing.T, name string, files map[string]string, typeNames ...string) map[string]string {
	t.Helper()

	dir := writeModule(t, name, files)

	pkg, err := goreset.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	srcs := make(map[string]string, len(typeNames))

	for _, typeName := range typeNames {
		src, err := pkg.Generate(typeName)
		if err != nil {
			t.Fatalf("Generate(%s): %v", typeName, err)
		}

		if err := os.WriteFile(filepath.Join(dir, goreset.FileName(typeName)), src, 0o600); err != nil {
			t.Fatal(err)
		}

		srcs[typeName] = string(src)
	}

	for _, args := range [][]string{{"vet", "./..."}, {"test", "-count=1", "-timeout=60s", "./..."}} {
		cmd := exec.Command("go", args...) //nolint:gosec // fixed go subcommands
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")

		if out, err := cmd.CombinedOutput(); err != nil {
			var all strings.Builder
			for typeName, src := range srcs {
				all.WriteString("--- " + typeName + " ---\n" + src)
			}

			t.Fatalf("go %s: %v\n%s\n%s", strings.Join(args, " "), err, out, all.String())
		}
	}

	return srcs
}

// TestInterfaceWithReset: an interface field is set to nil, even when its
// method set has Reset — calling it would panic on nil and keep the value.
func TestInterfaceWithReset(t *testing.T) {
	t.Parallel()

	srcs := checkGenerated(t, "iface", map[string]string{
		"m.go": `package m

import (
	"hash"
	"sync"
)

type Resetter interface{ Reset() }

type Bulk struct {
	H    hash.Hash
	R    Resetter
	A, B int
}

type Fieldwise struct {
	Mu sync.Mutex
	H  hash.Hash
	R  Resetter
	C  Resetter ` + "`" + `reset:"call=Reset"` + "`" + ` // kept: called when not nil
}
`,
		"m_test.go": `package m

import (
	"hash/fnv"
	"testing"
)

type res struct{}

func (*res) Reset() {}

func TestReset(t *testing.T) {
	var b Bulk
	b.Reset()

	b = Bulk{H: fnv.New32(), R: &res{}, A: 1}
	b.Reset()

	if b.H != nil || b.R != nil || b.A != 0 {
		t.Fatalf("Bulk = %+v, want zero", b)
	}

	var f Fieldwise
	f.Reset()

	f.H, f.R, f.C = fnv.New32(), &res{}, &res{}
	f.Reset()

	if f.H != nil || f.R != nil || f.C == nil {
		t.Fatalf("Fieldwise = %v %v %v, want H and R nil, C kept", f.H, f.R, f.C)
	}
}
`,
	}, "Bulk", "Fieldwise")

	if !strings.Contains(srcs["Bulk"], "*r = Bulk{}") {
		t.Errorf("Bulk is not reset in one assignment:\n%s", srcs["Bulk"])
	}

	if !strings.Contains(srcs["Fieldwise"], "r.H = nil") || !strings.Contains(srcs["Fieldwise"], "r.R = nil") {
		t.Errorf("Fieldwise does not set the interfaces to nil:\n%s", srcs["Fieldwise"])
	}
}

// TestLocksNotCopied: bulk mode copies the fields it keeps, so a kept field
// holding a lock or a sync/atomic value (reset:"-", a value with Reset,
// call= on a value) forces field-wise mode, as a sync field does.
func TestLocksNotCopied(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"m.go": `package m

import (
	"sync"
	"sync/atomic"
)

type Stats struct {
	mu sync.Mutex
	n  int
}

func (s *Stats) Reset() { s.n = 0 }

type Counter struct{ c atomic.Int64 }

func (c *Counter) Clear() { c.c.Store(0) }

// spin is a lock of its own: go vet's copylocks knows it by Lock and Unlock.
type spin struct{ v int32 }

func (s *spin) Lock()   { s.v = 1 }
func (s *spin) Unlock() { s.v = 0 }

// Skipped keeps a lock through reset:"-".
type Skipped struct {
	Mu [1]sync.Mutex ` + "`" + `reset:"-"` + "`" + `
	P0, P1, P2, P3, P4, P5, P6, P7 uint8
}

// Spun keeps a lock of its own through reset:"-".
type Spun struct {
	L spin ` + "`" + `reset:"-"` + "`" + `
	P0, P1, P2, P3, P4, P5, P6, P7 uint8
}

// Nested has a value with Reset that holds a lock.
type Nested struct {
	S Stats
	P0, P1, P2, P3, P4, P5, P6, P7 uint8
}

// Called has call= on a value holding an atomic.
type Called struct {
	C Counter ` + "`" + `reset:"call=Clear"` + "`" + `
	P0, P1, P2, P3, P4, P5, P6, P7 uint8
}
`,
		"m_test.go": `package m

import "testing"

func TestReset(t *testing.T) {
	var s Skipped
	s.Mu[0].Lock()
	s.P0 = 1
	s.Reset()
	if s.Mu[0].TryLock() || s.P0 != 0 {
		t.Fatal("Skipped: Mu must stay locked, P0 zeroed")
	}

	var sp Spun
	sp.L.Lock()
	sp.P0 = 1
	sp.Reset()
	if sp.L.v != 1 || sp.P0 != 0 {
		t.Fatal("Spun: L must stay locked, P0 zeroed")
	}

	var n Nested
	n.S.n = 1
	n.Reset()
	if n.S.n != 0 {
		t.Fatal("Nested: S not reset")
	}

	var c Called
	c.C.c.Store(5)
	c.Reset()
	if c.C.c.Load() != 0 {
		t.Fatal("Called: Clear not called")
	}
}
`,
	}

	srcs := checkGenerated(t, "locks", files, "Skipped", "Spun", "Nested", "Called")

	for typeName, src := range srcs {
		if strings.Contains(src, "*r = "+typeName+"{}") {
			t.Errorf("%s holds a lock it keeps, yet is reset in one assignment:\n%s", typeName, src)
		}
	}

	pkg, err := goreset.Load(writeModule(t, "lockhints", files))
	if err != nil {
		t.Fatal(err)
	}

	res, err := pkg.Analyze("Nested")
	if err != nil {
		t.Fatal(err)
	}

	wantReason := "S holds a sync.Mutex, which bulk mode would copy"
	if res.Bulk || len(res.Hints) != 1 || !strings.HasPrefix(res.Hints[0], "S: a Stats holds a sync.Mutex") ||
		!strings.Contains(string(res.Source), wantReason) {
		t.Fatalf("Analyze(Nested) = bulk %v, hints %q, want field-wise for %q:\n%s",
			res.Bulk, res.Hints, wantReason, res.Source)
	}
}

// TestAtomics: sync/atomic types are reset through Store, except Value,
// which cannot store a zero of every type it may hold and is zeroed; a type
// of another package named atomic (go.uber.org/atomic) is no sync/atomic
// type, and two packages of the same name get distinct import names.
func TestAtomics(t *testing.T) {
	t.Parallel()

	srcs := checkGenerated(t, "atomics", map[string]string{
		"atomic/atomic.go": `// Package atomic stands for go.uber.org/atomic.
package atomic

type String struct{ v string }

func (s *String) Store(v string) { s.v = v }
func (s *String) Load() string   { return s.v }
`,
		"m.go": `package m

import (
	"sync"
	"sync/atomic"

	uatomic "example.com/atomics/atomic"
)

type Fieldwise struct {
	Mu sync.Mutex
	V  atomic.Value
	U  atomic.Uint32
	S  uatomic.String
}
`,
		"m_test.go": `package m

import "testing"

func TestReset(t *testing.T) {
	var f Fieldwise
	f.Reset()

	f.V.Store("text")
	f.U.Store(3)
	f.S.Store("text")
	f.Reset()

	if f.V.Load() != nil || f.U.Load() != 0 || f.S.Load() != "" {
		t.Fatalf("Fieldwise = %v %v %q, want zero", f.V.Load(), f.U.Load(), f.S.Load())
	}
}
`,
	}, "Fieldwise")

	for _, want := range []string{"r.V = atomic.Value{}", "r.U.Store(0)", "r.S = atomic2.String{}"} {
		if !strings.Contains(srcs["Fieldwise"], want) {
			t.Errorf("Fieldwise lacks %q:\n%s", want, srcs["Fieldwise"])
		}
	}
}

// TestSliceTailCleared: a slice of pointer-bearing elements is cleared up to
// its capacity, so the elements past its length do not keep memory alive;
// a slice of plain values is cleared up to its length only.
func TestSliceTailCleared(t *testing.T) {
	t.Parallel()

	srcs := checkGenerated(t, "tail", map[string]string{
		"m.go": `package m

import (
	"sync"
	"unsafe"
)

type Elem struct {
	N    int
	Name string
}

type Bulk struct {
	P []*int
	E []Elem
	B []byte

	N0, N1, N2, N3, N4, N5, N6, N7, N8, N9, N10, N11, N12, N13, N14, N15 uint8
}

type Fieldwise struct {
	Mu sync.Mutex
	P  []*int
	E  []Elem
	B  []byte
	D  []struct{ W, H int }
	Q  [][2]uintptr
	U  [][1]unsafe.Pointer
}
`,
		"m_test.go": `package m

import "testing"

func TestReset(t *testing.T) {
	x := 1
	p := []*int{&x, &x}
	e := []Elem{{1, "a"}, {2, "b"}}

	b := Bulk{P: p[:1], E: e[:1], B: []byte("ab")}
	b.Reset()

	if p[0] != nil || p[1] != nil || e[1] != (Elem{}) || len(b.P) != 0 || cap(b.P) != 2 {
		t.Fatalf("Bulk: %v %v, want the arrays cleared to their capacity", p, e)
	}

	p, e = []*int{&x, &x}, []Elem{{1, "a"}, {2, "b"}}

	var f Fieldwise
	f.P, f.E = p[:1], e[:1]
	f.Reset()

	if p[0] != nil || p[1] != nil || e[1] != (Elem{}) || len(f.P) != 0 || cap(f.P) != 2 {
		t.Fatalf("Fieldwise: %v %v, want the arrays cleared to their capacity", p, e)
	}
}
`,
	}, "Bulk", "Fieldwise")

	for typeName, src := range srcs {
		if !strings.Contains(src, "clear(r.P[:cap(r.P)])") || !strings.Contains(src, "clear(r.B)\n") {
			t.Errorf("%s: want P cleared to its capacity, B to its length:\n%s", typeName, src)
		}
	}

	for _, want := range []string{"clear(r.D)\n", "clear(r.Q)\n", "clear(r.U[:cap(r.U)])"} {
		if !strings.Contains(srcs["Fieldwise"], want) {
			t.Errorf("Fieldwise lacks %q:\n%s", want, srcs["Fieldwise"])
		}
	}

	if !strings.Contains(srcs["Bulk"], "*r = Bulk{}") {
		t.Errorf("Bulk is not reset in one assignment:\n%s", srcs["Bulk"])
	}
}

// TestBlankAndUnsafe: blank fields (padding) cannot be referred to and are
// left out; an unsafe.Pointer is set to nil; a field type that names several
// packages imports all of them.
func TestBlankAndUnsafe(t *testing.T) {
	t.Parallel()

	srcs := checkGenerated(t, "blank", map[string]string{
		"m.go": `package m

import (
	"net/netip"
	"sync"
	"time"
	"unsafe"
)

type Pad struct{ n int }

func (p *Pad) Reset() { p.n = 0 }

type Bulk struct {
	_ [8]byte ` + "`" + `reset:"-"` + "`" + `
	_ Pad
	P unsafe.Pointer
	N int
}

type Fieldwise struct {
	Mu sync.Mutex
	_  [56]byte
	_  Pad
	P  unsafe.Pointer
	A  [1]map[netip.Addr]time.Time
}
`,
		"m_test.go": `package m

import (
	"net/netip"
	"testing"
	"time"
	"unsafe"
)

func TestReset(t *testing.T) {
	x := 1

	b := Bulk{P: unsafe.Pointer(&x), N: 1}
	b.Reset()

	if b.P != nil || b.N != 0 {
		t.Fatalf("Bulk = %+v, want zero", b)
	}

	var f Fieldwise
	f.P = unsafe.Pointer(&x)
	f.A[0] = map[netip.Addr]time.Time{}
	f.Reset()

	if f.P != nil || f.A[0] != nil {
		t.Fatal("Fieldwise: P and A are not zero")
	}
}
`,
	}, "Bulk", "Fieldwise")

	for typeName, src := range srcs {
		if strings.Contains(src, "r._") {
			t.Errorf("%s refers to a blank field:\n%s", typeName, src)
		}
	}

	if !strings.Contains(srcs["Bulk"], "*r = Bulk{}") {
		t.Errorf("Bulk is not reset in one assignment:\n%s", srcs["Bulk"])
	}
}
