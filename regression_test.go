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
	C  Resetter `+"`"+`reset:"call=Reset"`+"`"+` // kept: called when not nil
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
		t.Fatalf("Fieldwise = %v %v %v, want nil, nil, kept", f.H, f.R, f.C)
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
