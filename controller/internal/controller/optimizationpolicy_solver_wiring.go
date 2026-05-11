package controller

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/controller/internal/optimizer"
)

// solvePlacement runs the ILP solver and returns the chosen placement assignment.
//
// Contract:
// - Always attempts to call the solver (so controller really exercises optimizer pipeline).
// - Returns a non-nil map on success (may be empty when no replicas).
func (r *OptimizationPolicyReconciler) solvePlacement(ctx context.Context, pol *corev1alpha1.OptimizationPolicy, p optimizer.Problem) (map[optimizer.ReplicaKey]optimizer.ClusterNodeID, error) {
	start := time.Now()
	solver := optimizer.NewORToolsSolver()
	opts := optimizer.SolveOptions{}
	if r.SolverTimeout > 0 {
		opts.Timeout = r.SolverTimeout
	}
	res, err := optimizer.SolveProblem(ctx, solver, p, opts)
	if err != nil {
		// Surface variable scale limit as a first-class, user-actionable error.
		// This is a BuildILPModel validation error (not a solver runtime error).
		var tooMany *optimizer.ErrTooManyVariables
		if errors.As(err, &tooMany) {
			return nil, err
		}
		if errors.Is(err, optimizer.ErrSolverNotImplemented) {
			// Keep controller unblocked during incremental integration.
			log.FromContext(ctx).Info("ILP solver disabled; skip placement optimization", "policy", client.ObjectKeyFromObject(pol), "elapsed", time.Since(start).String())
			// Important: if we return an empty assignment, defaultEvaluate can't compute moves/summary
			// and may skip creating a plan in some paths. Use current placement as a stable fallback
			// so we still generate a plan (typically with 0 moves) and keep controller behavior testable.
			fallback := map[optimizer.ReplicaKey]optimizer.ClusterNodeID{}
			if pol != nil {
				for _, loc := range pol.Status.CurrentLayout {
					if !loc.Stable {
						continue
					}
					rk := optimizer.ReplicaKey{Namespace: loc.Namespace, Name: loc.Name, ReplicaIndex: loc.ReplicaIndex}
					fallback[rk] = optimizer.ClusterNodeID{ClusterID: loc.ClusterId, NodeName: loc.NodeName}
				}
			}
			return fallback, nil
		}
		return nil, err
	}
	if res == nil {
		log.FromContext(ctx).Info("ILP solver returned nil result", "policy", client.ObjectKeyFromObject(pol), "elapsed", time.Since(start).String())
		return map[optimizer.ReplicaKey]optimizer.ClusterNodeID{}, nil
	}
	if res.Assignment == nil {
		log.FromContext(ctx).Info("ILP solver returned empty assignment", "policy", client.ObjectKeyFromObject(pol), "status", res.Status, "elapsed", time.Since(start).String())
		return map[optimizer.ReplicaKey]optimizer.ClusterNodeID{}, nil
	}
	log.FromContext(ctx).Info("ILP solver finished", "policy", client.ObjectKeyFromObject(pol), "status", res.Status, "assigned", len(res.Assignment), "elapsed", time.Since(start).String())
	return res.Assignment, nil
}

