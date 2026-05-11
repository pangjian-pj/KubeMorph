package solver

import "context"

// Model is a placeholder for an optimization model.
//
// M0: we only define enough abstraction so optimizer package can depend on it
// without importing a concrete solver.
//
// M2+: this will be backed by OR-Tools/SCIP.
type Model interface {
	// Solve solves the model and returns a Result.
	Solve(ctx context.Context) (*Result, error)
}

// Result is a placeholder solve result.
type Result struct {
	Status string
}
