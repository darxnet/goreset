// Package goreset generates Reset methods for structs that are reused
// through a pool.
//
// A generated Reset puts the value back to its zero state without giving up
// the memory the value owns: slices keep their backing arrays (cleared, length
// zero), nested values with their own Reset are reset through it, and struct
// tags choose a different treatment per field:
//
//	Buf   []byte       `reset:"-"`                 // left as it is
//	Timer *time.Timer  `reset:"call=Stop"`         // kept, method called
//	Flags uint32       `reset:"value=^uint32(0)"`  // set to EXPR, not zero
//	Conn  *Conn        `reset:"release=PutConn"`   // PutConn(r.Conn); r.Conn = nil
//
// Fields of sync types are never touched, and a struct holding one is reset
// field by field. Any other struct is reset either in one assignment
// (`*r = T{}`) with the kept fields restored after it, or field by field,
// whichever a cost model built on the field sizes rates cheaper; the
// generated comment says which and why.
//
// The usual way to run it is a go:generate directive next to the type:
//
//	//go:generate go tool goreset -type=Request
//
// which writes request_reset.gen.go in the same package. [Load] and
// [Package.Generate] expose the generator to other tools.
package goreset
