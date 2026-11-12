package goreset

import (
	"bytes"
	"fmt"
	"go/format"
	"go/types"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"text/template"
)

// The cost model behind the choice of mode, in units of one field store.
//
// Bulk mode zeroes the whole struct in one memclr and then puts the kept
// fields back; field-wise mode writes each field on its own. Zeroing or
// copying memory in bulk moves bytesPerUnit bytes for the cost of one field
// store; the other constants count the statements each kind of field needs.
// The constants were fitted on two real structs (one of 700 bytes and 95
// fields where both modes measured equal, one of 2400 bytes and 74 fields
// where field-wise was 2.5x faster) and are a model, not a measurement.
const (
	bytesPerUnit = 16 // memclr / memmove throughput relative to one field store

	costStore   = 1 // r.F = 0, r.F = nil, r.F.Store(0), a Reset() call
	costSlice   = 2 // clear(r.F); r.F = r.F[:0]
	costKeep    = 2 // keepF := r.F ... r.F = keepF, around the bulk assignment
	costSliceBK = 3 // clear(r.F); keepF := r.F[:0] ... r.F = keepF
)

// FileName is the file Generate's output is meant for: <type>_reset.gen.go, lower case.
func FileName(typeName string) string {
	return strings.ToLower(typeName) + "_reset.gen.go"
}

type fieldInfo struct {
	// Imports are the paths of the packages Code names.
	Imports []string
	Field   string
	Code    string // field-wise reset
	// bulk mode: Keep runs before `*r = T{}`, Restore after. Empty Keep with
	// empty Restore means the bulk zeroing already did the right thing.
	Keep    string
	Restore string
	// Weight is the field's cost in field-wise mode; BulkExtra what it adds to
	// bulk mode on top of the memclr (keep and restore around the assignment).
	Weight    int
	BulkExtra int
	// Forbidden marks a field that cannot be kept across `*r = T{}` at all (a
	// lock): the type then uses field-wise mode whatever the costs, and Reason
	// says why in the generated comment.
	Forbidden bool
	Reason    string
	// Hint says what would let the type keep bulk mode despite this field.
	Hint string
}

// Result is what Analyze found out about a type.
type Result struct {
	// Source is the generated file, as Generate returns it.
	Source []byte
	// Hints say why Reset is field by field and how it could be one
	// assignment instead. Empty when Bulk.
	Hints []string
	// BulkCost and FieldwiseCost are the model's estimates for the two modes,
	// in units of one field store; the cheaper one is generated.
	BulkCost      int
	FieldwiseCost int
	// Bulk reports whether Reset is one assignment with the kept fields
	// restored after it (true), or one statement per field (false).
	Bulk bool
}

// Generate returns the gofmt-ed source of a file declaring `func (r *T) Reset()`
// for the struct type named typeName.
func (p *Package) Generate(typeName string) ([]byte, error) {
	res, err := p.Analyze(typeName)

	return res.Source, err
}

