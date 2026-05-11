package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	planExecutionTimeoutAnnotation = "kubex.io/execution-timeout"
	defaultPlanExecutionTimeout    = 10 * time.Minute
)

type ReOrchestrationPlanReconciler struct {
	client.Client
}

func (r *ReOrchestrationPlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ensureMetricsRegistered(nil)
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.ReOrchestrationPlan{}).
		Complete(r)
}

func (r *ReOrchestrationPlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	lg := log.FromContext(ctx)
	var plan corev1alpha1.ReOrchestrationPlan
	if err := r.Get(ctx, req.NamespacedName, &plan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	strategy := corev1alpha1.OptimizationStrategyPreview
	if plan.Annotations != nil {
		if v, ok := plan.Annotations["kubex.io/strategy"]; ok {
			strategy = corev1alpha1.OptimizationStrategy(v)
		}
	}

	// If already finished, do nothing.
	if plan.Status.Phase == corev1alpha1.PlanPhaseSucceeded || plan.Status.Phase == corev1alpha1.PlanPhaseFailed || plan.Status.Phase == corev1alpha1.PlanPhasePartiallyFailed || plan.Status.Phase == corev1alpha1.PlanPhaseTerminating {
		return ctrl.Result{}, nil
	}

	desiredPhase := corev1alpha1.PlanPhasePending
	cond := metav1.Condition{
		Type:               "ExecutionDecision",
		ObservedGeneration: plan.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	}

	switch strategy {
	case corev1alpha1.OptimizationStrategyPreview:
		desiredPhase = corev1alpha1.PlanPhasePending
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Preview"
		cond.Message = "Preview strategy: plan will not be executed"
	case corev1alpha1.OptimizationStrategyConservative, corev1alpha1.OptimizationStrategyAggressive:
		desiredPhase = corev1alpha1.PlanPhaseExecuting
		cond.Status = metav1.ConditionTrue
		cond.Reason = "AutoExecute"
		cond.Message = fmt.Sprintf("strategy=%s: plan enters Executing", strategy)
	default:
		desiredPhase = corev1alpha1.PlanPhasePending
		cond.Status = metav1.ConditionFalse
		cond.Reason = "UnknownStrategy"
		cond.Message = fmt.Sprintf("unknown strategy %q: keep Pending", strategy)
	}

	phaseChanged := plan.Status.Phase != desiredPhase
	if phaseChanged {
		patch := client.MergeFrom(plan.DeepCopy())
		plan.Status.Phase = desiredPhase
		apimeta.SetStatusCondition(&plan.Status.Conditions, cond)
		if err := r.Status().Patch(ctx, &plan, patch); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			}
			lg.Error(err, "failed to patch plan status", "phase", desiredPhase)
			return ctrl.Result{}, err
		}
	}

	if desiredPhase == corev1alpha1.PlanPhaseExecuting {
		if err := r.executeMoves(ctx, &plan); err != nil {
			lg.Error(err, "failed to execute moves")
			return ctrl.Result{}, err
		}
		// Monitor completion/timeout and converge plan phase.
		res, err := r.monitorAndConverge(ctx, &plan)
		if err != nil {
			lg.Error(err, "failed to monitor plan execution")
			return ctrl.Result{}, err
		}
		return res, nil
	}

	return ctrl.Result{}, nil
}

