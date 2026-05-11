//go:build kubex_ortools

#include "ortools_shim.h"

#include <cstdint>
#include <string>

#include "absl/time/time.h"
#include "ortools_export.h"

// Some Homebrew or-tools headers (notably generated *.pb.h) refer to OR_PROTO_DLL
// but don't guarantee it's defined via includes. Provide a safe fallback.
#ifndef OR_PROTO_DLL
#define OR_PROTO_DLL ORTOOLS_EXPORT
#endif
#include "ortools/linear_solver/linear_solver.h"

namespace {
thread_local std::string g_last_error;

void set_error(const std::string& s) { g_last_error = s; }

operations_research::MPSolver* as_solver(KubexORTSolver s) {
	return reinterpret_cast<operations_research::MPSolver*>(s);
}
operations_research::MPVariable* as_var(KubexORTVar v) {
	return reinterpret_cast<operations_research::MPVariable*>(v);
}
operations_research::MPConstraint* as_constraint(KubexORTConstraint c) {
	return reinterpret_cast<operations_research::MPConstraint*>(c);
}

KubexORTStatus map_status(operations_research::MPSolver::ResultStatus st) {
	switch (st) {
	case operations_research::MPSolver::OPTIMAL:
		return KUBEX_ORT_STATUS_OPTIMAL;
	case operations_research::MPSolver::FEASIBLE:
		return KUBEX_ORT_STATUS_FEASIBLE;
	case operations_research::MPSolver::INFEASIBLE:
		return KUBEX_ORT_STATUS_INFEASIBLE;
	case operations_research::MPSolver::ABNORMAL:
		return KUBEX_ORT_STATUS_ABNORMAL;
	case operations_research::MPSolver::NOT_SOLVED:
		return KUBEX_ORT_STATUS_NOT_SOLVED;
	default:
		return KUBEX_ORT_STATUS_ERROR;
	}
}
} // namespace

extern "C" {

const char* kubex_ort_last_error() {
	return g_last_error.c_str();
}

KubexORTSolver kubex_ort_new_solver(const char* name) {
	g_last_error.clear();
	if (name == nullptr) {
		set_error("name is null");
		return nullptr;
	}

	// Prefer SCIP if the brewed OR-Tools is built with it; otherwise fall back to CBC.
	operations_research::MPSolver* s = operations_research::MPSolver::CreateSolver("SCIP");
	if (s == nullptr) {
		s = operations_research::MPSolver::CreateSolver("CBC");
	}
	if (s == nullptr) {
		set_error("failed to create MPSolver backend (SCIP/CBC unavailable)");
		return nullptr;
	}
	return reinterpret_cast<KubexORTSolver>(s);
}

void kubex_ort_delete_solver(KubexORTSolver solver) {
	g_last_error.clear();
	if (solver == nullptr) {
		return;
	}
	delete as_solver(solver);
}

KubexORTVar kubex_ort_int_var(KubexORTSolver solver, double lb, double ub, const char* name) {
	g_last_error.clear();
	if (solver == nullptr) {
		set_error("solver is null");
		return nullptr;
	}
	if (name == nullptr) {
		set_error("var name is null");
		return nullptr;
	}
	auto* s = as_solver(solver);
	auto* v = s->MakeIntVar(lb, ub, std::string(name));
	if (v == nullptr) {
		set_error("failed to create int var");
		return nullptr;
	}
	return reinterpret_cast<KubexORTVar>(v);
}

KubexORTConstraint kubex_ort_constraint(KubexORTSolver solver, double lb, double ub) {
	g_last_error.clear();
	if (solver == nullptr) {
		set_error("solver is null");
		return nullptr;
	}
	auto* s = as_solver(solver);
	auto* c = s->MakeRowConstraint(lb, ub);
	if (c == nullptr) {
		set_error("failed to create constraint");
		return nullptr;
	}
	return reinterpret_cast<KubexORTConstraint>(c);
}

void kubex_ort_constraint_set_coeff(KubexORTConstraint c, KubexORTVar v, double coeff) {
	g_last_error.clear();
	if (c == nullptr || v == nullptr) {
		set_error("constraint or var is null");
		return;
	}
	as_constraint(c)->SetCoefficient(as_var(v), coeff);
}

void kubex_ort_objective_set_minimization(KubexORTSolver solver) {
	g_last_error.clear();
	if (solver == nullptr) {
		set_error("solver is null");
		return;
	}
	auto* s = as_solver(solver);
	auto* obj = s->MutableObjective();
	obj->SetMinimization();
}

void kubex_ort_objective_set_coeff(KubexORTSolver solver, KubexORTVar v, double coeff) {
	g_last_error.clear();
	if (solver == nullptr || v == nullptr) {
		set_error("solver or var is null");
		return;
	}
	auto* s = as_solver(solver);
	auto* obj = s->MutableObjective();
	obj->SetCoefficient(as_var(v), coeff);
}

void kubex_ort_set_time_limit_ms(KubexORTSolver solver, int64_t ms) {
	g_last_error.clear();
	if (solver == nullptr) {
		set_error("solver is null");
		return;
	}
	if (ms <= 0) {
		return;
	}
	as_solver(solver)->SetTimeLimit(absl::Milliseconds(ms));
}

KubexORTStatus kubex_ort_solve(KubexORTSolver solver) {
	g_last_error.clear();
	if (solver == nullptr) {
		set_error("solver is null");
		return KUBEX_ORT_STATUS_ERROR;
	}
	auto* s = as_solver(solver);
	auto st = s->Solve();
	return map_status(st);
}

double kubex_ort_var_solution_value(KubexORTVar v) {
	g_last_error.clear();
	if (v == nullptr) {
		set_error("var is null");
		return 0.0;
	}
	return as_var(v)->solution_value();
}

} // extern "C"
