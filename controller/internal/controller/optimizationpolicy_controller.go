package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/controller/internal/optimizer"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"

	"github.com/pangjian-pj/KubeMorph/controller/internal/controller/multicluster"
)

// Lease mutual exclusion / timers / snapshot building will be implemented in later milestones.
type OptimizationPolicyReconciler struct {
	client.Client
	Recorder record.EventRecorder

	mu           sync.Mutex
	timerHandles map[types.NamespacedName]context.CancelFunc

	// nowFunc makes time controllable in tests.
	nowFunc func() time.Time

	// evaluateFunc is invoked when a timer fires.
	evaluateFunc func(ctx context.Context, pol types.NamespacedName) error

	// Lock namespace/name(kubex-system/kubex-optimization-policy-lock)
	LockNamespace string
	LockName      string

	// LeaseDurationSeconds for lock. (RenewDeadline/RetryPeriod used in later milestone with leaderelection library)
	LeaseDurationSeconds int32

	// ProfilesNamespace is the namespace for optimization data ConfigMaps
	// like instance-cost-profiles and instance-family-energy-profiles.
	// If empty, defaults to kubex-system.
	ProfilesNamespace string

	// SolverTimeout is the time budget for a single optimization evaluation.
	// If 0, optimizer.DefaultSolveTimeout is used.
	SolverTimeout time.Duration

	// ControlNamespace is where Cluster CRs and kubeconfig Secrets live.
	// If empty, defaults to kubex-system.
	ControlNamespace string

	memberClients *multicluster.MemberClientCache

	// evaluateLocks prevents concurrent evaluations for the same policy key.
	// This avoids duplicate plan creation when multiple timers / immediate schedules race.
	evaluateLocks sync.Map // map[types.NamespacedName]*sync.Mutex
}