// Analyze is Generate with the findings behind the output: the mode, the
// costs the model gave the two modes, and for field-wise mode, why.
func (p *Package) Analyze(typeName string) (Result, error) {
	var res Result

	obj := p.pkg.Scope().Lookup(typeName)
	if obj == nil {
		return res, fmt.Errorf("goreset: type %s not found in package %s", typeName, p.pkg.Name())
	}

	structType, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return res, fmt.Errorf("goreset: %s is not a struct", typeName)
	}

	typeExpr, sized, err := p.generic(obj, structType)
	if err != nil {
		return res, err
	}

	var (
		imports []string
		// tagImports come from `reset:"import=..."` tags. Field-wise code names
		// field types (`r.Addr = netip.Addr{}`), so it needs their imports; bulk
		// code only does `*r = T{}` and keep/restore of r's fields, so those
		// imports would be unused. It still needs the ones a `value=` expression
		// asks for through import=, e.g. `reset:"value=consts.Default,import=example.com/consts"`.
		tagImports []string
		fields     []fieldInfo
		names      = importNames{byPath: map[string]string{}, byName: map[string]string{}, own: map[string]string{}}
	)

	// a value= expression names its import= package by the package's own
	// name, so those names are taken first; field types get the rest
	for i := range structType.NumFields() {
		if imp := parseTag(reflect.StructTag(structType.Tag(i)).Get("reset")).importPath; imp != "" {
			names.name(imp, path.Base(imp))
		}
	}

	for i := range structType.NumFields() {
		f := structType.Field(i)
		if f.Name() == "_" {
			continue // padding: it cannot be referred to, and nothing reads it
		}

		tag := reflect.StructTag(structType.Tag(i)).Get("reset")

		if tag == "-" {
			info := fieldInfo{
				Field:     f.Name(),
				Code:      fmt.Sprintf("// r.%s skipped via tag", f.Name()),
				Keep:      fmt.Sprintf("keep%s := r.%s", f.Name(), f.Name()),
				Restore:   fmt.Sprintf("r.%s = keep%s", f.Name(), f.Name()),
				BulkExtra: costKeep,
			}
			p.forbidCopy(&info, f.Type())
			fields = append(fields, info)

			continue
		}

		opts := parseTag(tag)

		if opts.importPath != "" {
			imports = append(imports, opts.importPath)
			tagImports = append(tagImports, opts.importPath)
		}

		switch {
		case opts.call != "":
			// call=Method keeps the field value and calls its method instead,
			// e.g. `reset:"call=Stop"` for a reusable *time.Timer.
			info := callCode(f, opts.call)
			p.forbidCopy(&info, f.Type())
			fields = append(fields, info)

		case opts.value != "":
			// value=EXPR resets the field to EXPR instead of the zero value,
			// e.g. `reset:"value=^uint32(0)"`.
			fields = append(fields, fieldInfo{
				Field:     f.Name(),
				Code:      fmt.Sprintf("r.%s = %s", f.Name(), opts.value),
				Restore:   fmt.Sprintf("r.%s = %s", f.Name(), opts.value),
				Weight:    costStore,
				BulkExtra: costStore,
			})

		default:
			fields = append(fields, p.resetCode(f, sized.Field(i).Type(), opts.release, &names))
		}
	}

	for i := range fields {
		imports = append(imports, fields[i].Imports...)
	}

	slices.SortFunc(fields, func(a, b fieldInfo) int {
		return strings.Compare(a.Field, b.Field)
	})

	// Bulk mode zeroes the struct in one assignment and restores the fields
	// that keep state (slices keep their arrays, tagged fields keep their
	// values). It is used unless a field is cheaper to reset in place.
	// The costs of the two modes; a lock rules bulk mode out whatever they say.
	var (
		reasons   []string
		forbidden bool
		kept      int
	)

	structSize := p.sizes.Sizeof(sized)
	res.BulkCost = units(structSize)

	for i := range fields {
		f := &fields[i]
		res.FieldwiseCost += f.Weight
		res.BulkCost += f.BulkExtra

		if f.BulkExtra != 0 {
			kept++
		}

		if f.Forbidden {
			forbidden = true
			reasons = append(reasons, f.Reason)
			res.Hints = append(res.Hints, f.Hint)
		}
	}

	bulk := !forbidden && res.BulkCost <= res.FieldwiseCost
	res.Bulk = bulk

	if !bulk && !forbidden {
		reasons = append(reasons, fmt.Sprintf("zeroing all %d bytes and restoring %d fields costs %d, "+
			"resetting the %d fields one by one costs %d", structSize, kept, res.BulkCost, len(fields), res.FieldwiseCost))
		res.Hints = append(res.Hints, fmt.Sprintf("%s: one assignment would zero all %d bytes and restore %d fields "+
			"(cost %d) where resetting the %d fields one by one costs %d; fewer slices and kept values, "+
			"or a smaller struct, would turn that around",
			typeName, structSize, kept, res.BulkCost, len(fields), res.FieldwiseCost))
	}

	if bulk {
		// see tagImports: field-type imports are unused by bulk code
		imports = tagImports
	}

	imports = slices.Compact(slices.Sorted(slices.Values(imports)))
	for i, path := range imports {
		imports[i] = names.spec(path)
	}

	data := struct {
		PackageName string
		TypeName    string
		TypeExpr    string
		Fields      []fieldInfo
		Imports     []string
		Reasons     []string
		Bulk        bool
	}{
		PackageName: p.pkg.Name(),
		TypeName:    typeName,
		TypeExpr:    typeExpr,
		Fields:      fields,
		Imports:     imports,
		Reasons:     reasons,
		Bulk:        bulk,
	}

	var buf bytes.Buffer
	if err = tmpl.Execute(&buf, data); err != nil {
		return res, fmt.Errorf("goreset: template: %w", err)
	}

	src, err := format.Source(buf.Bytes())
	if err != nil {
		return res, fmt.Errorf("goreset: format generated code: %w\n%s", err, buf.String())
	}

	res.Source = src

	return res, nil
}

