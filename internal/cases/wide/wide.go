// Package wide is a goreset test case: no locks, but the cost model picks
// field-wise mode — slices cost a keep and a restore each around one
// assignment, and there is little else for the assignment to zero.
package wide

//go:generate go run ../../../cmd/goreset -type=Slicy

type Slicy struct {
	A, B, C, D, E, F, G, H []byte
}
