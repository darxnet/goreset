package wide_test

import (
	"testing"

	"github.com/darxnet/goreset/internal/cases/wide"
)

func TestSlicyReset(t *testing.T) {
	t.Parallel()

	s := wide.Slicy{A: []byte("a"), H: make([]byte, 3, 32)}

	s.Reset()

	if len(s.A) != 0 || len(s.H) != 0 || cap(s.H) != 32 {
		t.Fatalf("Reset = %+v, want empty slices with their arrays", s)
	}
}