type tagOptions struct {
	importPath string
	release    string
	call       string
	value      string
}

func parseTag(tag string) tagOptions {
	var opts tagOptions

	for part := range strings.SplitSeq(tag, ",") {
		switch {
		case cut(part, "import=", &opts.importPath),
			cut(part, "release=", &opts.release),
			cut(part, "call=", &opts.call),
			cut(part, "value=", &opts.value):
		}
	}

	return opts
}

// cut stores the rest of part after prefix in dst and reports whether it matched.
func cut(part, prefix string, dst *string) bool {
	after, ok := strings.CutPrefix(part, prefix)
	if ok {
		*dst = after
	}

	return ok
}

// resetCode is the code that resets field f; sized is f's type with any type
// parameters replaced, whose size the model can take.
func (p *Package) resetCode(f *types.Var, sized types.Type, release string, names *importNames) fieldInfo {
	var (
		info    fieldInfo
		imports []string
	)

	qualifier := func(other *types.Package) string {
		if p.pkg == other {
			return ""
		}

		imports = append(imports, other.Path())

		return names.name(other.Path(), other.Name())
	}

	info.Field = f.Name()
	typeName := types.TypeString(f.Type(), qualifier)

	info.Weight = costStore // the common case: one statement, nothing to keep

	// A type parameter has no zero literal of its own, whatever its
	// constraint: *new(T) is its zero value. It is not reset through a Reset
	// in its constraint either, as that would panic on a nil pointer.
	if _, ok := types.Unalias(f.Type()).(*types.TypeParam); ok {
		info.Code = fmt.Sprintf("r.%s = *new(%s)", info.Field, typeName)

		return info
	}

	// The sync/atomic types are stored to, but Value is zeroed by the
	// assignment below: its Store panics on nil and on a type other than
	// the one stored before, so it has no zero to store.
	if atomicName := syncAtomicName(f.Type()); atomicName != "" && atomicName != "Value" {
		switch atomicName {
		case "Pointer":
			info.Code = fmt.Sprintf("r.%s.Store(nil)", info.Field)
		case "Bool":
			info.Code = fmt.Sprintf("r.%s.Store(false)", info.Field)
		default:
			info.Code = fmt.Sprintf("r.%s.Store(0)", info.Field)
		}

		// bulk zeroing resets an atomic too: Reset runs without concurrent users
		return info
	}

	if isForbidden(typeName) {
		info.Code = fmt.Sprintf("// r.%s (type %s) skip forbidden", info.Field, typeName)
		info.Weight = 0
		info.Forbidden = true // a lock cannot be kept by value
		info.Reason = fmt.Sprintf("%s is a %s, which must not be copied", info.Field, typeName)
		info.Hint = fmt.Sprintf("%s: a %s cannot be kept across the assignment; make it a *%s "+
			"(tagged reset:\"-\", so it is kept and never reset), or keep the lock outside the pooled value",
			info.Field, typeName, typeName)

		return info
	}

	// An interface is set to nil even when its method set has Reset: the
	// call would panic on a nil value and keep a non-nil one.
	if _, ok := f.Type().Underlying().(*types.Interface); !ok && hasResetMethod(f.Type()) {
		if _, ok := f.Type().(*types.Pointer); ok {
			if release != "" {
				info.Code = fmt.Sprintf("if r.%s != nil { %s(r.%s); r.%s = nil }", info.Field, release, info.Field, info.Field)
				info.Keep = fmt.Sprintf("if r.%s != nil { %s(r.%s) }", info.Field, release, info.Field)
				info.Weight = costSlice // the call and the nil store
				info.BulkExtra = costStore

				return info
			}

			info.Code = fmt.Sprintf("if r.%s != nil { r.%s.Reset() }", info.Field, info.Field)
			info.Keep = fmt.Sprintf("keep%s := r.%s", info.Field, info.Field)
			info.Restore = fmt.Sprintf("r.%s = keep%s\n\t%s", info.Field, info.Field, info.Code)
			info.BulkExtra = costKeep

			return info
		}

		// A value with Reset is kept across the zeroing: copied out and back,
		// then reset in place. Both modes pay the Reset call; bulk mode pays
		// the two copies on top, and the model weighs that against the memclr.
		info.Code = fmt.Sprintf("r.%s.Reset()", info.Field)
		info.Keep = fmt.Sprintf("keep%s := r.%s", info.Field, info.Field)
		info.Restore = fmt.Sprintf("r.%s = keep%s\n\tr.%s.Reset()", info.Field, info.Field, info.Field)
		info.BulkExtra = costKeep + 2*units(p.sizes.Sizeof(sized))
		p.forbidCopy(&info, f.Type())

		return info
	}

	switch ut := f.Type().Underlying().(type) {
	case *types.Slice:
		// Elements past len(s) that hold pointers keep their memory alive
		// until the slice grows over them: clear those up to cap(s).
		cleared := "r." + info.Field
		if hasPointers(ut.Elem()) {
			cleared = fmt.Sprintf("r.%s[:cap(r.%s)]", info.Field, info.Field)
		}

		info.Code = fmt.Sprintf("clear(%s)\nr.%s = r.%s[:0]", cleared, info.Field, info.Field)
		info.Keep = fmt.Sprintf("clear(%s)\n\tkeep%s := r.%s[:0]", cleared, info.Field, info.Field)
		info.Restore = fmt.Sprintf("r.%s = keep%s", info.Field, info.Field)
		info.Weight = costSlice
		info.BulkExtra = costSliceBK

		return info

	case *types.Pointer, *types.Map, *types.Chan, *types.Interface, *types.Signature:
		info.Code = fmt.Sprintf("r.%s = nil", info.Field)

		return info

	case *types.Basic:
		switch {
		case ut.Info()&types.IsNumeric != 0:
			info.Code = fmt.Sprintf("r.%s = 0", info.Field)
		case ut.Info()&types.IsBoolean != 0:
			info.Code = fmt.Sprintf("r.%s = false", info.Field)
		case ut.Info()&types.IsString != 0:
			info.Code = fmt.Sprintf("r.%s = \"\"", info.Field)
		default: // unsafe.Pointer
			info.Code = fmt.Sprintf("r.%s = nil", info.Field)
		}

		return info
	}

	// arrays and structs without Reset: assign the zero value, a memclr of the field
	info.Imports = imports
	info.Code = fmt.Sprintf("r.%s = %s{}", info.Field, typeName)
	info.Weight = units(p.sizes.Sizeof(sized))

	return info
}