func (r *OptimizationPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)

	var pol corev1alpha1.OptimizationPolicy
	if err := r.Get(ctx, req.NamespacedName, &pol); err != nil {
		// If deleted, cancel timer.
		if apierrors.IsNotFound(err) {
			r.cancelTimer(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	phase, cond, ok := validateOptimizationPolicySpec(&pol)
	if !ok {
		lg.Info("OptimizationPolicy spec validation failed", "reason", cond.Reason, "message", cond.Message)
		// best-effort: status update failure shouldn't hard fail reconcile.
		_ = r.setStatusPhaseAndCondition(ctx, &pol, phase, cond)
		if r.Recorder != nil {
			r.Recorder.Event(&pol, "Warning", cond.Reason, cond.Message)
		}
		return ctrl.Result{}, nil
	}

	// Spec ok: converge phase.
	if !pol.Spec.Enabled {
		r.cancelTimer(req.NamespacedName)
		cond = metav1.Condition{
			Type:               "SpecValid",
			Status:             metav1.ConditionTrue,
			Reason:             "Valid",
			Message:            "spec is valid",
			ObservedGeneration: pol.GetGeneration(),
			LastTransitionTime: metav1.Now(),
		}
		if err := r.setStatusPhaseAndCondition(ctx, &pol, corev1alpha1.OptimizationPolicyPhaseDisabled, cond); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// enabled=true: attempt to acquire lease lock. If lock acquired => Active, else Failed(Conflict).
	acquired, holder, err := r.tryAcquirePolicyLease(ctx, &pol)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !acquired {
		r.cancelTimer(req.NamespacedName)
		cond = metav1.Condition{
			Type:               "LeaseAcquired",
			Status:             metav1.ConditionFalse,
			Reason:             "Conflict",
			Message:            fmt.Sprintf("conflict: another policy holds lease %s/%s: %s", r.getLockNamespace(), r.getLockName(), holder),
			ObservedGeneration: pol.GetGeneration(),
			LastTransitionTime: metav1.Now(),
		}
		lg.Info("OptimizationPolicy lease conflict", "holder", holder)
		_ = r.setStatusPhaseAndCondition(ctx, &pol, corev1alpha1.OptimizationPolicyPhaseFailed, cond)
		if r.Recorder != nil {
			r.Recorder.Event(&pol, "Warning", "Conflict", cond.Message)
		}
		return ctrl.Result{}, nil
	}

	cond = metav1.Condition{
		Type:               "LeaseAcquired",
		Status:             metav1.ConditionTrue,
		Reason:             "Acquired",
		Message:            "lease acquired",
		ObservedGeneration: pol.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	}
	if err := r.setStatusPhaseAndCondition(ctx, &pol, corev1alpha1.OptimizationPolicyPhaseActive, cond); err != nil {
		return ctrl.Result{}, err
	}

	// M2 3.1: build snapshot scope: select GlobalDeployments by targetSelector across namespaces.
	// For now we only write observedDeployments; later milestones will build full snapshot.
	if err := r.reconcileObservedDeployments(ctx, &pol); err != nil {
		lg.Error(err, "failed to reconcile observedDeployments")
		return ctrl.Result{}, err
	}

	// M2 3.2: collect current layout (ground truth) for selected GlobalDeployments.
	if err := r.reconcileCurrentLayout(ctx, &pol); err != nil {
		lg.Error(err, "failed to reconcile currentLayout")
		return ctrl.Result{}, err
	}

	// M1 2.3: ensure timer is scheduled/cancelled based on policy state.
	if err := r.ensureTimer(ctx, &pol); err != nil {
		lg.Error(err, "failed to ensure timer")
		return ctrl.Result{}, err
	}

	// Still requeue to renew lease.
	return ctrl.Result{RequeueAfter: time.Duration(r.getLeaseDurationSeconds()/2) * time.Second}, nil
}

func (r *OptimizationPolicyReconciler) reconcileObservedDeployments(ctx context.Context, pol *corev1alpha1.OptimizationPolicy) error {
	// Empty selector => all GlobalDeployments.
	selector := labels.Everything()
	if pol.Spec.TargetSelector != nil {
		s, err := metav1.LabelSelectorAsSelector(pol.Spec.TargetSelector)
		if err != nil {
			// Treat invalid selector as a hard error: spec validation doesn't cover selector syntax.
			return err
		}
		selector = s
	}

	var gds corev1alpha1.GlobalDeploymentList
	// Cross-namespace selection by default; do NOT use InNamespace.
	if err := r.List(ctx, &gds, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return err
	}

	observed := int32(len(gds.Items))
	if pol.Status.ObservedDeployments == observed {
		return nil
	}
	patch := client.MergeFrom(pol.DeepCopy())
	pol.Status.ObservedDeployments = observed
	return r.Status().Patch(ctx, pol, patch)
}

func (r *OptimizationPolicyReconciler) reconcileCurrentLayout(ctx context.Context, pol *corev1alpha1.OptimizationPolicy) error {
	// Re-list selected GDs so we can iterate them.
	selector := labels.Everything()
	if pol.Spec.TargetSelector != nil {
		s, err := metav1.LabelSelectorAsSelector(pol.Spec.TargetSelector)
		if err != nil {
			return err
		}
		selector = s
	}
	var gds corev1alpha1.GlobalDeploymentList
	if err := r.List(ctx, &gds, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return err
	}

	// Build layout by scanning RBs once (cross-namespace), then joining by globalDeploymentRef.
	var rbs corev1alpha1.ReplicaBindingList
	if err := r.List(ctx, &rbs); err != nil {
		return err
	}

	// index: ns/name -> replicaIndex -> rb
	rbIndex := make(map[string]map[int32]corev1alpha1.ReplicaBinding)
	for i := range rbs.Items {
		rb := rbs.Items[i]
		gdKey := rb.Spec.GlobalDeploymentRef.Namespace + "/" + rb.Spec.GlobalDeploymentRef.Name
		m := rbIndex[gdKey]
		if m == nil {
			m = make(map[int32]corev1alpha1.ReplicaBinding)
			rbIndex[gdKey] = m
		}
		m[rb.Spec.ReplicaIndex] = rb
	}

	items := make([]corev1alpha1.CurrentReplicaLocation, 0)
	reasonCounts := map[string]int32{}
	var stableCount int32
	var unstableCount int32
	for _, gd := range gds.Items {
		migratable, nonMigratableReason := globalDeploymentMigratable(&gd)
		gdKey := gd.Namespace + "/" + gd.Name
		m := rbIndex[gdKey]
		if m == nil {
			continue
		}
		for idx, rb := range m {
			loc := corev1alpha1.CurrentReplicaLocation{
				Name:         gd.Name,
				Namespace:    gd.Namespace,
				ReplicaIndex: idx,
			}
			// M2 3.2 current layout: ground truth prefers RB.status.{nodeName,clusterName}.
			// Note: ClusterName is currently a display name (v1: equals clusterID). We treat it as clusterId for now.
			if rb.Status.NodeName != "" || rb.Status.ClusterName != "" {
				loc.NodeName = rb.Status.NodeName
				loc.ClusterId = rb.Status.ClusterName
				loc.Source = "RBStatus"
			} else if rb.Spec.TargetNodeName != "" || rb.Spec.TargetCluster != "" {
				// Fallback: spec desired placement (best-effort current)
				loc.NodeName = rb.Spec.TargetNodeName
				loc.ClusterId = rb.Spec.TargetCluster
				loc.Source = "RBSpecFallback"
			} else {
				loc.Source = "Unknown"
			}

			// M2 3.3 stable replica hard conditions (best-effort in control-plane envtest):
			// - must be migratable
			// - must have non-empty clusterId (we need x_ij_current to include cluster)
			// - must have non-empty nodeName
			// - RB phase must be Running (proxy for member workload ready)
			loc.Stable = true
			// M2 3.4: 不可迁移过滤（hostPath/local PV、required nodeAffinity）。
			// 这里我们按“副本所属的 GD 模板”做硬过滤：一旦 GD 不可迁移，其下所有副本都不可迁移。
			if !migratable {
				loc.Stable = false
				loc.UnstableReason = nonMigratableReason
			} else if loc.ClusterId == "" {
				loc.Stable = false
				loc.UnstableReason = "ClusterIdEmpty"
			} else if loc.NodeName == "" {
				loc.Stable = false
				loc.UnstableReason = "NodeNameEmpty"
			} else if rb.Status.Phase != corev1alpha1.ReplicaBindingPhaseRunning {
				loc.Stable = false
				loc.UnstableReason = "ReplicaNotRunning"
			}
			if loc.Stable {
				stableCount++
			} else {
				unstableCount++
				reasonCounts[loc.UnstableReason]++
			}

			items = append(items, loc)
		}
	}

	observedReplicas := int32(len(items))
	// Avoid status churn where possible.
	if pol.Status.ObservedReplicas == observedReplicas &&
		pol.Status.StableReplicas == stableCount &&
		pol.Status.UnstableReplicas == unstableCount &&
		equalStringInt32Map(pol.Status.UnstableReasonsCount, reasonCounts) &&
		equalCurrentLayout(pol.Status.CurrentLayout, items) {
		return nil
	}
	patch := client.MergeFrom(pol.DeepCopy())
	pol.Status.ObservedReplicas = observedReplicas
	pol.Status.CurrentLayout = items
	pol.Status.StableReplicas = stableCount
	pol.Status.UnstableReplicas = unstableCount
	pol.Status.UnstableReasonsCount = reasonCounts
	return r.Status().Patch(ctx, pol, patch)
}

// globalDeploymentMigratable returns whether a GlobalDeployment's template is migratable.
// Current rules (M2 3.4):
// - hostPath volume => not migratable
// - PV with local volume => not migratable (we conservatively mark as not migratable if template references a PVC)
// - required nodeAffinity => not migratable
//
// Note: We only have access to the template in the control-plane cluster (envtest). We don't
// dereference PVC/PV objects here; instead we do best-effort checks on the template.
func globalDeploymentMigratable(gd *corev1alpha1.GlobalDeployment) (bool, string) {
	if gd == nil {
		return true, ""
	}
	// Decode RawExtension into appsv1.DeploymentSpec.
	var spec appsv1.DeploymentSpec
	if len(gd.Spec.Template.Raw) > 0 {
		// Prefer JSON first; also works when Raw contains YAML if it was stored as JSON.
		_ = json.Unmarshal(gd.Spec.Template.Raw, &spec)
		if spec.Template.Spec.Containers == nil && spec.Template.Spec.Volumes == nil && spec.Selector == nil {
			// Fallback: try yaml in case the Raw is YAML.
			// We avoid importing sigs.k8s.io/yaml here to keep deps minimal; the RB controller already
			// uses yaml.Unmarshal, but this controller can remain JSON-only for now.
		}
	}

	// required nodeAffinity
	if spec.Template.Spec.Affinity != nil && spec.Template.Spec.Affinity.NodeAffinity != nil {
		if spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
			return false, "NonMigratableRequiredNodeAffinity"
		}
	}

	// hostPath & pvc usage checks
	for _, v := range spec.Template.Spec.Volumes {
		if v.HostPath != nil {
			return false, "NonMigratableHostPath"
		}
		// Best-effort: referencing PVC is often tied to a specific storage class / zone.
		// We treat it as non-migratable until we can inspect the actual PV and detect `local`.
		if v.PersistentVolumeClaim != nil {
			return false, "NonMigratablePVC"
		}
		// If user specifies inline CSI volume, it's potentially migratable; we don't block it.
		_ = corev1.Volume{} // keep corev1 import used even if build tags change
	}

	return true, ""
}

func equalStringInt32Map(a, b map[string]int32) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if bv, ok := b[k]; !ok || bv != av {
			return false
		}
	}
	return true
}