// computeImprovementPct is a minimal M3 wiring: call an ILP solver and translate its output
// into a single improvement percent metric.
//
// Current contract:
// - Always attempts to call the solver (so controller really exercises optimizer pipeline).
// - Returns (improvementPct, source, error)
// - If solver returns no feasible improvement or empty result, we return 0.
//
// Notes:
// - This is intentionally small; richer plan generation and objective plugins are later milestones.
func (r *OptimizationPolicyReconciler) computeImprovementPct(ctx context.Context, pol *corev1alpha1.OptimizationPolicy) (float64, string, error) {
	// Keep improvementPct consistent with the Conservative threshold decision.
	// Source of truth for improvement is plan summary's score delta:
	// improvementPct = max(0, (currentScore-expectedScore)/max(|currentScore|,eps) * 100)
	// (lower score is better; we treat it as a minimization objective).
	if pol == nil || len(pol.Status.CurrentLayout) == 0 {
		return 0, "summary", nil
	}

	stableReplicas := make([]optimizer.ReplicaKey, 0)
	currentPlacement := make(map[optimizer.ReplicaKey]optimizer.ClusterNodeID)
	for _, loc := range pol.Status.CurrentLayout {
		if !loc.Stable {
			continue
		}
		rk := optimizer.ReplicaKey{Namespace: loc.Namespace, Name: loc.Name, ReplicaIndex: loc.ReplicaIndex}
		stableReplicas = append(stableReplicas, rk)
		currentPlacement[rk] = optimizer.ClusterNodeID{ClusterID: loc.ClusterId, NodeName: loc.NodeName}
	}
	if len(stableReplicas) == 0 {
		return 0, "summary", nil
	}

	// Candidate nodes: same rule as defaultEvaluate (Cluster CR status.nodes Ready-only),
	// with a safe fallback to current placement.
	candidateNodes := make([]optimizer.ClusterNodeID, 0)
	{
		var clusters corev1alpha1.ClusterList
		if err := r.List(ctx, &clusters); err != nil {
			return 0, "summary", err
		}
		for i := range clusters.Items {
			c := &clusters.Items[i]
			if c.Status.Phase != corev1alpha1.ClusterPhaseReady {
				continue
			}
			clusterID := c.Name
			for _, ns := range c.Status.Nodes {
				if !ns.Ready || ns.Name == "" {
					continue
				}
				candidateNodes = append(candidateNodes, optimizer.ClusterNodeID{ClusterID: clusterID, NodeName: ns.Name})
			}
		}
	}
	if len(candidateNodes) == 0 {
		candSet := map[optimizer.ClusterNodeID]struct{}{}
		for _, n := range currentPlacement {
			candSet[n] = struct{}{}
		}
		for n := range candSet {
			candidateNodes = append(candidateNodes, n)
		}
	}

	nodeCtxs, err := r.buildNodeContexts(ctx, candidateNodes)
	if err != nil {
		return 0, "summary", err
	}

	// ReplicaRequests: same rule as defaultEvaluate (derive from GD template container requests; fallback CPU=1000m, Mem=1024Mi).
	repReq := map[optimizer.ReplicaKey]optimizer.ResourceQuantity{}
	{
		selector := labels.Everything()
		if pol.Spec.TargetSelector != nil {
			s, selErr := metav1.LabelSelectorAsSelector(pol.Spec.TargetSelector)
			if selErr == nil {
				selector = s
			}
		}
		var gds corev1alpha1.GlobalDeploymentList
		if err := r.List(ctx, &gds, client.MatchingLabelsSelector{Selector: selector}); err != nil {
			return 0, "summary", err
		}
		gdCPU := map[string]int64{}
		gdMemMi := map[string]int64{}
		for i := range gds.Items {
			gd := &gds.Items[i]
			milli := int64(0)
			memMi := int64(0)
			var dep appsv1.Deployment
			if err := json.Unmarshal(gd.Spec.Template.Raw, &dep); err == nil {
				for _, c := range dep.Spec.Template.Spec.Containers {
					if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
						milli += q.MilliValue()
					}
					if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
						memMi += q.Value() / (1024 * 1024)
					}
				}
			}
			if milli <= 0 {
				milli = 1000
			}
			if memMi <= 0 {
				memMi = 1024
			}
			gdCPU[gd.Namespace+"/"+gd.Name] = milli
			gdMemMi[gd.Namespace+"/"+gd.Name] = memMi
		}
		for _, rk := range stableReplicas {
			milli, ok := gdCPU[rk.Namespace+"/"+rk.Name]
			if !ok {
				milli = 1000
			}
			memMi, ok := gdMemMi[rk.Namespace+"/"+rk.Name]
			if !ok {
				memMi = 1024
			}
			repReq[rk] = optimizer.ResourceQuantity{MilliCPU: milli, MemoryMi: memMi}
		}
	}

	nodeCap := map[optimizer.ClusterNodeID]optimizer.ResourceQuantity{}
	for _, nc := range nodeCtxs {
		cap := nc.CPUAllocatableMilli
		if cap <= 0 {
			cap = 4000
		}
		memCap := nc.MemoryAllocatableMi
		if memCap <= 0 {
			memCap = 8192
		}
		nodeCap[nc.ID] = optimizer.ResourceQuantity{MilliCPU: cap, MemoryMi: memCap}
	}

	goals := make([]optimizer.WeightedGoal, 0, len(pol.Spec.OptimizationGoals))
	for _, g := range pol.Spec.OptimizationGoals {
		if g.Weight == 0 {
			continue
		}
		goals = append(goals, optimizer.WeightedGoal{Type: g.Type, Weight: g.Weight, SourceCity: g.SourceCity, TopologyRef: g.TopologyRef})
	}

	profiles, err := loadProfilesFromConfigMaps(ctx, r.Client, r.getProfilesNamespace())
	if err != nil {
		return 0, "summary", err
	}

	// Communication optional inputs.
	var (
		deps           map[optimizer.NamespacedName][]optimizer.NamespacedName
		replicaService map[optimizer.ReplicaKey]optimizer.NamespacedName
		nodeRegion     map[optimizer.ClusterNodeID]string
		regionLat      map[string]map[string]float64
	)
	hasComm := false
	topologyRef := ""
	for _, g := range goals {
		if g.Type == "Communication" && g.Weight != 0 {
			hasComm = true
			topologyRef = g.TopologyRef
			break
		}
	}
	if hasComm {
		deps, err = loadTopologyFromConfigMap(ctx, r.Client, r.getProfilesNamespace(), topologyRef)
		if err != nil {
			return 0, "summary", err
		}
		svcByGD := map[string]optimizer.NamespacedName{}
		{
			selector := labels.Everything()
			if pol.Spec.TargetSelector != nil {
				s, selErr := metav1.LabelSelectorAsSelector(pol.Spec.TargetSelector)
				if selErr == nil {
					selector = s
				}
			}
			var gds corev1alpha1.GlobalDeploymentList
			if listErr := r.List(ctx, &gds, client.MatchingLabelsSelector{Selector: selector}); listErr != nil {
				return 0, "summary", listErr
			}
			for i := range gds.Items {
				gd := &gds.Items[i]
				name := ""
				if gd.Labels != nil {
					if v := gd.Labels["kubex.io/service"]; v != "" {
						name = v
					} else if v := gd.Labels["app"]; v != "" {
						name = v
					}
				}
				if name == "" {
					name = gd.Name
				}
				svcByGD[gd.Namespace+"/"+gd.Name] = optimizer.NamespacedName{Namespace: gd.Namespace, Name: name}
			}
		}
		replicaService = make(map[optimizer.ReplicaKey]optimizer.NamespacedName, len(stableReplicas))
		for _, rk := range stableReplicas {
			if s, ok := svcByGD[rk.Namespace+"/"+rk.Name]; ok {
				replicaService[rk] = s
			} else {
				replicaService[rk] = optimizer.NamespacedName{Namespace: rk.Namespace, Name: rk.Name}
			}
		}
		nodeRegion = make(map[optimizer.ClusterNodeID]string, len(nodeCtxs))
		for _, nc := range nodeCtxs {
			if nc.Region != "" {
				nodeRegion[nc.ID] = nc.Region
			}
		}
		regionLat = profiles.RegionLatencyMs
	}

	p := optimizer.Problem{
		StableReplicas:   stableReplicas,
		CandidateNodes:   candidateNodes,
		CurrentPlacement: currentPlacement,
		NodeContexts:     nodeCtxs,
		ReplicaRequests:  repReq,
		NodeCapacities:   nodeCap,
		RequireCPU:       true,
		RequireMemory:    true,
		Objective: &optimizer.ProblemObjective{
			Goals:               goals,
			InstancePrice:       profiles.InstancePrice,
			CityRegionLatencyMs: profiles.CityRegionLatencyMs,
			Dependencies:        deps,
			RegionLatencyMs:     regionLat,
			ReplicaService:      replicaService,
			NodeRegion:          nodeRegion,
			InstancePower:       profiles.InstancePower,
		},
	}

	assignment, err := r.solvePlacement(ctx, pol, p)
	if err != nil {
		return 0, "summary", err
	}

	pluginCtx := optimizer.PluginContext{Replicas: stableReplicas, Nodes: nodeCtxs, CurrentPlacement: currentPlacement, ReplicaRequests: repReq}
	// Inject controller logger for optimizer-level diagnostics.
	diagCtx := context.WithValue(ctx, optimizer.OptimizerDiagLoggerKey{}, log.FromContext(ctx))
	metrics, _, err := optimizer.ComputePlanFromGoals(diagCtx, optimizer.ObjectiveInputs{
		Goals:               goals,
		Ctx:                 pluginCtx,
		InstancePrice:       profiles.InstancePrice,
		InstancePower:       profiles.InstancePower,
		CityRegionLatencyMs: profiles.CityRegionLatencyMs,
		Dependencies:        deps,
		RegionLatencyMs:     regionLat,
		ReplicaService:      replicaService,
		NodeRegion:          nodeRegion,
	}, optimizer.ExpectedPlacement(assignment))
	if err != nil {
		return 0, "summary", err
	}

	// Convert score improvement into pct for threshold gate.
	cur := metrics.CurrentScore
	exp := metrics.ExpectedScore
	delta := cur - exp
	if delta <= 0 {
		return 0, "summary", nil
	}
	den := math.Max(math.Abs(cur), 1e-9)
	return math.Max(0, (delta/den)*100.0), "summary", nil
}
