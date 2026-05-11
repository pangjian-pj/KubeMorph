//go:build !kubex_ortools

package optimizer

import (
	"context"
	"fmt"
)

// ORToolsSolver is a stub implementation used when the optional OR-Tools backend
// isn't enabled (build tag kubex_ortools).
//
// This keeps the repository IDE-friendly: gopls won't try to compile cgo+OR-Tools
// unless explicitly requested.
//
//nolint:unused
type ORToolsSolver struct{}

func NewORToolsSolver() *ORToolsSolver { return &ORToolsSolver{} }

func (s *ORToolsSolver) Solve(ctx context.Context, p Problem) (*SolveResult, error) {
	return nil, fmt.Errorf("ORTools backend is disabled; rebuild with -tags=kubex_ortools")
}