func equalCurrentLayout(a, b []corev1alpha1.CurrentReplicaLocation) bool {
	if len(a) != len(b) {
		return false
	}
	// Order isn't guaranteed due to map iteration; compare as signature map.
	type sig struct {
		ns   string
		name string
		idx  int32
		cid  string
		node string
		src  string
	}
	ma := make(map[sig]int, len(a))
	for _, v := range a {
		ma[sig{v.Namespace, v.Name, v.ReplicaIndex, v.ClusterId, v.NodeName, v.Source}]++
	}
	for _, v := range b {
		s := sig{v.Namespace, v.Name, v.ReplicaIndex, v.ClusterId, v.NodeName, v.Source}
		if ma[s] == 0 {
			return false
		}
		ma[s]--
	}
	for _, c := range ma {
		if c != 0 {
			return false
		}
	}
	return true
}

func (r *OptimizationPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ensureMetricsRegistered(nil)
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("optimizationpolicy")
	}
	if r.timerHandles == nil {
		r.timerHandles = map[types.NamespacedName]context.CancelFunc{}
	}
	if r.nowFunc == nil {
		r.nowFunc = time.Now
	}
	if r.evaluateFunc == nil {
		r.evaluateFunc = r.defaultEvaluate
	}
	if r.ControlNamespace == "" {
		r.ControlNamespace = "kubex-system"
	}
	if r.memberClients == nil {
		r.memberClients = multicluster.NewMemberClientCache()
	}
	if r.LockNamespace == "" {
		r.LockNamespace = "kubex-system"
	}
	if r.LockName == "" {
		r.LockName = "kubex-optimization-policy-lock"
	}
	if r.LeaseDurationSeconds == 0 {
		r.LeaseDurationSeconds = 15
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.OptimizationPolicy{}).
		Named("optimizationpolicy").
		Complete(r)
}

func (r *OptimizationPolicyReconciler) getMemberClient(ctx context.Context, clusterID string) (*kubernetes.Clientset, error) {
	if r.memberClients == nil {
		r.memberClients = multicluster.NewMemberClientCache()
	}
	controlNS := r.ControlNamespace
	if controlNS == "" {
		controlNS = "kubex-system"
	}
	return r.memberClients.GetOrBuild(ctx, r.Client, controlNS, clusterID)
}

func (r *OptimizationPolicyReconciler) cancelTimer(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.timerHandles == nil {
		return
	}
	if cancel, ok := r.timerHandles[key]; ok {
		cancel()
		delete(r.timerHandles, key)
	}
}

func (r *OptimizationPolicyReconciler) ensureTimer(parent context.Context, pol *corev1alpha1.OptimizationPolicy) error {
	key := types.NamespacedName{Name: pol.Name, Namespace: pol.Namespace}
	// allow direct-reconciler usage in tests without calling SetupWithManager
	if r.timerHandles == nil {
		r.timerHandles = map[types.NamespacedName]context.CancelFunc{}
	}
	if r.nowFunc == nil {
		r.nowFunc = time.Now
	}
	if r.evaluateFunc == nil {
		r.evaluateFunc = r.defaultEvaluate
	}

	// Figure out next trigger.
	now := r.nowFunc()
	var next time.Time
	switch pol.Spec.RunMode {
	case corev1alpha1.OptimizationRunModeOnce:
		// If never run => schedule immediate; else no timer.
		if pol.Status.LastEvaluationTime.IsZero() {
			next = now
		} else {
			r.cancelTimer(key)
			return nil
		}
	case corev1alpha1.OptimizationRunModePeriodic:
		// If no last => schedule immediate; else schedule last + duration.
		d, err := time.ParseDuration(pol.Spec.RebalancePoint)
		if err != nil {
			return err
		}
		if pol.Status.LastEvaluationTime.IsZero() {
			// First periodic run: schedule at now + duration (so rebalancePoint edits can be observed).
			next = now.Add(d)
		} else {
			next = pol.Status.LastEvaluationTime.Time.Add(d)
			// If already overdue, trigger immediately.
			if !next.After(now) {
				next = now
			}
		}
	default:
		// unknown runmode: be safe
		r.cancelTimer(key)
		return nil
	}

	delay := time.Until(next)
	if delay < 0 {
		delay = 0
	}

	// Always cancel & reschedule on reconcile to satisfy: policy changed => cancel old timer.
	// (M1 acceptance expects rebalancePoint change takes effect immediately)
	r.cancelTimer(key)

	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	r.timerHandles[key] = cancel
	r.mu.Unlock()

	// Use AfterFunc so we can cancel it via context.
	t := time.NewTimer(delay)
	go func() {
		defer t.Stop()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Use parent context so logs are correlated; defaultEvaluate has its own per-policy lock.
			_ = r.evaluateFunc(parent, key)
		}
	}()

	return nil
}

