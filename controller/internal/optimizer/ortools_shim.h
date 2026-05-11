//go:build kubex_ortools

#pragma once

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// A tiny C API wrapper around OR-Tools Linear Solver (MPSolver).
//
// We avoid exposing C++ types directly to cgo.
// The Go side holds opaque void* handles.

typedef void* KubexORTSolver;
typedef void* KubexORTVar;
typedef void* KubexORTConstraint;

typedef enum KubexORTStatus {
	KUBEX_ORT_STATUS_OPTIMAL = 0,
	KUBEX_ORT_STATUS_FEASIBLE = 1,
	KUBEX_ORT_STATUS_INFEASIBLE = 2,
	KUBEX_ORT_STATUS_ABNORMAL = 3,
	KUBEX_ORT_STATUS_NOT_SOLVED = 4,
	KUBEX_ORT_STATUS_ERROR = 100,
} KubexORTStatus;

// Creates a MIP-capable solver. Tries SCIP first, then CBC.
// Returns NULL on failure.
KubexORTSolver kubex_ort_new_solver(const char* name);

void kubex_ort_delete_solver(KubexORTSolver solver);

KubexORTVar kubex_ort_int_var(KubexORTSolver solver, double lb, double ub, const char* name);

KubexORTConstraint kubex_ort_constraint(KubexORTSolver solver, double lb, double ub);

void kubex_ort_constraint_set_coeff(KubexORTConstraint c, KubexORTVar v, double coeff);

void kubex_ort_objective_set_minimization(KubexORTSolver solver);

// Adds a linear term coeff * v to the objective.
void kubex_ort_objective_set_coeff(KubexORTSolver solver, KubexORTVar v, double coeff);

void kubex_ort_set_time_limit_ms(KubexORTSolver solver, int64_t ms);

KubexORTStatus kubex_ort_solve(KubexORTSolver solver);

double kubex_ort_var_solution_value(KubexORTVar v);

// Returns a short, human readable error message for the last error on this thread.
// The pointer is valid until the next shim call.
const char* kubex_ort_last_error();

#ifdef __cplusplus
} // extern "C"
#endif