// generic returns how the generated code spells the type named by obj, with
// its type parameters (Pair[K, V]), and the struct whose sizes the cost model
// takes: a generic type has none until instantiated, so each type parameter
// is replaced by a pointer-sized placeholder.
func (p *Package) generic(obj types.Object, st *types.Struct) (string, *types.Struct, error) {
	named, ok := obj.Type().(*types.Named)
	if !ok || named.TypeParams().Len() == 0 {
		return obj.Name(), st, nil
	}

	tparams := named.TypeParams()
	names := make([]string, tparams.Len())
	placeholders := make([]types.Type, tparams.Len())

	for i := range tparams.Len() {
		names[i] = tparams.At(i).Obj().Name()
		if names[i] == "r" || names[i] == "_" {
			return "", nil, fmt.Errorf("goreset: %s has a type parameter named %s, "+
				"which the receiver of Reset cannot refer to; rename it", obj.Name(), names[i])
		}

		placeholders[i] = types.Typ[types.UnsafePointer]
	}

	inst, err := types.Instantiate(nil, named, placeholders, false)
	if err != nil {
		return "", nil, fmt.Errorf("goreset: size %s: %w", obj.Name(), err)
	}

	sized, ok := inst.Underlying().(*types.Struct)
	if !ok {
		return "", nil, fmt.Errorf("goreset: %s is not a struct", obj.Name())
	}

	return obj.Name() + "[" + strings.Join(names, ", ") + "]", sized, nil
}