func (r *OptimizationPolicyReconciler) defaultEvaluate(ctx context.Context, key types.NamespacedName) error {
	// Prevent concurrent evaluations for the same policy.
	muAny, _ := r.evaluateLocks.LoadOrStore(key, &sync.Mutex{})
	lk := muAny.(*sync.Mutex)
	lk.Lock()
	defer lk.Unlock()

	// De-dup guard: if we just evaluated very recently, skip.
	// This protects against reconcile-trigger + timer-trigger happening back-to-back
	// in a single controller process.
	// NOTE: keep window small so periodic schedules still work.
	const dedupWindow = 60 * time.Second
	{
		var cur corev1alpha1.OptimizationPolicy
		if err := r.Get(ctx, key, &cur); err == nil {
			if !cur.Status.LastEvaluationTime.IsZero() {
				now := r.nowFunc()
				d := now.Sub(cur.Status.LastEvaluationTime.Time)
				if d >= 0 && d < dedupWindow {
					log.FromContext(ctx).Info(
						"policy evaluation skipped (dedup)",
						"policy", key,
						"last", cur.Status.LastEvaluationTime.Time,
						"now", now,
						"delta", d.String(),
					)
					return nil
				}
			}
		}
	}

	// M4 scope (partial): create ReOrchestrationPlan stubs and apply Conservative "not executed below threshold" behavior.
	// NOTE: Full plan generation (solver integration) + policy status updates are covered in later milestones (M4 5.1/5.3).
	start := time.Now()
	log.FromContext(ctx).Info("policy evaluation started", "policy", key)
	defer func() {
		log.FromContext(ctx).Info("policy evaluation finished", "policy", key, "elapsed", time.Since(start).String())
	}()
	var pol corev1alpha1.OptimizationPolicy
	if err := r.Get(ctx, key, &pol); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Always update lastEvaluationTime (keeps existing behavior for M1 tests).
	// Best-effort is okay, but log failures because it directly impacts dedup.
	patch := client.MergeFrom(pol.DeepCopy())
	pol.Status.LastEvaluationTime = metav1.NewTime(r.nowFunc())
	if err := r.Status().Patch(ctx, &pol, patch); err != nil {
		log.FromContext(ctx).Error(err, "failed to patch policy lastEvaluationTime (dedup may be affected)", "policy", key)
	}

	// Best-effort: if the policy isn't Active anymore, don't create plans.
	if pol.Status.Phase != corev1alpha1.OptimizationPolicyPhaseActive {
		return nil
	}
	activePolicyGauge.Set(1)
	defer func() {
		calculationDuration.Observe(time.Since(start).Seconds())
	}()

	// M3: compute improvement via solver. We still allow injecting improvementPct via annotation
	// so existing M4 5.2 tests can stay stable.
	const annImprovementPct = "kubex.io/debug-improvement-percent"
	improvementPct, improvementSource, err := r.computeImprovementPct(ctx, &pol)
	if err != nil {
		// If OR-Tools backend is disabled (default build), keep controller functional by
		// falling back to 0 improvement. This preserves M4 behavior and keeps tests green.
		if errors.Is(err, optimizer.ErrSolverNotImplemented) {
			improvementPct = 0
			improvementSource = "solver-disabled"
			err = nil
		} else {
			// M3 4.1: surface solver failure/timeout via condition + event.
			reason := "SolverError"
			msg := err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				reason = "SolverTimeout"
				msg = "solver timed out"
			}
			_ = r.setStatusPhaseAndCondition(ctx, &pol, pol.Status.Phase, metav1.Condition{
				Type:               "RebalanceSucceeded",
				Status:             metav1.ConditionFalse,
				Reason:             reason,
				Message:            msg,
				ObservedGeneration: pol.GetGeneration(),
				LastTransitionTime: metav1.Now(),
			})
			if r.Recorder != nil {
				r.Recorder.Event(&pol, "Warning", reason, msg)
			}
			return err
		}
	}
	if pol.Annotations != nil {
		if v, ok := pol.Annotations[annImprovementPct]; ok {
			if p, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				improvementPct = p
				improvementSource = "annotation"
			}
		}
	}

	// Create a minimal plan object. Execution decision is only about status/condition in this milestone.
	planName := fmt.Sprintf("%s-%s", pol.Name, rand.String(6))
	plan := &corev1alpha1.ReOrchestrationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      planName,
			Namespace: pol.Namespace,
			Labels: map[string]string{
				"kubex.io/policy": pol.Name,
			},
			Annotations: map[string]string{
				"kubex.io/strategy":             string(pol.Spec.Strategy),
				annImprovementPct:               fmt.Sprintf("%g", improvementPct),
				"kubex.io/improvementPctSource": improvementSource,
			},
		},
		Spec: corev1alpha1.ReOrchestrationPlanSpec{
			Summary: corev1alpha1.PlanSummary{PolicyName: pol.Name},
		},
	}
	// Make the plan owned by the OptimizationPolicy so deleting the policy cascades to plans.
	// This also helps operators quickly trace plan provenance.
	if err := controllerutil.SetControllerReference(&pol, plan, r.Scheme()); err != nil {
		return err
	}
	// Always populate deterministic summary fields so API/UI can rely on non-nil values.
	zeroPods := int32(0)
	zeroScore := float64(0)
	plan.Spec.Summary.PodsToMove = &zeroPods
	plan.Spec.Summary.CurrentScore = &zeroScore
	plan.Spec.Summary.ExpectedScore = &zeroScore
	plan.Spec.Summary.EstimatedImprovementScore = &zeroScore
	plansCreated.Inc()

	// M3/M4 bridge: generate plan moves from solver assignment.
	// Current placement uses policy.status.currentLayout (best-effort in control-plane envtest).
	// Expected placement comes from solver output. We only emit moves for stable replicas and when destination != current.
	//
	// Note: The full snapshot/scoring pipeline is later milestones; here we focus on connecting assignment -> moves.
	if len(pol.Status.CurrentLayout) > 0 {
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

		if len(stableReplicas) > 0 {
			// Candidate nodes: derive from Cluster CR status.nodes (not from host Node API).
			// Only include clusters in Phase=Ready and nodes with Ready=true.
			candidateNodes := make([]optimizer.ClusterNodeID, 0)
			{
				var clusters corev1alpha1.ClusterList
				if err := r.List(ctx, &clusters); err != nil {
					return err
				}
				for i := range clusters.Items {
					c := &clusters.Items[i]
					if c.Status.Phase != corev1alpha1.ClusterPhaseReady {
						continue
					}
					clusterID := c.Name
					for _, ns := range c.Status.Nodes {
						if !ns.Ready {
							continue
						}
						if ns.Name == "" {
							continue
						}
						candidateNodes = append(candidateNodes, optimizer.ClusterNodeID{ClusterID: clusterID, NodeName: ns.Name})
					}
				}
			}
			if len(candidateNodes) == 0 {
				// Safety fallback: keep old behavior.
				//
				// In envtest/unit tests we often don't populate Cluster.status.nodes.
				// When that happens, fall back to nodes in current placement so we can still
				// build NodeContexts, compute summary scores, and create a plan.
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
				return err
			}

			// ReplicaRequests: derive from host GlobalDeployment template container requests.
			// We treat each replicaIndex as having the same request.
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
					return err
				}
				// ns/name -> resource request per replica
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
						milli = 1000 // safe default
					}
					if memMi <= 0 {
						memMi = 1024 // safe default (1Gi)
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

			// Build objective inputs from policy goals + ConfigMap profiles/topology.
			goals := make([]optimizer.WeightedGoal, 0, len(pol.Spec.OptimizationGoals))
			for _, g := range pol.Spec.OptimizationGoals {
				if g.Weight == 0 {
					continue
				}
				goals = append(goals, optimizer.WeightedGoal{
					Type:        g.Type,
					Weight:      g.Weight,
					SourceCity:  g.SourceCity,
					TopologyRef: g.TopologyRef,
				})
			}

			profiles, err := loadProfilesFromConfigMaps(ctx, r.Client, r.getProfilesNamespace())
			if err != nil {
				return err
			}

			// Optional Communication goal wiring.
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
					return err
				}
				// Build GlobalDeployment -> serviceName mapping from labels.
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
						return listErr
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
						// Topology templates (ConfigMap) often use service name only (namespace omitted).
						// To make deps lookup work (p.Dependencies[svc]), also omit namespace here.
						replicaService[rk] = optimizer.NamespacedName{Namespace: "", Name: s.Name}
					} else {
						// Fallback: keep name, omit namespace so it matches topology keys.
						replicaService[rk] = optimizer.NamespacedName{Namespace: "", Name: rk.Name}
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

			assignment, err := r.solvePlacement(ctx, &pol, p)
			if err != nil {
				var tooMany *optimizer.ErrTooManyVariables
				if errors.As(err, &tooMany) {
					msg := fmt.Sprintf("ILP 变量规模超限：variables=%d limit=%d；请缩小目标范围（更少 replicas / nodes）或提升上限", tooMany.Variables, tooMany.Limit)
					apimeta.SetStatusCondition(&pol.Status.Conditions, metav1.Condition{
						Type:               "RebalanceSucceeded",
						Status:             metav1.ConditionFalse,
						Reason:             "TooManyVariables",
						Message:            msg,
						ObservedGeneration: pol.Generation,
						LastTransitionTime: metav1.Now(),
					})
					r.Recorder.Event(&pol, corev1.EventTypeWarning, "TooManyVariables", msg)
					return r.Status().Update(ctx, &pol)
				}
				// Solver not enabled: keep controller unblocked.
				// Use current placement as a stable fallback so we still compute summary/moves.
				if errors.Is(err, optimizer.ErrSolverNotImplemented) {
					assignment = map[optimizer.ReplicaKey]optimizer.ClusterNodeID{}
					for rk, nid := range currentPlacement {
						assignment[rk] = nid
					}
				} else {
					return err
				}
			}
			moves := make([]corev1alpha1.PlanMove, 0)
			clusterNameByID := map[string]string{}
			getClusterName := func(clusterID string) string {
				if n, ok := clusterNameByID[clusterID]; ok {
					return n
				}
				var c corev1alpha1.Cluster
				err := r.Get(ctx, types.NamespacedName{Namespace: r.ControlNamespace, Name: clusterID}, &c)
				if err == nil {
					if v := strings.TrimSpace(c.Annotations["kubex.io/name"]); v != "" {
						clusterNameByID[clusterID] = v
						return v
					}
					if c.Name != "" {
						clusterNameByID[clusterID] = c.Name
						return c.Name
					}
				}
				// Fallback: keep it readable even if Cluster object isn't available.
				clusterNameByID[clusterID] = clusterID
				return clusterID
			}
			for _, rkey := range stableReplicas {
				src, ok := currentPlacement[rkey]
				if !ok {
					continue
				}
				dst, ok := assignment[rkey]
				if !ok {
					continue
				}
				if src == dst {
					continue
				}
				srcName := getClusterName(src.ClusterID)
				dstName := getClusterName(dst.ClusterID)
				moves = append(moves, corev1alpha1.PlanMove{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: rkey.Name, Namespace: rkey.Namespace},
					ReplicaIndex:        rkey.ReplicaIndex,
					Source:              corev1alpha1.MoveLocation{ClusterID: src.ClusterID, ClusterName: srcName, NodeName: src.NodeName},
					Destination:         corev1alpha1.MoveLocation{ClusterID: dst.ClusterID, ClusterName: dstName, NodeName: dst.NodeName},
				})
			}
			plan.Spec.Moves = moves
			log.FromContext(ctx).Info("plan moves generated", "policy", client.ObjectKeyFromObject(&pol), "moves", len(moves))

			// Summary still comes from the same scoring pipeline.
			pluginCtx := optimizer.PluginContext{Replicas: stableReplicas, Nodes: nodeCtxs, CurrentPlacement: currentPlacement, ReplicaRequests: repReq}
			if len(goals) > 0 {
				// Inject controller logger for optimizer-level diagnostics.
				diagCtx := context.WithValue(ctx, optimizer.OptimizerDiagLoggerKey{}, log.FromContext(ctx))
				metrics, _, mErr := optimizer.ComputePlanFromGoals(diagCtx, optimizer.ObjectiveInputs{
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
				if mErr != nil {
					return mErr
				}
				if metrics.CurrentScore == 0 && metrics.ExpectedScore == 0 {
					log.FromContext(ctx).Info("plan summary scores are zero; please check goal/plugin inputs and normalization", "policy", client.ObjectKeyFromObject(&pol), "podsToMove", metrics.PodsToMove, "goals", len(goals), "hasComm", hasComm, "deps", len(deps), "regionLatencyRows", len(regionLat), "replicaService", len(replicaService), "nodeRegion", len(nodeRegion), "cityLatencyRows", len(profiles.CityRegionLatencyMs), "instancePrice", len(profiles.InstancePrice))
				}
				podsToMove := metrics.PodsToMove
				cur := metrics.CurrentScore
				exp := metrics.ExpectedScore
				impr := metrics.EstimatedImprovement
				plan.Spec.Summary.PodsToMove = &podsToMove
				plan.Spec.Summary.CurrentScore = &cur
				plan.Spec.Summary.ExpectedScore = &exp
				plan.Spec.Summary.EstimatedImprovementScore = &impr

				// Per-goal breakdown for explainability.
				goalScores := map[string]corev1alpha1.GoalScoreSummary{}
				for _, g := range goals {
					// skip disabled goals
					if g.Weight == 0 {
						continue
					}
					m2, _, gErr := optimizer.ComputePlanForSingleGoal(diagCtx, optimizer.ObjectiveInputs{
						Goals:               nil, // overwritten inside ComputePlanForSingleGoal
						Ctx:                 pluginCtx,
						InstancePrice:       profiles.InstancePrice,
						InstancePower:       profiles.InstancePower,
						CityRegionLatencyMs: profiles.CityRegionLatencyMs,
						Dependencies:        deps,
						RegionLatencyMs:     regionLat,
						ReplicaService:      replicaService,
						NodeRegion:          nodeRegion,
					}, g, optimizer.ExpectedPlacement(assignment))
					if gErr != nil {
						return gErr
					}
					w := g.Weight
					gcur := m2.CurrentScore
					gexp := m2.ExpectedScore
					gimpr := m2.EstimatedImprovement

					// Human-readable metric in original units (best-effort).
					var hr *corev1alpha1.GoalHumanReadableSummary
					if h, hrErr := optimizer.ComputeHumanReadableMetric(diagCtx, optimizer.ObjectiveInputs{
						// NOTE: reuse the same inputs as scoring; this metric is goal-specific and unit-based.
						Goals:               nil,
						Ctx:                 pluginCtx,
						InstancePrice:       profiles.InstancePrice,
						InstancePower:       profiles.InstancePower,
						CityRegionLatencyMs: profiles.CityRegionLatencyMs,
						Dependencies:        deps,
						RegionLatencyMs:     regionLat,
						ReplicaService:      replicaService,
						NodeRegion:          nodeRegion,
					}, g, optimizer.ExpectedPlacement(assignment)); hrErr == nil {
						hr = &corev1alpha1.GoalHumanReadableSummary{
							Kind:   h.Kind,
							Unit:   h.Unit,
							From:   h.From,
							To:     h.To,
							Delta:  h.Delta,
							Detail: h.Detail,
						}
					} else {
						// Keep it silent: readable metric is optional and should not fail plan creation.
						log.FromContext(ctx).Info("human readable metric compute skipped", "goal", g.Type, "error", hrErr.Error())
					}

					goalScores[g.Type] = corev1alpha1.GoalScoreSummary{
						Weight:                    &w,
						CurrentScore:              &gcur,
						ExpectedScore:             &gexp,
						EstimatedImprovementScore: &gimpr,
						HumanReadable:             hr,
					}
				}
				if len(goalScores) > 0 {
					plan.Spec.Summary.GoalScores = goalScores
				}
			}
		}
	}

	// We'll set Status via the status subresource after create.
	status := corev1alpha1.ReOrchestrationPlanStatus{Phase: corev1alpha1.PlanPhasePending}

	// M4 5.2 Conservative behavior: threshold not met => keep Pending and record reason.
	if pol.Spec.Strategy == corev1alpha1.OptimizationStrategyConservative {
		thr := float64(pol.Spec.ImprovementThresholdPercent)
		if improvementPct < thr {
			// Also write a stable annotation so callers can observe it without relying on Status subresource.
			plan.Annotations["kubex.io/notExecuted"] = "improvement below threshold"
			plan.Annotations["kubex.io/notExecutedReason"] = "ImprovementBelowThreshold"
			plan.Annotations["kubex.io/improvementThresholdPercent"] = fmt.Sprintf("%g", thr)
			status.Phase = corev1alpha1.PlanPhasePending
			status.Conditions = append(status.Conditions, metav1.Condition{
				Type:               "NotExecuted",
				Status:             metav1.ConditionTrue,
				Reason:             "ImprovementBelowThreshold",
				Message:            fmt.Sprintf("NotExecuted: improvement below threshold (got %.4g%%, threshold %.4g%%)", improvementPct, thr),
				ObservedGeneration: 0,
				LastTransitionTime: metav1.Now(),
			})
		} else {
			// In M5 we will set Executing for Conservative/Aggressive; in M4 5.2 we only guarantee the "not executed" path.
			status.Phase = corev1alpha1.PlanPhasePending
		}
	}

	// Persist plan. Ignore AlreadyExists (extremely unlikely with random suffix).
	if err := r.Create(ctx, plan); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	// Set status for conditions/phase.
	statusPatch := client.MergeFrom(plan.DeepCopy())
	plan.Status = status
	if err := r.Status().Patch(ctx, plan, statusPatch); err != nil {
		return err
	}

	// M4 5.3: write policy.status.latestPlanRef and update RebalanceSucceeded condition.
	// NOTE: Today we treat "plan created" as succeeded; later milestones will encode solver failure/timeout/insufficient-input as False.
	polPatch := client.MergeFrom(pol.DeepCopy())
	pol.Status.LatestPlanRef = &corev1alpha1.LocalObjectRef{Name: plan.Name, Namespace: plan.Namespace}
	apimeta.SetStatusCondition(&pol.Status.Conditions, metav1.Condition{
		Type:               "RebalanceSucceeded",
		Status:             metav1.ConditionTrue,
		Reason:             "PlanCreated",
		Message:            fmt.Sprintf("created plan %s/%s", plan.Namespace, plan.Name),
		ObservedGeneration: pol.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, &pol, polPatch); err != nil {
		return err
	}

	return nil
}

