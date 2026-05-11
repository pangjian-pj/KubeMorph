//go:build kubex_ortools

package optimizer

/*
#cgo darwin,arm64 CXXFLAGS: -std=c++17
#cgo darwin,arm64 CPPFLAGS: -I/opt/homebrew/opt/or-tools/include -I/opt/homebrew/opt/abseil/include -I/opt/homebrew/opt/protobuf/include
#cgo darwin,arm64 LDFLAGS: -L/opt/homebrew/opt/or-tools/lib -lortools

#include <stdlib.h>
#include "ortools_shim.h"

// OR-Tools 的 C++ API 需要用 C++ 编译器进行链接，这里通过一个空的 cgo translation unit
// 强制 Go 构建链走 C++ link，并把 include/lib 路径指向 Homebrew 安装位置。
//
// 注意：我们暂时不直接调用 C++ API，仅先确保能成功编译+链接 libortools。
// 后续在求解器实现里再补齐真正的模型构建与求解。
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"unsafe"
)

// ORToolsSolver 使用本机安装的 OR-Tools（Homebrew: or-tools）作为 ILP 求解后端。
//
// 现阶段先做“可编译可链接”的工业级后端占位，并在 Solve 时给出明确错误，
// 方便在 controller 调谐链路中识别是否真正走到了 OR-Tools。
//
// TODO(M3): 用 C/C++ shim 暴露最小 LP/MIP API，再在这里做完整映射。
//
//	直接在 cgo 里调用 C++ 类会比较痛苦，推荐额外放一层 C wrapper。
//	这将在下一步提交里补齐。
//
// 约束：当前实现保证“工业级依赖已接入且可链接”，但还不会产生可用解。
//
//	这比 brute-force 强，但仍不满足最终 M3 交付（我们会继续完善）。
//
// 说明：之所以先这样落地，是为了先让仓库从“无法编译”恢复到“可编译可测试”，
//
//	再迭代补齐模型求解。
//
// 如果你希望我们一次性完成 shim + 完整求解，也可以，但需要再多读一轮 Problem/Model 细节。
//
//nolint:unused
type ORToolsSolver struct{}

func NewORToolsSolver() *ORToolsSolver { return &ORToolsSolver{} }

func (s *ORToolsSolver) Solve(ctx context.Context, p Problem) (*SolveResult, error) {
	if _, err := BuildILPModel(ctx, p); err != nil {
		return nil, err
	}
	if len(p.StableReplicas) == 0 {
		return &SolveResult{Status: "NoReplicas", Assignment: map[ReplicaKey]ClusterNodeID{}}, nil
	}
	if len(p.CandidateNodes) == 0 {
		return nil, fmt.Errorf("no candidate nodes")
	}

	// Objective coefficients are built from scoring plugins (Cost/Latency/Communication/Energy)
	// plus migration penalty (Migration goal).
	//
	// Contract:
	// - Placement scores are linear terms: sum_i sum_j score_ij * x_ij.
	// - Migration penalty is modeled as an additional linear term on x_ij:
	//   migPenalty_i * (1 - x_i,current). Since sum_j x_ij = 1, this equals
	//   constant + migPenalty_i * (- x_i,current). Dropping constant does not
	//   change argmin, so we add +migPenalty_i to all j and add -migPenalty_i to
	//   the current node variable.
	//
	// If no objective inputs are provided, we fall back to feasibility-first.
	placementScore, migPenalty, err := buildObjectiveCoefficients(ctx, p)
	if err != nil {
		return nil, err
	}

	name := C.CString("kubex_ilp")
	defer C.free(unsafe.Pointer(name))

	solver := C.kubex_ort_new_solver(name)
	if solver == nil {
		return nil, fmt.Errorf("failed to create ortools solver: %s", C.GoString(C.kubex_ort_last_error()))
	}
	defer C.kubex_ort_delete_solver(solver)

	C.kubex_ort_objective_set_minimization(solver)

	// Time limit from ctx deadline if present.
	if dl, ok := ctx.Deadline(); ok {
		ms := time.Until(dl).Milliseconds()
		if ms > 0 {
			C.kubex_ort_set_time_limit_ms(solver, C.int64_t(ms))
		}
	}

	I := p.StableReplicas
	J := p.CandidateNodes

	// x[r][n] in {0,1}
	x := make(map[ReplicaKey]map[ClusterNodeID]C.KubexORTVar, len(I))
	for _, r := range I {
		x[r] = make(map[ClusterNodeID]C.KubexORTVar, len(J))
		for _, n := range J {
			vn := C.CString(fmt.Sprintf("x_%s_%s", r.String(), n.String()))
			v := C.kubex_ort_int_var(solver, 0, 1, vn)
			C.free(unsafe.Pointer(vn))
			if v == nil {
				return nil, fmt.Errorf("failed creating var: %s", C.GoString(C.kubex_ort_last_error()))
			}
			x[r][n] = v
		}
	}

	// Objective: sum_i sum_j coeff_ij * x_ij
	// (Lower is better; objective is minimize)
	if placementScore != nil {
		for _, r := range I {
			for _, n := range J {
				coeff := placementScore[r][n]
				if coeff == 0 {
					continue
				}
				C.kubex_ort_objective_set_coeff(solver, x[r][n], C.double(coeff))
				if msg := C.GoString(C.kubex_ort_last_error()); msg != "" {
					return nil, fmt.Errorf("failed setting objective coeff: %s", msg)
				}
			}
		}
	}

	// Assignment: for each replica r, sum_n x[r][n] == 1
	for _, r := range I {
		c := C.kubex_ort_constraint(solver, 1, 1)
		if c == nil {
			return nil, fmt.Errorf("failed creating constraint: %s", C.GoString(C.kubex_ort_last_error()))
		}
		for _, n := range J {
			C.kubex_ort_constraint_set_coeff(c, x[r][n], 1)
			if msg := C.GoString(C.kubex_ort_last_error()); msg != "" {
				return nil, fmt.Errorf("failed setting coeff: %s", msg)
			}
		}
	}

	// CPU capacity: for each node n, sum_r cpu[r] * x[r][n] <= cap[n]
	if p.RequireCPU {
		for _, n := range J {
			cap, ok := p.NodeCapacities[n]
			if !ok {
				return nil, fmt.Errorf("missing node capacity for %s", n.String())
			}
			c := C.kubex_ort_constraint(solver, C.double(math.Inf(-1)), C.double(cap.MilliCPU))
			if c == nil {
				return nil, fmt.Errorf("failed creating cpu constraint: %s", C.GoString(C.kubex_ort_last_error()))
			}
			for _, r := range I {
				req, ok := p.ReplicaRequests[r]
				if !ok {
					return nil, fmt.Errorf("missing replica request for %s", r.String())
				}
				C.kubex_ort_constraint_set_coeff(c, x[r][n], C.double(req.MilliCPU))
				if msg := C.GoString(C.kubex_ort_last_error()); msg != "" {
					return nil, fmt.Errorf("failed setting cpu coeff: %s", msg)
				}
			}
		}
	}

	// Memory capacity: for each node n, sum_r mem[r] * x[r][n] <= cap[n]
	// Unit: Mi (mebibytes)
	if p.RequireMemory {
		for _, n := range J {
			cap, ok := p.NodeCapacities[n]
			if !ok {
				return nil, fmt.Errorf("missing node capacity for %s", n.String())
			}
			c := C.kubex_ort_constraint(solver, C.double(math.Inf(-1)), C.double(cap.MemoryMi))
			if c == nil {
				return nil, fmt.Errorf("failed creating memory constraint: %s", C.GoString(C.kubex_ort_last_error()))
			}
			for _, r := range I {
				req, ok := p.ReplicaRequests[r]
				if !ok {
					return nil, fmt.Errorf("missing replica request for %s", r.String())
				}
				C.kubex_ort_constraint_set_coeff(c, x[r][n], C.double(req.MemoryMi))
				if msg := C.GoString(C.kubex_ort_last_error()); msg != "" {
					return nil, fmt.Errorf("failed setting memory coeff: %s", msg)
				}
			}
		}
	}

	st := C.kubex_ort_solve(solver)
	switch st {
	case C.KUBEX_ORT_STATUS_OPTIMAL, C.KUBEX_ORT_STATUS_FEASIBLE:
		// ok
	case C.KUBEX_ORT_STATUS_INFEASIBLE:
		return nil, fmt.Errorf("ortools infeasible")
	case C.KUBEX_ORT_STATUS_NOT_SOLVED:
		// If deadline exceeded, propagate ctx error.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("ortools not solved")
	default:
		msg := C.GoString(C.kubex_ort_last_error())
		if msg == "" {
			msg = "unknown"
		}
		return nil, fmt.Errorf("ortools solve failed: %s", msg)
	}

	assignment := make(map[ReplicaKey]ClusterNodeID, len(I))
	for _, r := range I {
		chosen := false
		for _, n := range J {
			val := float64(C.kubex_ort_var_solution_value(x[r][n]))
			if val >= 0.5 {
				assignment[r] = n
				chosen = true
				break
			}
		}
		if !chosen {
			return nil, fmt.Errorf("ortools returned no assignment for %s", r.String())
		}
	}

	_ = migPenalty // kept for debugging/inspection in future (e.g., plan metrics)
	return &SolveResult{Status: "Feasible", Assignment: assignment}, nil
}

func buildObjectiveCoefficients(solveCtx context.Context, p Problem) (map[ReplicaKey]map[ClusterNodeID]float64, map[ReplicaKey]float64, error) {
	if p.Objective == nil {
		return nil, nil, nil
	}
	pctx := PluginContext{
		Replicas:         p.StableReplicas,
		Nodes:            p.NodeContexts,
		ReplicaRequests:  p.ReplicaRequests,
		CurrentPlacement: p.CurrentPlacement,
	}
	out, err := BuildObjective(solveCtx, ObjectiveInputs{
		Goals:               p.Objective.Goals,
		Ctx:                 pctx,
		InstancePrice:       p.Objective.InstancePrice,
		CityRegionLatencyMs: p.Objective.CityRegionLatencyMs,
		Dependencies:        p.Objective.Dependencies,
		RegionLatencyMs:     p.Objective.RegionLatencyMs,
		ReplicaService:      p.Objective.ReplicaService,
		NodeRegion:          p.Objective.NodeRegion,
		InstancePower:       p.Objective.InstancePower,
	})
	if err != nil {
		return nil, nil, err
	}

	coeff := out.PlacementScores
	if coeff == nil {
		return nil, out.MigrationPenalty, nil
	}

	// Apply migration penalty as explained above.
	if len(out.MigrationPenalty) > 0 && p.CurrentPlacement != nil {
		for r, pen := range out.MigrationPenalty {
			if pen == 0 {
				continue
			}
			cur, ok := p.CurrentPlacement[r]
			if !ok {
				continue
			}
			row := coeff[r]
			if row == nil {
				continue
			}
			for n := range row {
				row[n] += pen
			}
			row[cur] -= pen
		}
	}

	return coeff, out.MigrationPenalty, nil
}

// NOTE: Objective wiring lives in this file; Problem carries objective inputs.