func (r *ReOrchestrationPlanReconciler) executeMoves(ctx context.Context, plan *corev1alpha1.ReOrchestrationPlan) error {
	if plan == nil {
		return nil
	}

	// Initialize startTime + moveStatuses (best-effort).
	latest := &corev1alpha1.ReOrchestrationPlan{}
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	changed := false
	if latest.Status.StartTime.IsZero() {
		latest.Status.StartTime = metav1.Now()
		changed = true
	}
	if len(latest.Status.MoveStatuses) == 0 && len(latest.Spec.Moves) > 0 {
		latest.Status.MoveStatuses = make([]corev1alpha1.MoveStatus, 0, len(latest.Spec.Moves))
		for _, mv := range latest.Spec.Moves {
			latest.Status.MoveStatuses = append(latest.Status.MoveStatuses, corev1alpha1.MoveStatus{
				GlobalDeploymentRef: mv.GlobalDeploymentRef,
				ReplicaIndex:        mv.ReplicaIndex,
				Status:              corev1alpha1.MoveExecutionStatusPending,
			})
		}
		changed = true
	}
	if changed {
		if err := r.Status().Patch(ctx, latest, patch); err != nil {
			return err
		}
	}

	// Apply each move intent (idempotent).
	for i := range latest.Spec.Moves {
		mv := latest.Spec.Moves[i]
		rbName := fmt.Sprintf("%s-rb-%d", mv.GlobalDeploymentRef.Name, mv.ReplicaIndex)
		rbKey := types.NamespacedName{Name: rbName, Namespace: mv.GlobalDeploymentRef.Namespace}
		var rb corev1alpha1.ReplicaBinding
		if err := r.Get(ctx, rbKey, &rb); err != nil {
			_ = r.patchMoveStatus(ctx, latest, mv, corev1alpha1.MoveExecutionStatusFailed, fmt.Sprintf("ReplicaBinding %s not found: %v", rbKey.String(), err))
			continue
		}

		reqToken := r.buildRescheduleRequestToken(latest)
		needPatch := rb.Spec.TargetCluster != mv.Destination.ClusterID || rb.Spec.TargetNodeName != mv.Destination.NodeName || rb.Spec.Reschedule != true || rb.Spec.RescheduleRequest != reqToken
		if needPatch {
			// 1) Patch spec (main resource).
			specPatch := client.MergeFrom(rb.DeepCopy())
			rb.Spec.TargetCluster = mv.Destination.ClusterID
			rb.Spec.TargetNodeName = mv.Destination.NodeName
			rb.Spec.Reschedule = true
			rb.Spec.RescheduleRequest = reqToken
			if err := r.Patch(ctx, &rb, specPatch); err != nil {
				_ = r.patchMoveStatus(ctx, latest, mv, corev1alpha1.MoveExecutionStatusFailed, fmt.Sprintf("patch ReplicaBinding spec failed: %v", err))
				continue
			}

			// 2) Patch status (status subresource).
			// Re-get to avoid resourceVersion conflicts between spec patch and status patch.
			var rbLatest corev1alpha1.ReplicaBinding
			if err := r.Get(ctx, rbKey, &rbLatest); err != nil {
				_ = r.patchMoveStatus(ctx, latest, mv, corev1alpha1.MoveExecutionStatusFailed, fmt.Sprintf("re-get ReplicaBinding failed after spec patch: %v", err))
				continue
			}
			if rbLatest.Status.Phase != corev1alpha1.ReplicaBindingPhaseRescheduling {
				statusPatch := client.MergeFrom(rbLatest.DeepCopy())
				rbLatest.Status.Phase = corev1alpha1.ReplicaBindingPhaseRescheduling
				rbLatest.Status.LastTransitionTime = metav1.Now()
				rbLatest.Status.LastError = ""
				if err := r.Status().Patch(ctx, &rbLatest, statusPatch); err != nil {
					_ = r.patchMoveStatus(ctx, latest, mv, corev1alpha1.MoveExecutionStatusFailed, fmt.Sprintf("patch ReplicaBinding status failed: %v", err))
					continue
				}
			}
		}

		// Only move to InProgress from Pending so we don't override Succeeded/Failed computed by monitor.
		_ = r.patchMoveStatusIf(ctx, latest, mv, corev1alpha1.MoveExecutionStatusPending, corev1alpha1.MoveExecutionStatusInProgress, "reschedule intent applied")
	}

	_ = r.recomputePlanExecutionSummary(ctx, latest)
	return nil
}