// hasPointers reports whether a value of type t holds a pointer the garbage
// collector follows: a string, a pointer, a slice, a map, a channel, a func,
// an interface, or an array or struct with one of those.
func hasPointers(t types.Type) bool {
	switch ut := t.Underlying().(type) {
	case *types.Basic:
		return ut.Info()&types.IsString != 0 || ut.Kind() == types.UnsafePointer
	case *types.Array:
		return hasPointers(ut.Elem())
	case *types.Struct:
		for field := range ut.Fields() {
			if hasPointers(field.Type()) {
				return true
			}
		}

		return false
	default:
		return true
	}
}

// syncAtomicName is the name of t when t is a type of sync/atomic, "" if not.
func syncAtomicName(t types.Type) string {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "sync/atomic" {
		return ""
	}

	return named.Obj().Name()
}

// importNames gives every package the generated code names a name of its
// own: the package name, or, when another package has it already, that
// name with a number appended (atomic, atomic2).
type importNames struct {
	byPath map[string]string // import path → name in the generated file
	byName map[string]string // name → import path
	own    map[string]string // import path → package name
}

// name returns the name of the package at importPath, whose own name is own.
func (n *importNames) name(importPath, own string) string {
	if name, ok := n.byPath[importPath]; ok {
		return name
	}

	name := own
	for i := 2; n.byName[name] != ""; i++ {
		name = own + strconv.Itoa(i)
	}

	n.byPath[importPath], n.byName[name], n.own[importPath] = name, importPath, own

	return name
}

// spec is the import line for importPath: quoted, with the name in front when it
// is not the package's own.
func (n *importNames) spec(importPath string) string {
	if name := n.byPath[importPath]; name != n.own[importPath] {
		return name + " " + strconv.Quote(importPath)
	}

	return strconv.Quote(importPath)
}

// units is the cost of zeroing or copying size bytes in bulk.
func units(size int64) int {
	return max(1, int((size+bytesPerUnit-1)/bytesPerUnit))
}

func callCode(f *types.Var, method string) fieldInfo {
	info := fieldInfo{Field: f.Name(), Weight: costStore, BulkExtra: costKeep}
	info.Keep = fmt.Sprintf("keep%s := r.%s", info.Field, info.Field)

	switch f.Type().Underlying().(type) {
	case *types.Pointer, *types.Interface:
		info.Code = fmt.Sprintf("if r.%s != nil { r.%s.%s() }", info.Field, info.Field, method)
	default:
		info.Code = fmt.Sprintf("r.%s.%s()", info.Field, method)
	}

	info.Restore = fmt.Sprintf("r.%s = keep%s\n\t%s", info.Field, info.Field, info.Code)

	return info
}

