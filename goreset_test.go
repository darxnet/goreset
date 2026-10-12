package goreset_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/darxnet/goreset"
)

// TestGenerateMatchesCommitted checks the generator against the files it
// produced for the test cases: any change in the output shows up as a diff,
// and the committed files are known to be current.
func TestGenerateMatchesCommitted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dir, typeName, pkgName string
	}{
		{dir: "internal/cases/bulk", typeName: "Request", pkgName: "bulk"},
		{dir: "internal/cases/fieldwise", typeName: "State", pkgName: "fieldwise"},
		{dir: "internal/cases/wide", typeName: "Slicy", pkgName: "wide"},
	}

	for _, tc := range cases {
		t.Run(tc.typeName, func(t *testing.T) {
			t.Parallel()

			pkg, err := goreset.Load(tc.dir)
			if err != nil {
				t.Fatal(err)
			}

			if pkg.Name() != tc.pkgName {
				t.Fatalf("Name() = %q, want %q", pkg.Name(), tc.pkgName)
			}

			got, err := pkg.Generate(tc.typeName)
			if err != nil {
				t.Fatal(err)
			}

			want, err := os.ReadFile(filepath.Join(tc.dir, goreset.FileName(tc.typeName)))
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, want) {
				t.Fatalf("generated code differs from the committed file; run go generate ./...\n"+
					"--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestGenerateModes(t *testing.T) {
	t.Parallel()

	bulk, err := goreset.Load("internal/cases/bulk")
	if err != nil {
		t.Fatal(err)
	}

	src, err := bulk.Generate("Request")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"*r = Request{}",                                     // bulk assignment
		"keepData := r.Data[:0]",                             // slices keep their arrays
		"r.Mask = ^uint32(0)",                                // value=
		"r.Level = consts.DefaultLevel",                      // value= with import=
		`"github.com/darxnet/goreset/internal/cases/consts"`, // the import= path
		"PutBuffer(r.Buf)",                                   // release=
		"r.Timer.Stop()",                                     // call= on a pointer
		"r.Cleared.Clear()",                                  // call= on a value
		"r.Keep = keepKeep",                                  // reset:"-"
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("bulk output lacks %q", want)
		}
	}

	if strings.Contains(string(src), `"time"`) {
		t.Error("bulk output must not import the field types")
	}

	if !strings.Contains(string(src), "keepBigKept := r.BigKept") || !strings.Contains(string(src), "r.BigKept.Reset()") {
		t.Error("a value with Reset is kept across the bulk assignment whatever its size")
	}

	fieldwise, err := goreset.Load("internal/cases/fieldwise")
	if err != nil {
		t.Fatal(err)
	}

	src, err = fieldwise.Generate("State")
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"// r.Mu (type sync.Mutex) skip forbidden",
		"r.Big.Reset()",
		"r.When = time.Time{}",
		"r.Addr = netip.Addr{}",
		`"net/netip"`,
		"r.Count.Store(0)",
		"r.Flag.Store(false)",
		"r.PtrA.Store(nil)",
		"// r.Keep skipped via tag",
		"r.Arr = [2]int{}",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("field-wise output lacks %q", want)
		}
	}

	if strings.Contains(string(src), "*r = State{}") {
		t.Error("a struct with a lock must not be reset in one assignment")
	}

	// the comment names the field that rules bulk mode out, and why
	want := "// Reset zeroes r field by field, not in one assignment:\n" +
		"//   - Mu is a sync.Mutex, which must not be copied\n"
	if !strings.Contains(string(src), want) {
		t.Errorf("field-wise output lacks the reasons comment %q", want)
	}
}