func (r *ReOrchestrationPlanReconciler) patchMoveStatusIf(ctx context.Context, plan *corev1alpha1.ReOrchestrationPlan, mv corev1alpha1.PlanMove, from corev1alpha1.MoveExecutionStatus, to corev1alpha1.MoveExecutionStatus, msg string) error {
	if plan == nil {
		return nil
	}
	latest := &corev1alpha1.ReOrchestrationPlan{}
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	for i := range latest.Status.MoveStatuses {
		ms := &latest.Status.MoveStatuses[i]
		if ms.GlobalDeploymentRef.Name == mv.GlobalDeploymentRef.Name && ms.GlobalDeploymentRef.Namespace == mv.GlobalDeploymentRef.Namespace && ms.ReplicaIndex == mv.ReplicaIndex {
			if ms.Status == from {
				ms.Status = to
				ms.Message = msg
				movesTotal.WithLabelValues(string(to)).Inc()
				if to == corev1alpha1.MoveExecutionStatusSucceeded || to == corev1alpha1.MoveExecutionStatusFailed {
					// TODO: hook real per-move timing when moveStatuses includes timestamps.
					moveDuration.Observe(0)
				}
				return r.Status().Patch(ctx, latest, patch)
			}
			return nil
		}
	}
	return nil
}

func (r *ReOrchestrationPlanReconciler) patchMoveStatus(ctx context.Context, plan *corev1alpha1.ReOrchestrationPlan, mv corev1alpha1.PlanMove, st corev1alpha1.MoveExecutionStatus, msg string) error {
	if plan == nil {
		return nil
	}
	latest := &corev1alpha1.ReOrchestrationPlan{}
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	found := false
	for i := range latest.Status.MoveStatuses {
		ms := &latest.Status.MoveStatuses[i]
		if ms.GlobalDeploymentRef.Name == mv.GlobalDeploymentRef.Name && ms.GlobalDeploymentRef.Namespace == mv.GlobalDeploymentRef.Namespace && ms.ReplicaIndex == mv.ReplicaIndex {
			ms.Status = st
			ms.Message = msg
			found = true
			break
		}
	}
	if !found {
		latest.Status.MoveStatuses = append(latest.Status.MoveStatuses, corev1alpha1.MoveStatus{
			GlobalDeploymentRef: mv.GlobalDeploymentRef,
			ReplicaIndex:        mv.ReplicaIndex,
			Status:              st,
			Message:             msg,
		})
		movesTotal.WithLabelValues(string(st)).Inc()
		if st == corev1alpha1.MoveExecutionStatusSucceeded || st == corev1alpha1.MoveExecutionStatusFailed {
			// TODO: hook real per-move timing when moveStatuses includes timestamps.
			moveDuration.Observe(0)
		}
	}
	return r.Status().Patch(ctx, latest, patch)
}

func (r *ReOrchestrationPlanReconciler) recomputePlanExecutionSummary(ctx context.Context, plan *corev1alpha1.ReOrchestrationPlan) error {
	if plan == nil {
		return nil
	}
	latest := &corev1alpha1.ReOrchestrationPlan{}
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	var total, succ, fail, pend int32
	for _, ms := range latest.Status.MoveStatuses {
		total++
		switch ms.Status {
		case corev1alpha1.MoveExecutionStatusSucceeded:
			succ++
		case corev1alpha1.MoveExecutionStatusFailed:
			fail++
		case corev1alpha1.MoveExecutionStatusPending:
			pend++
		default:
			// InProgress treated as pending for summary.
			pend++
		}
	}
	latest.Status.Summary.TotalMoves = total
	latest.Status.Summary.SucceededMoves = succ
	latest.Status.Summary.FailedMoves = fail
	latest.Status.Summary.PendingMoves = pend
	return r.Status().Patch(ctx, latest, patch)
}

