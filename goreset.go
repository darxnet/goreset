package goreset

import (
	"bytes"
	"fmt"
	"go/format"
	"go/types"
	"reflect"
	"slices"
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
	Import string
	Field  string
	Code   string // field-wise reset
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

	var (
		imports []string
		// tagImports come from `reset:"import=..."` tags. Field-wise code names
		// field types (`r.Addr = netip.Addr{}`), so it needs their imports; bulk
		// code only does `*r = T{}` and keep/restore of r's fields, so those
		// imports would be unused. It still needs the ones a `value=` expression
		// asks for through import=, e.g. `reset:"value=consts.Default,import=example.com/consts"`.
		tagImports []string
		fields     []fieldInfo
	)

	for i := range structType.NumFields() {
		f := structType.Field(i)
		tag := reflect.StructTag(structType.Tag(i)).Get("reset")

		if tag == "-" {
			fields = append(fields, fieldInfo{
				Field:     f.Name(),
				Code:      fmt.Sprintf("// r.%s skipped via tag", f.Name()),
				Keep:      fmt.Sprintf("keep%s := r.%s", f.Name(), f.Name()),
				Restore:   fmt.Sprintf("r.%s = keep%s", f.Name(), f.Name()),
				BulkExtra: costKeep,
			})

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
			fields = append(fields, callCode(f, opts.call))

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
			fields = append(fields, p.resetCode(f, opts.release))
		}
	}

	for i := range fields {
		if f := &fields[i]; f.Import != "" {
			imports = append(imports, f.Import)
		}
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

	structSize := p.sizes.Sizeof(obj.Type())
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

	data := struct {
		PackageName string
		TypeName    string
		Fields      []fieldInfo
		Imports     []string
		Reasons     []string
		Bulk        bool
	}{
		PackageName: p.pkg.Name(),
		TypeName:    typeName,
		Fields:      fields,
		Imports:     imports,
		Reasons:     reasons,
		Bulk:        bulk,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
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

func (p *Package) resetCode(f *types.Var, release string) fieldInfo {
	var (
		info       fieldInfo
		importPath string
	)

	qualifier := func(other *types.Package) string {
		if p.pkg == other {
			return ""
		}

		importPath = other.Path()

		return other.Name()
	}

	info.Field = f.Name()
	typeName := types.TypeString(f.Type(), qualifier)

	info.Weight = costStore // the common case: one statement, nothing to keep

	if strings.HasPrefix(typeName, "atomic.") {
		switch {
		case strings.HasPrefix(typeName, "atomic.Pointer"):
			info.Code = fmt.Sprintf("r.%s.Store(nil)", info.Field)
		case typeName == "atomic.Bool":
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

	if hasResetMethod(f.Type()) {
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
		info.BulkExtra = costKeep + 2*units(p.sizes.Sizeof(f.Type()))

		return info
	}

	switch ut := f.Type().Underlying().(type) {
	case *types.Slice:
		info.Code = fmt.Sprintf("clear(r.%s)\nr.%s = r.%s[:0]", info.Field, info.Field, info.Field)
		info.Keep = fmt.Sprintf("clear(r.%s)\n\tkeep%s := r.%s[:0]", info.Field, info.Field, info.Field)
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
		default: // string
			info.Code = fmt.Sprintf("r.%s = \"\"", info.Field)
		}

		return info
	}

	// arrays and structs without Reset: assign the zero value, a memclr of the field
	info.Import = importPath
	info.Code = fmt.Sprintf("r.%s = %s{}", info.Field, typeName)
	info.Weight = units(p.sizes.Sizeof(f.Type()))

	return info
}

// units is the cost of zeroing or copying size bytes in bulk.
func units(size int64) int {
	return max(1, int((size+bytesPerUnit-1)/bytesPerUnit))
}

func callCode(f *types.Var, method string) fieldInfo {
	info := fieldInfo{Field: f.Name(), Weight: costStore, BulkExtra: costKeep}
	info.Keep = fmt.Sprintf("keep%s := r.%s", info.Field, info.Field)

	if _, ok := f.Type().Underlying().(*types.Pointer); ok {
		info.Code = fmt.Sprintf("if r.%s != nil { r.%s.%s() }", info.Field, info.Field, method)
	} else {
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

func hasResetMethod(t types.Type) bool {
	meth, _, _ := types.LookupFieldOrMethod(t, true, nil, "Reset")
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
	"{{.}}"
{{- end}}
)
{{- end}}

{{- if .Bulk}}

// Reset zeroes r in one assignment and gives the slices their arrays back.
func (r *{{.TypeName}}) Reset() {
{{- range .Fields}}
{{- if .Keep}}
	{{.Keep}}
{{- end}}
{{- end}}

	*r = {{.TypeName}}{}

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
func (r *{{.TypeName}}) Reset() {
{{- range .Fields}}
	{{.Code}}
{{- end}}
}
{{- end}}
`))