func TestAnalyzeHints(t *testing.T) {
	t.Parallel()

	bulk, err := goreset.Load("internal/cases/bulk")
	if err != nil {
		t.Fatal(err)
	}

	res, err := bulk.Analyze("Request")
	if err != nil {
		t.Fatal(err)
	}

	if !res.Bulk || len(res.Hints) != 0 || len(res.Source) == 0 || res.BulkCost > res.FieldwiseCost {
		t.Fatalf("Analyze(Request) = bulk %v, hints %q, costs %d vs %d", res.Bulk, res.Hints, res.BulkCost, res.FieldwiseCost)
	}

	// no lock, but eight slices and nothing else: the model says field by field
	wide, err := goreset.Load("internal/cases/wide")
	if err != nil {
		t.Fatal(err)
	}

	res, err = wide.Analyze("Slicy")
	if err != nil {
		t.Fatal(err)
	}

	if res.Bulk || res.BulkCost <= res.FieldwiseCost || res.BulkCost != 36 || res.FieldwiseCost != 16 {
		t.Fatalf("Analyze(Slicy) = bulk %v, costs %d vs %d; want field-wise, 36 vs 16",
			res.Bulk, res.BulkCost, res.FieldwiseCost)
	}

	wantHint := "Slicy: one assignment would zero all 192 bytes and restore 8 fields (cost 36) " +
		"where resetting the 8 fields one by one costs 16; fewer slices and kept values, " +
		"or a smaller struct, would turn that around"
	if !slices.Equal(res.Hints, []string{wantHint}) {
		t.Fatalf("Analyze(Slicy) hints = %q, want %q", res.Hints, wantHint)
	}

	if !strings.Contains(string(res.Source), "zeroing all 192 bytes and restoring 8 fields costs 36, "+
		"resetting the 8 fields one by one costs 16") {
		t.Fatalf("Slicy comment lacks the costs:\n%s", res.Source)
	}

	fieldwise, err := goreset.Load("internal/cases/fieldwise")
	if err != nil {
		t.Fatal(err)
	}

	res, err = fieldwise.Analyze("State")
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"Mu: a sync.Mutex cannot be kept across the assignment; make it a *sync.Mutex " +
			"(tagged reset:\"-\", so it is kept and never reset), or keep the lock outside the pooled value",
	}

	if res.Bulk || !slices.Equal(res.Hints, want) {
		t.Fatalf("Analyze(State) = bulk %v, hints %q, want %q", res.Bulk, res.Hints, want)
	}
}

func TestGenerateErrors(t *testing.T) {
	t.Parallel()

	pkg, err := goreset.Load("internal/cases/bulk")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pkg.Generate("Missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Generate(Missing) error = %v, want not found", err)
	}

	if _, err := pkg.Generate("NotAStruct"); err == nil || !strings.Contains(err.Error(), "not a struct") {
		t.Fatalf("Generate(NotAStruct) error = %v, want not a struct", err)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()

	if _, err := goreset.Load(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("Load of a missing directory must fail")
	}

	empty := writeModule(t, "empty", map[string]string{})

	if _, err := goreset.Load(empty); err == nil {
		t.Fatal("Load of a directory without Go files must fail")
	}

	broken := writeModule(t, "broken", map[string]string{
		"dep/dep.go": "package dep\n\nvar X = undefined\n",
		"root.go": "package broken\n\nimport \"example.com/broken/dep\"\n\n" +
			"var _ = dep.X\n\ntype T struct{ Data []byte }\n",
	})

	if _, err := goreset.Load(broken); err == nil || !strings.Contains(err.Error(), "dependency example.com/broken/dep") {
		t.Fatalf("Load with a broken dependency = %v, want its error", err)
	}
}

// TestLoadStaleGeneratedFile checks that a generated file that no longer
// compiles (its field was removed) does not stop the type from being
// generated again.
func TestLoadStaleGeneratedFile(t *testing.T) {
	t.Parallel()

	dir := writeModule(t, "stale", map[string]string{
		"t.go":                "package stale\n\nimport \"time\"\n\ntype T struct {\n\tWhen time.Time\n\tData []byte\n}\n",
		goreset.FileName("T"): "package stale\n\nfunc (r *T) Reset() {\n\tr.Removed = nil\n}\n",
	})

	pkg, err := goreset.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	src, err := pkg.Generate("T")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(src), "Data") || strings.Contains(string(src), "Removed") {
		t.Fatalf("generated from a stale package:\n%s", src)
	}
}

// writeModule creates a throwaway module named example.com/<name> with files in it.
func writeModule(t *testing.T, name string, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()
	files["go.mod"] = "module example.com/" + name + "\n\ngo 1.24\n"

	for file, content := range files {
		path := filepath.Join(dir, file)

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestFileName(t *testing.T) {
	t.Parallel()

	if got := goreset.FileName("LoadVars"); got != "loadvars_reset.gen.go" {
		t.Fatalf("FileName = %q", got)
	}
}