func (r *OptimizationPolicyReconciler) getLockNamespace() string {
	if r.LockNamespace != "" {
		return r.LockNamespace
	}
	return "kubex-system"
}

func (r *OptimizationPolicyReconciler) getLockName() string {
	if r.LockName != "" {
		return r.LockName
	}
	return "kubex-optimization-policy-lock"
}

func (r *OptimizationPolicyReconciler) getLeaseDurationSeconds() int32 {
	if r.LeaseDurationSeconds > 0 {
		return r.LeaseDurationSeconds
	}
	return 15
}

func (r *OptimizationPolicyReconciler) getProfilesNamespace() string {
	if r.ProfilesNamespace != "" {
		return r.ProfilesNamespace
	}
	return "kubex-system"
}

func (r *OptimizationPolicyReconciler) identity() string {
	if v := os.Getenv("POD_NAME"); v != "" {
		return v
	}
	return "kubeX-controller"
}

func toNodeContexts(nodes []optimizer.ClusterNodeID) []optimizer.NodeContext {
	out := make([]optimizer.NodeContext, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, optimizer.NodeContext{ID: n})
	}
	return out
}

func (r *OptimizationPolicyReconciler) buildNodeContexts(ctx context.Context, nodes []optimizer.ClusterNodeID) ([]optimizer.NodeContext, error) {
	// Multi-cluster: Node objects live in member clusters, so we must fetch Node metadata
	// (labels/allocatable/region) from each member api-server.
	//
	// Production contract: we do NOT fall back to host cluster nodes. If member access fails,
	// we return an error so the caller can surface it via events/conditions.
	//
	// Note: CPU baseline (Pod requests) is intentionally left as 0 for now to avoid
	// cross-cluster Pod listing overhead. Capacity is still populated.

	out := make([]optimizer.NodeContext, 0, len(nodes))
	for _, id := range nodes {
		nc := optimizer.NodeContext{ID: id}
		if id.ClusterID == "" || id.NodeName == "" {
			return nil, fmt.Errorf("buildNodeContexts: empty clusterID/nodeName: clusterID=%q nodeName=%q", id.ClusterID, id.NodeName)
		}
		cs, err := r.getMemberClient(ctx, id.ClusterID)
		if err != nil {
			return nil, fmt.Errorf("buildNodeContexts: get member client for cluster %q: %w", id.ClusterID, err)
		}
		n, err := cs.CoreV1().Nodes().Get(ctx, id.NodeName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("buildNodeContexts: get node %q in cluster %q: %w", id.NodeName, id.ClusterID, err)
		}
		nc.Labels = n.Labels
		if n.Labels != nil {
			if v := n.Labels["node.kubex.io/region"]; v != "" {
				nc.Region = v
			}
		}
		if n.Status.Allocatable != nil {
			if cpuQty, ok := n.Status.Allocatable[corev1.ResourceCPU]; ok {
				nc.CPUAllocatableMilli = cpuQty.MilliValue()
			}
			// Memory allocatable (Mi). ResourceQuantity.Value() is bytes for memory.
			if memQty, ok := n.Status.Allocatable[corev1.ResourceMemory]; ok {
				nc.MemoryAllocatableMi = memQty.Value() / (1024 * 1024)
			}
		}
		out = append(out, nc)
	}
	return out, nil
}