func (r *ReOrchestrationPlanReconciler) monitorAndConverge(ctx context.Context, plan *corev1alpha1.ReOrchestrationPlan) (ctrl.Result, error) {
	if plan == nil {
		return ctrl.Result{}, nil
	}
	latest := &corev1alpha1.ReOrchestrationPlan{}
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Ensure moveStatuses is initialized so patchMoveStatus() can transition states.
	// (executeMoves() does this too, but keep this here to be robust to partial updates.)
	if len(latest.Spec.Moves) > 0 && len(latest.Status.MoveStatuses) == 0 {
		patch := client.MergeFrom(latest.DeepCopy())
		latest.Status.MoveStatuses = make([]corev1alpha1.MoveStatus, 0, len(latest.Spec.Moves))
		for _, mv := range latest.Spec.Moves {
			latest.Status.MoveStatuses = append(latest.Status.MoveStatuses, corev1alpha1.MoveStatus{
				GlobalDeploymentRef: mv.GlobalDeploymentRef,
				ReplicaIndex:        mv.ReplicaIndex,
				Status:              corev1alpha1.MoveExecutionStatusPending,
			})
		}
		if err := r.Status().Patch(ctx, latest, patch); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	// Determine timeout.
	timeout := defaultPlanExecutionTimeout
	if latest.Annotations != nil {
		if v, ok := latest.Annotations[planExecutionTimeoutAnnotation]; ok {
			// Support either duration string (e.g. "10m") or seconds int.
			if d, err := time.ParseDuration(v); err == nil {
				timeout = d
			} else if secs, err2 := strconv.Atoi(v); err2 == nil {
				timeout = time.Duration(secs) * time.Second
			}
		}
	}
	if !latest.Status.StartTime.IsZero() {
		if time.Since(latest.Status.StartTime.Time) > timeout {
			// Mark all non-terminal moves as Failed.
			for i := range latest.Status.MoveStatuses {
				ms := &latest.Status.MoveStatuses[i]
				if ms.Status != corev1alpha1.MoveExecutionStatusSucceeded && ms.Status != corev1alpha1.MoveExecutionStatusFailed {
					ms.Status = corev1alpha1.MoveExecutionStatusFailed
					ms.Message = fmt.Sprintf("timeout after %s", timeout.String())
				}
			}
			// Converge to failed-ish phase.
			return r.convergePlanPhase(ctx, latest)
		}
	}

	// Update each move status based on ReplicaBinding status.
	for i := range latest.Spec.Moves {
		mv := latest.Spec.Moves[i]
		rbName := fmt.Sprintf("%s-rb-%d", mv.GlobalDeploymentRef.Name, mv.ReplicaIndex)
		rbKey := types.NamespacedName{Name: rbName, Namespace: mv.GlobalDeploymentRef.Namespace}
		var rb corev1alpha1.ReplicaBinding
		if err := r.Get(ctx, rbKey, &rb); err != nil {
			// Don't flip to failed immediately if RB temporarily missing; keep InProgress.
			continue
		}

		desired, msg := r.computeMoveStatus(latest, &rb, mv)

		_ = r.patchMoveStatus(ctx, latest, mv, desired, msg)
	}

	return r.convergePlanPhase(ctx, latest)
}

func (r *ReOrchestrationPlanReconciler) buildRescheduleRequestToken(plan *corev1alpha1.ReOrchestrationPlan) string {
	// token format per design doc: <policyNamespace>/<policyName>/<planName>
	if plan == nil {
		return ""
	}
	policyNS := plan.Namespace
	policyName := ""
	if plan.Spec.Summary.PolicyName != "" {
		policyName = plan.Spec.Summary.PolicyName
	}
	if plan.Labels != nil {
		if v, ok := plan.Labels["kubex.io/policy-namespace"]; ok && v != "" {
			policyNS = v
		}
		if v, ok := plan.Labels["kubex.io/policy-name"]; ok && v != "" {
			policyName = v
		}
	}

	parts := []string{policyNS, policyName, plan.Name}
	tok := strings.Trim(strings.Join(parts, "/"), "/")

	// Ensure uniqueness across multiple migrations of the same ReplicaBinding.
	// Appending plan UID preserves the base format and avoids collisions.
	if plan.UID != "" {
		return fmt.Sprintf("%s#%s", tok, plan.UID)
	}
	return tok
}

func (r *ReOrchestrationPlanReconciler) computeMoveStatus(plan *corev1alpha1.ReOrchestrationPlan, rb *corev1alpha1.ReplicaBinding, mv corev1alpha1.PlanMove) (corev1alpha1.MoveExecutionStatus, string) {
	if plan == nil || rb == nil {
		return corev1alpha1.MoveExecutionStatusInProgress, ""
	}
	// Hard fail still based on RB phase.
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseFailed {
		msg := rb.Status.LastError
		if msg == "" {
			msg = "replicabinding failed"
		}
		return corev1alpha1.MoveExecutionStatusFailed, msg
	}

	// 先确认是不是由该rop触发的
	actualReq := rb.Spec.RescheduleRequest
	expectedReq := r.buildRescheduleRequestToken(plan)
	if expectedReq != "" && actualReq != expectedReq {
		return corev1alpha1.MoveExecutionStatusInProgress, fmt.Sprintf("waiting for intent applied (wantRescheduleRequest=%s)", expectedReq)
	}
	// 如果rb status为Rescheduling或Applying，表示迁移还在进行中
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseRescheduling || rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseApplying {
		return corev1alpha1.MoveExecutionStatusInProgress, "waiting for reschedule handling"
	}
	// 如果rb status为Running，表示已经迁移完成
	if rb.Status.Phase == corev1alpha1.ReplicaBindingPhaseRunning {
		return corev1alpha1.MoveExecutionStatusSucceeded, "reschedule completed"
	}

	// Handled but no terminal result written: treat as in progress.
	return corev1alpha1.MoveExecutionStatusInProgress, "handled, waiting for terminal result"
}

func (r *ReOrchestrationPlanReconciler) convergePlanPhase(ctx context.Context, plan *corev1alpha1.ReOrchestrationPlan) (ctrl.Result, error) {
	if plan == nil {
		return ctrl.Result{}, nil
	}
	latest := &corev1alpha1.ReOrchestrationPlan{}
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Refresh summary.
	_ = r.recomputePlanExecutionSummary(ctx, latest)
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Name, Namespace: plan.Namespace}, latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// If spec.moves exists but moveStatuses isn't populated yet, we must keep Executing.
	if len(latest.Spec.Moves) > 0 && len(latest.Status.MoveStatuses) == 0 {
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
	}

	allTerminal := true
	anyFailed := false
	anySucceeded := false
	for _, ms := range latest.Status.MoveStatuses {
		if ms.Status != corev1alpha1.MoveExecutionStatusSucceeded && ms.Status != corev1alpha1.MoveExecutionStatusFailed {
			allTerminal = false
		}
		if ms.Status == corev1alpha1.MoveExecutionStatusFailed {
			anyFailed = true
		}
		if ms.Status == corev1alpha1.MoveExecutionStatusSucceeded {
			anySucceeded = true
		}
	}

	if !allTerminal {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	finalPhase := corev1alpha1.PlanPhaseSucceeded
	if anyFailed && anySucceeded {
		finalPhase = corev1alpha1.PlanPhasePartiallyFailed
	} else if anyFailed {
		finalPhase = corev1alpha1.PlanPhaseFailed
	}

	if latest.Status.Phase == finalPhase && !latest.Status.CompletionTime.IsZero() {
		return ctrl.Result{}, nil
	}

	patch := client.MergeFrom(latest.DeepCopy())
	latest.Status.Phase = finalPhase
	if latest.Status.CompletionTime.IsZero() {
		latest.Status.CompletionTime = metav1.Now()
	}
	cond := metav1.Condition{
		Type:               "ExecutionCompleted",
		ObservedGeneration: latest.GetGeneration(),
		LastTransitionTime: metav1.Now(),
		Status:             metav1.ConditionTrue,
		Reason:             string(finalPhase),
		Message:            fmt.Sprintf("plan finished with phase=%s", finalPhase),
	}
	apimeta.SetStatusCondition(&latest.Status.Conditions, cond)
	if err := r.Status().Patch(ctx, latest, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