// isForbidden reports whether a field of this type must not be touched: the
// sync types (Mutex, RWMutex, WaitGroup, Once, Pool...) cannot be copied and
// are not meant to be reset.
func isForbidden(typeName string) bool {
	return strings.HasPrefix(typeName, "sync.")
}

// forbidCopy rules bulk mode out for a field that bulk mode keeps by copy
// (reset:"-", call=, a value with Reset) when its value holds a lock.
func (p *Package) forbidCopy(info *fieldInfo, t types.Type) {
	lock := heldLock(t)
	if lock == "" {
		return
	}

	typeName := types.TypeString(t, types.RelativeTo(p.pkg))
	info.Forbidden = true
	info.Reason = fmt.Sprintf("%s holds a %s, which bulk mode would copy", info.Field, lock)
	info.Hint = fmt.Sprintf("%s: a %s holds a %s, so it cannot be kept across the assignment; "+
		"make the field a pointer, or keep the lock outside the pooled value", info.Field, typeName, lock)
}

// heldLock names the lock a value of type t holds, "" if none: a type of
// sync or sync/atomic, or, as go vet's copylocks check sees it, a struct
// whose pointer is a sync.Locker and whose value is not. Pointers,
// interfaces and the like hold nothing; arrays and struct fields are
// searched (a struct cannot hold itself by value, so this ends).
func heldLock(t types.Type) string {
	for {
		arr, ok := t.Underlying().(*types.Array)
		if !ok {
			break
		}

		t = arr.Elem()
	}

	st, ok := t.Underlying().(*types.Struct)
	if !ok {
		return ""
	}

	if named, ok := types.Unalias(t).(*types.Named); ok && named.Obj().Pkg() != nil {
		switch named.Obj().Pkg().Path() {
		case "sync", "sync/atomic":
			return types.TypeString(named, func(p *types.Package) string { return p.Name() })
		}
	}

	if isLocker(types.NewPointer(t)) && !isLocker(t) {
		return types.TypeString(t, func(p *types.Package) string { return p.Name() })
	}

	for field := range st.Fields() {
		if lock := heldLock(field.Type()); lock != "" {
			return lock
		}
	}

	return ""
}

// isLocker reports whether t has Lock() and Unlock() methods.
func isLocker(t types.Type) bool {
	return hasNiladicMethod(t, false, "Lock") && hasNiladicMethod(t, false, "Unlock")
}

// hasResetMethod reports whether r.F.Reset() compiles for a field of type t.
func hasResetMethod(t types.Type) bool {
	return hasNiladicMethod(t, true, "Reset")
}

// hasNiladicMethod reports whether t has a method name without parameters
// and results; addressable counts the methods of *t too.
func hasNiladicMethod(t types.Type, addressable bool, name string) bool {
	meth, _, _ := types.LookupFieldOrMethod(t, addressable, nil, name)
	if meth == nil {
		return false
	}

	sig, ok := meth.Type().(*types.Signature)

	return ok && sig.Params().Len() == 0 && sig.Results().Len() == 0
}

var tmpl = template.Must(template.New("reset").Parse(`// Code generated by goreset; DO NOT EDIT.

package {{.PackageName}}

{{- if .Imports}}

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)
{{- end}}

{{- if .Bulk}}

// Reset zeroes r in one assignment and gives the slices their arrays back.
func (r *{{.TypeExpr}}) Reset() {
{{- range .Fields}}
{{- if .Keep}}
	{{.Keep}}
{{- end}}
{{- end}}

	*r = {{.TypeExpr}}{}

{{- range .Fields}}
{{- if .Restore}}
	{{.Restore}}
{{- end}}
{{- end}}
}
{{- else}}

// Reset zeroes r field by field, not in one assignment:
{{- range .Reasons}}
//   - {{.}}
{{- end}}
func (r *{{.TypeExpr}}) Reset() {
{{- range .Fields}}
	{{.Code}}
{{- end}}
}
{{- end}}
`))