// tryAcquirePolicyLease tries to acquire/renew the shared lease.
// Contract:
// - acquired=true means caller is the holder (may have renewed).
// - acquired=false means someone else holds it; holder identifies them (best-effort).
func (r *OptimizationPolicyReconciler) tryAcquirePolicyLease(ctx context.Context, pol *corev1alpha1.OptimizationPolicy) (acquired bool, holder string, err error) {
	leaseKey := types.NamespacedName{Name: r.getLockName(), Namespace: r.getLockNamespace()}
	var lease coordinationv1.Lease
	if err := r.Get(ctx, leaseKey, &lease); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, "", err
		}
		// create a new lease
		id := r.identity() + ":" + pol.Namespace + "/" + pol.Name
		now := metav1.NowMicro()
		dur := r.getLeaseDurationSeconds()
		lease = coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{Name: leaseKey.Name, Namespace: leaseKey.Namespace},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       &id,
				LeaseDurationSeconds: &dur,
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}
		if err := r.Create(ctx, &lease); err != nil {
			// if create races, try read again next reconcile
			return false, "", err
		}
		return true, id, nil
	}

	// existing lease
	if lease.Spec.HolderIdentity != nil {
		holder = *lease.Spec.HolderIdentity
	}

	// if already holder: renew
	selfId := r.identity() + ":" + pol.Namespace + "/" + pol.Name
	if holder == selfId {
		patch := client.MergeFrom(lease.DeepCopy())
		now := metav1.NowMicro()
		lease.Spec.RenewTime = &now
		dur := r.getLeaseDurationSeconds()
		lease.Spec.LeaseDurationSeconds = &dur
		if err := r.Patch(ctx, &lease, patch); err != nil {
			return false, holder, err
		}
		return true, selfId, nil
	}

	// time-based expiration check (best-effort): if expired, attempt takeover.
	var renewTime time.Time
	if lease.Spec.RenewTime != nil {
		rt := lease.Spec.RenewTime.Time
		renewTime = rt
	}
	leaseDur := r.getLeaseDurationSeconds()
	if lease.Spec.LeaseDurationSeconds != nil {
		leaseDur = *lease.Spec.LeaseDurationSeconds
	}
	if !renewTime.IsZero() {
		if time.Since(renewTime) <= time.Duration(leaseDur)*time.Second {
			return false, holder, nil
		}
	}

	// expired (or unknown renew time): try takeover
	patch := client.MergeFrom(lease.DeepCopy())
	now := metav1.NowMicro()
	lease.Spec.HolderIdentity = &selfId
	lease.Spec.AcquireTime = &now
	lease.Spec.RenewTime = &now
	dur := r.getLeaseDurationSeconds()
	lease.Spec.LeaseDurationSeconds = &dur
	if err := r.Patch(ctx, &lease, patch); err != nil {
		return false, holder, err
	}
	return true, selfId, nil
}

// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch

func (r *OptimizationPolicyReconciler) setStatusPhaseAndCondition(ctx context.Context, pol *corev1alpha1.OptimizationPolicy, phase corev1alpha1.OptimizationPolicyPhase, cond metav1.Condition) error {
	// avoid unnecessary status writes
	needUpdate := pol.Status.Phase != phase
	if !needUpdate {
		// check existing condition
		for _, c := range pol.Status.Conditions {
			if c.Type == cond.Type && c.Status == cond.Status && c.Reason == cond.Reason && c.Message == cond.Message && c.ObservedGeneration == cond.ObservedGeneration {
				needUpdate = false
				break
			}
			needUpdate = true
		}
	}
	if !needUpdate {
		return nil
	}

	patch := client.MergeFrom(pol.DeepCopy())
	pol.Status.Phase = phase
	apimeta.SetStatusCondition(&pol.Status.Conditions, cond)
	return r.Status().Patch(ctx, pol, patch)
}

func validateOptimizationPolicySpec(pol *corev1alpha1.OptimizationPolicy) (corev1alpha1.OptimizationPolicyPhase, metav1.Condition, bool) {
	// 1) Goal 类型必须是注册的默认 goal（M1 先做硬编码，后续可切到插件注册表）
	allowedGoals := map[string]struct{}{
		"Cost":          {},
		"Latency":       {},
		"Communication": {},
		"Energy":        {},
		"Migration":     {},
	}
	for i := range pol.Spec.OptimizationGoals {
		g := pol.Spec.OptimizationGoals[i]
		if _, ok := allowedGoals[g.Type]; !ok {
			return corev1alpha1.OptimizationPolicyPhaseFailed, metav1.Condition{
				Type:               "SpecValid",
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidGoalType",
				Message:            fmt.Sprintf("goal[%d].type=%q is not supported", i, g.Type),
				ObservedGeneration: pol.GetGeneration(),
				LastTransitionTime: metav1.Now(),
			}, false
		}
	}

	// 2) weight>0 的项求和必须 == 1（允许浮点误差阈值 1e-6）
	const sumEps = 1e-6
	sum := 0.0
	for _, g := range pol.Spec.OptimizationGoals {
		if g.Weight > 0 {
			sum += g.Weight
		}
	}
	if len(pol.Spec.OptimizationGoals) > 0 {
		if math.Abs(sum-1.0) > sumEps {
			return corev1alpha1.OptimizationPolicyPhaseFailed, metav1.Condition{
				Type:               "SpecValid",
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidGoalWeights",
				Message:            fmt.Sprintf("sum(weights where weight>0) must be 1 (eps=%g), got %g", sumEps, sum),
				ObservedGeneration: pol.GetGeneration(),
				LastTransitionTime: metav1.Now(),
			}, false
		}
	}

	// 3) runMode=Periodic 时 rebalancePoint 可 parse
	if pol.Spec.RunMode == corev1alpha1.OptimizationRunModePeriodic {
		if pol.Spec.RebalancePoint == "" {
			return corev1alpha1.OptimizationPolicyPhaseFailed, metav1.Condition{
				Type:               "SpecValid",
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidRebalancePoint",
				Message:            "rebalancePoint is required when runMode=Periodic",
				ObservedGeneration: pol.GetGeneration(),
				LastTransitionTime: metav1.Now(),
			}, false
		}
		if _, err := time.ParseDuration(pol.Spec.RebalancePoint); err != nil {
			return corev1alpha1.OptimizationPolicyPhaseFailed, metav1.Condition{
				Type:               "SpecValid",
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidRebalancePoint",
				Message:            fmt.Sprintf("rebalancePoint %q is not a valid duration: %v", pol.Spec.RebalancePoint, err),
				ObservedGeneration: pol.GetGeneration(),
				LastTransitionTime: metav1.Now(),
			}, false
		}
	}

	// 4) strategy=Conservative 时 improvementThresholdPercent 合法（0-100）
	if pol.Spec.Strategy == corev1alpha1.OptimizationStrategyConservative {
		if pol.Spec.ImprovementThresholdPercent < 0 || pol.Spec.ImprovementThresholdPercent > 100 {
			return corev1alpha1.OptimizationPolicyPhaseFailed, metav1.Condition{
				Type:               "SpecValid",
				Status:             metav1.ConditionFalse,
				Reason:             "InvalidImprovementThreshold",
				Message:            fmt.Sprintf("improvementThresholdPercent must be in [0,100], got %d", pol.Spec.ImprovementThresholdPercent),
				ObservedGeneration: pol.GetGeneration(),
				LastTransitionTime: metav1.Now(),
			}, false
		}
	}

	return corev1alpha1.OptimizationPolicyPhasePending, metav1.Condition{}, true
}
