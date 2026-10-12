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

// maxKeepSize is the largest value struct (bytes) that bulk mode keeps by copy:
// a bigger one is cheaper to reset in place, so its type uses field-wise mode.
const maxKeepSize = 64

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
	// NoBulk marks a field whose state cannot be kept cheaply across `*r = T{}`
	// (a lock, or a big value struct with its own Reset): the type then uses
	// field-wise mode, and Reason says why, in the generated comment.
	NoBulk bool
	Reason string
	// Hint says what would let the type keep bulk mode despite this field.
	Hint string
}

// Result is what Analyze found out about a type.
type Result struct {
	// Source is the generated file, as Generate returns it.
	Source []byte
	// Hints say how the type could be reset in one assignment instead of
	// field by field: one per field that rules bulk mode out. Empty when Bulk.
	Hints []string
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

// Analyze is Generate with the findings behind the output: the mode, and for
// field-wise mode, how to get bulk mode back.
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
				Field:   f.Name(),
				Code:    fmt.Sprintf("// r.%s skipped via tag", f.Name()),
				Keep:    fmt.Sprintf("keep%s := r.%s", f.Name(), f.Name()),
				Restore: fmt.Sprintf("r.%s = keep%s", f.Name(), f.Name()),
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
				Field:   f.Name(),
				Code:    fmt.Sprintf("r.%s = %s", f.Name(), opts.value),
				Restore: fmt.Sprintf("r.%s = %s", f.Name(), opts.value),
			})

		default:
			fields = append(fields, p.resetCode(f, opts))
		}
	}

	for _, f := range fields {
		if f.Import != "" {
			imports = append(imports, f.Import)
		}
	}

	slices.SortFunc(fields, func(a, b fieldInfo) int {
		return strings.Compare(a.Field, b.Field)
	})

	// Bulk mode zeroes the struct in one assignment and restores the fields
	// that keep state (slices keep their arrays, tagged fields keep their
	// values). It is used unless a field is cheaper to reset in place.
	var reasons []string

	for _, f := range fields {
		if f.NoBulk {
			reasons = append(reasons, f.Reason)
			res.Hints = append(res.Hints, f.Hint)
		}
	}

	bulk := len(reasons) == 0
	res.Bulk = bulk

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
	// keep copies a value with Reset across the bulk assignment whatever its
	// size, instead of switching the type to field-wise mode: `reset:"keep"`.
	keep bool
}

func parseTag(tag string) tagOptions {
	var opts tagOptions

	for part := range strings.SplitSeq(tag, ",") {
		switch {
		case part == "keep":
			opts.keep = true
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

func (p *Package) resetCode(f *types.Var, opts tagOptions) fieldInfo {
	release := opts.release

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
		info.NoBulk = true // a lock cannot be kept by value
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

				return info
			}

			info.Code = fmt.Sprintf("if r.%s != nil { r.%s.Reset() }", info.Field, info.Field)
			info.Keep = fmt.Sprintf("keep%s := r.%s", info.Field, info.Field)
			info.Restore = fmt.Sprintf("r.%s = keep%s\n\t%s", info.Field, info.Field, info.Code)

			return info
		}

		info.Code = fmt.Sprintf("r.%s.Reset()", info.Field)

		// Keeping a value struct across the zeroing copies it twice; that is
		// fine for small ones, a large one is cheaper to reset in place —
		// unless the field says reset:"keep", and the rest of the struct gets
		// its one assignment.
		if size := p.sizes.Sizeof(f.Type()); size <= maxKeepSize || opts.keep {
			info.Keep = fmt.Sprintf("keep%s := r.%s", info.Field, info.Field)
			info.Restore = fmt.Sprintf("r.%s = keep%s\n\tr.%s.Reset()", info.Field, info.Field, info.Field)
		} else {
			info.NoBulk = true
			info.Reason = fmt.Sprintf("%s (%d bytes, has Reset) is cheaper to reset in place than to copy twice",
				info.Field, size)
			info.Hint = fmt.Sprintf("%s: tag it reset:\"keep\" to copy it across the assignment anyway "+
				"(%d bytes, twice) and reset the rest of the struct in one go, or make it a *%s",
				info.Field, size, typeName)
		}

		return info
	}

	switch ut := f.Type().Underlying().(type) {
	case *types.Slice:
		info.Code = fmt.Sprintf("clear(r.%s)\nr.%s = r.%s[:0]", info.Field, info.Field, info.Field)
		info.Keep = fmt.Sprintf("clear(r.%s)\n\tkeep%s := r.%s[:0]", info.Field, info.Field, info.Field)
		info.Restore = fmt.Sprintf("r.%s = keep%s", info.Field, info.Field)

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

	// arrays and structs without Reset: assign the zero value
	info.Import = importPath
	info.Code = fmt.Sprintf("r.%s = %s{}", info.Field, typeName)

	return info
}

func callCode(f *types.Var, method string) fieldInfo {
	info := fieldInfo{Field: f.Name()}
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
