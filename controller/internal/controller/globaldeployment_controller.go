/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"

	corev1alpha1 "github.com/pangjian-pj/KubeMorph/controller/api/v1alpha1"
	"github.com/pangjian-pj/KubeMorph/controller/internal/controller/multicluster"
)

const revisionLabelKey = "kubex.io/revision"
const replicaIndexLabelKey = "kubex.io/replicaIndex"

const globalDeploymentFinalizer = "core.kubex.io/finalizer-globaldeployment"

// GlobalDeploymentReconciler reconciles a GlobalDeployment object.
//
// Contract (MVP):
// - Input: GlobalDeployment.spec.replicas + spec.template
// - Output: A set of ReplicaBinding objects (one per replicaIndex)
// - Scheduler input: all Cluster.status.nodes (from Cluster CRs)
// - Executor: a ReplicaBinding controller will create a member Deployment with nodeAffinity.
type GlobalDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ControlNamespace is where Cluster CRs and kubeconfig Secrets live.
	// If empty, defaults to gd.Namespace.
	ControlNamespace string

	memberClients *multicluster.MemberClientCache

	// TANF cursor (per GlobalDeployment key). Kept in-memory for now.
	// NOTE: for production we'd persist this in status.
	cursorMu sync.Mutex
	cursors  map[string]int
}

func (r *GlobalDeploymentReconciler) getMemberClient(ctx context.Context, namespace string, clusterID string) (*kubernetes.Clientset, error) {
	if r.memberClients == nil {
		r.memberClients = multicluster.NewMemberClientCache()
	}
	return r.memberClients.GetOrBuild(ctx, r.Client, namespace, clusterID)
}

// +kubebuilder:rbac:groups=core.kubex.io,resources=globaldeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.kubex.io,resources=globaldeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.kubex.io,resources=replicabindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.kubex.io,resources=replicabindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.kubex.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (r *GlobalDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if r.memberClients == nil {
		r.memberClients = multicluster.NewMemberClientCache()
	}
	if r.cursors == nil {
		r.cursors = map[string]int{}
	}

	var gd corev1alpha1.GlobalDeployment
	if err := r.Get(ctx, req.NamespacedName, &gd); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion flow (finalizer-based):
	// 1) Ensure Deleting phase
	// 2) Delete all owned ReplicaBindings (which triggers RB controller to clean member resources)
	// 3) Wait until all owned ReplicaBindings are gone, then remove finalizer.
	if !gd.DeletionTimestamp.IsZero() {
		res, err := r.reconcileDelete(ctx, &gd)
		return res, err
	}

	// Ensure finalizer exists so we can orchestrate member cleanup during deletion.
	if !controllerutil.ContainsFinalizer(&gd, globalDeploymentFinalizer) {
		patch := client.MergeFrom(gd.DeepCopy())
		controllerutil.AddFinalizer(&gd, globalDeploymentFinalizer)
		if err := r.Patch(ctx, &gd, patch); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue quickly to proceed with rest of reconcile on latest object.
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	// Ensure observedRevision is initialized.
	if gd.Status.ObservedRevision == "" {
		gdPatch := client.MergeFrom(gd.DeepCopy())
		gd.Status.ObservedRevision = "v1"
		if err := r.Status().Patch(ctx, &gd, gdPatch); err != nil {
			return ctrl.Result{}, err
		}
		// Requeue quickly to proceed with the rest of reconcile with persisted status.
		return ctrl.Result{RequeueAfter: 100 * time.Millisecond}, nil
	}

	replicas := int32(1)
	if gd.Spec.Replicas != nil {
		replicas = *gd.Spec.Replicas
	}
	if replicas < 0 {
		replicas = 0
	}

	// List existing bindings
	var bindings corev1alpha1.ReplicaBindingList
	if err := r.List(ctx, &bindings, client.InNamespace(gd.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	owned := make([]corev1alpha1.ReplicaBinding, 0)
	for _, b := range bindings.Items {
		// Prefer controller ownership to avoid accidentally managing RBs that only reference this GD.
		for _, ref := range b.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == gd.UID {
				owned = append(owned, b)
				break
			}
		}
	}

	// Ensure 0..replicas-1 bindings exist
	existing := map[int32]*corev1alpha1.ReplicaBinding{}
	for i := range owned {
		b := owned[i]
		existing[b.Spec.ReplicaIndex] = &b
	}

	for i := int32(0); i < replicas; i++ {
		if existing[i] != nil {
			continue
		}
		b := &corev1alpha1.ReplicaBinding{
			TypeMeta: metav1.TypeMeta{
				APIVersion: corev1alpha1.GroupVersion.String(),
				Kind:       "ReplicaBinding",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-rb-%d", gd.Name, i),
				Namespace: gd.Namespace,
				Labels: map[string]string{
					"core.kubex.io/globaldeploy":  gd.Name,
					"core.kubex.io/replica-index": strconv.Itoa(int(i)),
				},
			},
			Spec: corev1alpha1.ReplicaBindingSpec{
				GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
				ReplicaIndex:        i,
			},
		}
		if err := ctrl.SetControllerReference(&gd, b, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		// Create RB with an explicit initial phase.
		// Design contract: RBs start at Pending (unscheduled). GD controller is responsible for
		// assigning targetCluster/targetNodeName and advancing phase to Assigned.
		b.Status.Phase = corev1alpha1.ReplicaBindingPhasePending
		b.Status.LastTransitionTime = metav1.Now()
		if err := r.Create(ctx, b); err != nil {
			return ctrl.Result{}, err
		}
		// Persist status via subresource update.
		// Some apiservers drop status fields on create unless status subresource is used.
		if err := r.Status().Update(ctx, b); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("created ReplicaBinding", "name", b.Name, "replicaIndex", i)
	}

	// TODO: scale down - delete bindings with replicaIndex >= replicas

	// Schedule Pending bindings
	orderedNodes, err := r.buildOrderedNodes(ctx)
	if err != nil {
		log.Error(err, "build ordered nodes failed")
		// No hard fail; wait next reconcile.
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	key := fmt.Sprintf("%s/%s", gd.Namespace, gd.Name)
	cursor := r.getCursor(key)

	for _, b := range owned {
		// Always re-GET latest binding object before making scheduling decision.
		// This avoids scheduling churn when RB controller advances phase to Applying/Running.
		var latest corev1alpha1.ReplicaBinding
		if err := r.Get(ctx, types.NamespacedName{Name: b.Name, Namespace: b.Namespace}, &latest); err != nil {
			return ctrl.Result{}, err
		}
		phase := latest.Status.Phase
		if phase == "" {
			phase = corev1alpha1.ReplicaBindingPhasePending
		}
		if phase != corev1alpha1.ReplicaBindingPhasePending {
			continue
		}
		slot, nextCursor, pickErr := pickNextFit(orderedNodes, cursor, func(n orderedNode) bool {
			// Filter: node must be Ready and has enough free resources.
			// Requests: derived from gd.Spec.Template.
			if !n.Ready {
				return false
			}
			reqRes := sumDeploymentPodRequests(&gd)
			return fits(n, reqRes)
		})
		if pickErr != nil {
			log.Info("no fit for replica", "binding", b.Name, "err", pickErr.Error())
			continue
		}

		// Update binding only if slot actually changes.
		// This avoids endless Update events (and RB requeue storms) when scheduler runs periodically.
		specChanged := latest.Spec.TargetCluster != slot.ClusterID || latest.Spec.TargetNodeName != slot.NodeName
		statusChanged := latest.Status.Phase != corev1alpha1.ReplicaBindingPhaseAssigned
		if specChanged {
			latest.Spec.TargetCluster = slot.ClusterID
			latest.Spec.TargetNodeName = slot.NodeName
			// IMPORTANT: spec fields must be persisted via Update() (status subresource update won't write spec).
			if err := r.Update(ctx, &latest); err != nil {
				return ctrl.Result{}, err
			}
		}
		if statusChanged {
			latest.Status.Phase = corev1alpha1.ReplicaBindingPhaseAssigned
			latest.Status.LastTransitionTime = metav1.Now()
			latest.Status.LastError = ""
			if err := r.Status().Update(ctx, &latest); err != nil {
				return ctrl.Result{}, err
			}
		}
		cursor = nextCursor
		r.setCursor(key, cursor)
		log.Info("assigned replica", "binding", latest.Name, "cluster", slot.ClusterID, "node", slot.NodeName)
	}

	// Aggregate phases
	// IMPORTANT: re-list latest ReplicaBinding objects before aggregating.
	// `owned` was built from an earlier List() and becomes stale quickly because
	// ReplicaBinding controller updates status to Applying/Running.
	var latestBindings corev1alpha1.ReplicaBindingList
	if err := r.List(ctx, &latestBindings, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	latestOwned := make([]corev1alpha1.ReplicaBinding, 0)
	for _, b := range latestBindings.Items {
		for _, ref := range b.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == gd.UID {
				latestOwned = append(latestOwned, b)
				break
			}
		}
	}

	status := corev1alpha1.GlobalDeploymentStatus{}
	status.UpdatedAt = metav1.Now()
	for _, b := range latestOwned {
		switch b.Status.Phase {
		case corev1alpha1.ReplicaBindingPhaseRunning:
			status.Running++
		case corev1alpha1.ReplicaBindingPhaseFailed:
			status.Failed++
		case corev1alpha1.ReplicaBindingPhaseApplying, corev1alpha1.ReplicaBindingPhaseAssigned:
			// Keep status schema stable: treat Applying/Assigned as Pending until Running.
			status.Pending++
		case corev1alpha1.ReplicaBindingPhasePending, "":
			status.Pending++
		}
	}
	// Phase computation (simplified, aligned with doc ordering)
	if status.Running == replicas && replicas > 0 {
		status.Phase = corev1alpha1.GlobalDeploymentPhaseRunning
	} else if status.Failed > 0 {
		status.Phase = corev1alpha1.GlobalDeploymentPhaseDegraded
	} else if status.Running > 0 {
		status.Phase = corev1alpha1.GlobalDeploymentPhaseProgressing
	} else {
		status.Phase = corev1alpha1.GlobalDeploymentPhasePending
	}

	gd.Status = status
	if err := r.updateGDStatusWithRetry(ctx, req.NamespacedName, &gd, status); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *GlobalDeploymentReconciler) reconcileDelete(ctx context.Context, gd *corev1alpha1.GlobalDeployment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if gd == nil {
		return ctrl.Result{}, nil
	}

	// If no finalizer, nothing to do.
	if !controllerutil.ContainsFinalizer(gd, globalDeploymentFinalizer) {
		return ctrl.Result{}, nil
	}

	// 1) Set phase=Deleting best-effort for UI.
	if gd.Status.Phase != corev1alpha1.GlobalDeploymentPhaseDeleting {
		patch := client.MergeFrom(gd.DeepCopy())
		gd.Status.Phase = corev1alpha1.GlobalDeploymentPhaseDeleting
		gd.Status.UpdatedAt = metav1.Now()
		_ = r.Status().Patch(ctx, gd, patch)
	}

	// 2) List and delete all owned ReplicaBindings.
	var rbs corev1alpha1.ReplicaBindingList
	if err := r.List(ctx, &rbs, client.InNamespace(gd.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	owned := make([]corev1alpha1.ReplicaBinding, 0)
	for _, b := range rbs.Items {
		for _, ref := range b.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == gd.UID {
				owned = append(owned, b)
				break
			}
		}
	}

	// Issue delete requests. Ignore not found; tolerate already-deleting.
	deletedAny := false
	for i := range owned {
		b := owned[i]
		if b.DeletionTimestamp != nil {
			continue
		}
		// gd controller删之前要检查rb状态是否为running或failed
		if b.Status.Phase == corev1alpha1.ReplicaBindingPhaseRunning || b.Status.Phase == corev1alpha1.ReplicaBindingPhaseFailed {
			err := r.Delete(ctx, &b)
			if err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			deletedAny = true
			log.Info("deleting globaldeployment: requested ReplicaBinding delete", "globaldeployment", gd.Name, "binding", b.Name)
		}
	}
	if deletedAny {
		// Give RB controller time to react and clean member resources.
		return ctrl.Result{RequeueAfter: 300 * time.Millisecond}, nil
	}

	// Re-list to see if any owned RBs still exist (including those already in deletion).
	var rbs2 corev1alpha1.ReplicaBindingList
	if err := r.List(ctx, &rbs2, client.InNamespace(gd.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	remaining := 0
	for _, b := range rbs2.Items {
		for _, ref := range b.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == gd.UID {
				remaining++
				break
			}
		}
	}
	if remaining > 0 {
		log.Info("deleting globaldeployment: waiting ReplicaBindings to be removed", "globaldeployment", gd.Name, "remaining", remaining)
		return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, nil
	}

	// 3) All owned RBs are gone. Remove finalizer to allow GD deletion to complete.
	patch := client.MergeFrom(gd.DeepCopy())
	controllerutil.RemoveFinalizer(gd, globalDeploymentFinalizer)
	if err := r.Patch(ctx, gd, patch); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("deleting globaldeployment: finalizer removed", "globaldeployment", gd.Name)
	return ctrl.Result{}, nil
}

func (r *GlobalDeploymentReconciler) updateGDStatusWithRetry(ctx context.Context, key types.NamespacedName, base *corev1alpha1.GlobalDeployment, st corev1alpha1.GlobalDeploymentStatus) error {
	// In real clusters, RB controller and other actors may update GD concurrently.
	// Status().Update will conflict if resourceVersion moves. Retry with latest.
	for i := 0; i < 5; i++ {
		latest := &corev1alpha1.GlobalDeployment{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		latest.Status = st
		if err := r.Status().Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("update globaldeployment status conflict retries exceeded")
}

// handleRescheduleMVP keeps the original MVP skeleton (RB-status-driven).
// It's no longer used by reconcile (replaced by handleRescheduleRolling), but kept for reference.
// Return (result, acted, err).
func (r *GlobalDeploymentReconciler) handleRescheduleMVP(ctx context.Context, gd *corev1alpha1.GlobalDeployment, owned []corev1alpha1.ReplicaBinding) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)
	if gd == nil {
		return ctrl.Result{}, false, nil
	}

	// 1) find trigger RBs: reschedule=true
	for _, rb := range owned {
		if !rb.Spec.Reschedule {
			continue
		}
		// trigger RB should have a concrete destination
		if rb.Spec.TargetCluster == "" || rb.Spec.TargetNodeName == "" {
			continue
		}

		nextRev := bumpRevision(gd.Status.ObservedRevision)
		newRBName := fmt.Sprintf("%s-rb-%d-%s", gd.Name, rb.Spec.ReplicaIndex, nextRev)

		// 2) ensure new revision RB exists
		var newRB corev1alpha1.ReplicaBinding
		nErr := r.Get(ctx, types.NamespacedName{Name: newRBName, Namespace: gd.Namespace}, &newRB)
		if nErr != nil {
			if client.IgnoreNotFound(nErr) != nil {
				return ctrl.Result{}, true, nErr
			}
			create := &corev1alpha1.ReplicaBinding{
				TypeMeta: metav1.TypeMeta{APIVersion: corev1alpha1.GroupVersion.String(), Kind: "ReplicaBinding"},
				ObjectMeta: metav1.ObjectMeta{
					Name:      newRBName,
					Namespace: gd.Namespace,
					Labels: map[string]string{
						revisionLabelKey:     nextRev,
						replicaIndexLabelKey: strconv.Itoa(int(rb.Spec.ReplicaIndex)),
					},
				},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        rb.Spec.ReplicaIndex,
					TargetCluster:       rb.Spec.TargetCluster,
					TargetNodeName:      rb.Spec.TargetNodeName,
					Reschedule:          false,
				},
			}
			if err := ctrl.SetControllerReference(gd, create, r.Scheme); err != nil {
				return ctrl.Result{}, true, err
			}
			// Put it into execution state so RB controller will apply.
			create.Status.Phase = corev1alpha1.ReplicaBindingPhaseAssigned
			create.Status.LastTransitionTime = metav1.Now()
			if err := r.Create(ctx, create); err != nil {
				return ctrl.Result{}, true, err
			}
			if err := r.Status().Update(ctx, create); err != nil {
				return ctrl.Result{}, true, err
			}
			log.Info("created migration ReplicaBinding", "gd", gd.Name, "trigger", rb.Name, "newRB", newRBName, "revision", nextRev)
			return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, true, nil
		}

		// 3) wait new revision RB Running
		if newRB.Status.Phase != corev1alpha1.ReplicaBindingPhaseRunning {
			return ctrl.Result{RequeueAfter: 500 * time.Millisecond}, true, nil
		}

		// 4) delete old revision RBs (same replicaIndex, different revision name)
		for _, candidate := range owned {
			if candidate.Name == rb.Name || candidate.Name == newRBName {
				continue
			}
			if candidate.Spec.ReplicaIndex != rb.Spec.ReplicaIndex {
				continue
			}
			// only consider revision-shaped RB names: <gd>-r<idx>-vX
			if !strings.HasPrefix(candidate.Name, fmt.Sprintf("%s-rb-%d-", gd.Name, rb.Spec.ReplicaIndex)) {
				continue
			}
			_ = r.Delete(ctx, &candidate)
		}

		// 5) reset trigger RB.spec.reschedule=false
		var latestTrigger corev1alpha1.ReplicaBinding
		if err := r.Get(ctx, types.NamespacedName{Name: rb.Name, Namespace: rb.Namespace}, &latestTrigger); err == nil {
			if latestTrigger.Spec.Reschedule {
				patch := client.MergeFrom(latestTrigger.DeepCopy())
				latestTrigger.Spec.Reschedule = false
				if err := r.Patch(ctx, &latestTrigger, patch); err != nil {
					return ctrl.Result{}, true, err
				}
			}
		}

		// 6) advance GD observedRevision
		gd2 := &corev1alpha1.GlobalDeployment{}
		if err := r.Get(ctx, types.NamespacedName{Name: gd.Name, Namespace: gd.Namespace}, gd2); err == nil {
			gdPatch := client.MergeFrom(gd2.DeepCopy())
			gd2.Status.ObservedRevision = nextRev
			if err := r.Status().Patch(ctx, gd2, gdPatch); err != nil {
				return ctrl.Result{}, true, err
			}
		}

		log.Info("migration completed (MVP)", "gd", gd.Name, "replicaIndex", rb.Spec.ReplicaIndex, "revision", nextRev)
		return ctrl.Result{RequeueAfter: 200 * time.Millisecond}, true, nil
	}
	return ctrl.Result{}, false, nil
}

// handleRescheduleRolling implements M6 7.1 rolling migration driven by RB.spec.reschedule.
// It uses member-cluster ground truth for readiness (Deployment + Pod).
// Return (result, acted, err).
func (r *GlobalDeploymentReconciler) handleRescheduleRolling(ctx context.Context, gd *corev1alpha1.GlobalDeployment, owned []corev1alpha1.ReplicaBinding) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)
	if gd == nil {
		return ctrl.Result{}, false, nil
	}

	// The `owned` slice is built from an earlier List() in Reconcile and may get stale quickly.
	// Since rolling-migration is a state machine driven by RB.spec.reschedule and we need to
	// observe newly created revision RBs, always re-list the latest RBs for this GD here.
	{
		var latest corev1alpha1.ReplicaBindingList
		if err := r.List(ctx, &latest, client.InNamespace(gd.Namespace)); err == nil {
			owned = owned[:0]
			for _, b := range latest.Items {
				for _, ref := range b.OwnerReferences {
					if ref.Controller != nil && *ref.Controller && ref.UID == gd.UID {
						owned = append(owned, b)
						break
					}
				}
			}
		}
	}

	controlNS := r.ControlNamespace
	if controlNS == "" {
		controlNS = gd.Namespace
	}

	for _, trigger := range owned {
		// Trigger is driven by rescheduleRequest token + reschedule flag (backward compatible).
		if trigger.Spec.RescheduleRequest == "" {
			continue
		}
		if !trigger.Spec.Reschedule {
			continue
		}
		// Idempotency gate: if this request token has already been handled successfully,
		// don't create more rolling-migration revisions.
		if trigger.Status.Reschedule.LastHandledRequest == trigger.Spec.RescheduleRequest &&
			trigger.Status.Reschedule.LastResult == corev1alpha1.RescheduleResultSucceeded {
			continue
		}
		if trigger.Spec.TargetCluster == "" || trigger.Spec.TargetNodeName == "" {
			continue
		}

		// Idempotency (strong): for the same replicaIndex + request token,
		// reuse an existing revision RB if already created.
		// Without this, a fast requeue loop can keep creating vNNN RBs before the first one becomes ready.
		var existingRevisionRBName string
		for i := range owned {
			cand := owned[i]
			if cand.Spec.ReplicaIndex != trigger.Spec.ReplicaIndex {
				continue
			}
			if cand.Name == trigger.Name {
				continue
			}
			if cand.Spec.RescheduleRequest != trigger.Spec.RescheduleRequest {
				continue
			}
			// Only consider RBs that look like rolling revisions (have revision label).
			if cand.Labels == nil || cand.Labels[revisionLabelKey] == "" {
				continue
			}
			existingRevisionRBName = cand.Name
			break
		}

		// Next revision must be per-replicaIndex.
		// Using a single GD-level observedRevision causes "v" numbers to jump across different replicas.
		nextRev := nextRevisionForReplicaIndex(owned, trigger.Spec.ReplicaIndex)
		newRBName := fmt.Sprintf("%s-rb-%d-%s", gd.Name, trigger.Spec.ReplicaIndex, nextRev)
		if existingRevisionRBName != "" {
			newRBName = existingRevisionRBName
		}

		// 1) Ensure new revision RB exists in control-plane.
		var newRB corev1alpha1.ReplicaBinding
		getErr := r.Get(ctx, types.NamespacedName{Name: newRBName, Namespace: gd.Namespace}, &newRB)
		if getErr != nil {
			if client.IgnoreNotFound(getErr) != nil {
				return ctrl.Result{}, true, getErr
			}

			create := &corev1alpha1.ReplicaBinding{
				TypeMeta: metav1.TypeMeta{APIVersion: corev1alpha1.GroupVersion.String(), Kind: "ReplicaBinding"},
				ObjectMeta: metav1.ObjectMeta{
					Name:      newRBName,
					Namespace: gd.Namespace,
					Labels: map[string]string{
						// Keep consistent with base (trigger) RB labels so that List(MatchingLabels{core.kubex.io/globaldeploy: gd.Name})
						// can see revision RBs too. Otherwise they will never be included in `owned` and thus won't be GC-ed.
						"core.kubex.io/globaldeploy": gd.Name,
						revisionLabelKey:             nextRev,
						replicaIndexLabelKey:         strconv.Itoa(int(trigger.Spec.ReplicaIndex)),
					},
				},
				Spec: corev1alpha1.ReplicaBindingSpec{
					GlobalDeploymentRef: corev1alpha1.NamespacedObjectRef{Name: gd.Name, Namespace: gd.Namespace},
					ReplicaIndex:        trigger.Spec.ReplicaIndex,
					TargetCluster:       trigger.Spec.TargetCluster,
					TargetNodeName:      trigger.Spec.TargetNodeName,
					RescheduleRequest:   trigger.Spec.RescheduleRequest,
					Reschedule:          false,
				},
			}
			if err := ctrl.SetControllerReference(gd, create, r.Scheme); err != nil {
				return ctrl.Result{}, true, err
			}
			create.Status.Phase = corev1alpha1.ReplicaBindingPhaseAssigned
			create.Status.LastTransitionTime = metav1.Now()
			if err := r.Create(ctx, create); err != nil {
				return ctrl.Result{}, true, err
			}
			if err := r.Status().Update(ctx, create); err != nil {
				return ctrl.Result{}, true, err
			}
			log.Info("created rolling migration ReplicaBinding", "gd", gd.Name, "trigger", trigger.Name, "newRB", newRBName, "revision", nextRev)
			return ctrl.Result{RequeueAfter: 300 * time.Millisecond}, true, nil
		}

		// 2) Ready gate (member cluster ground truth): Deployment + Pod readiness.
		memberCS, err := r.getMemberClient(ctx, controlNS, newRB.Spec.TargetCluster)
		if err != nil {
			log.Info("rolling migration: get member client failed", "cluster", newRB.Spec.TargetCluster, "err", err.Error())
			return ctrl.Result{RequeueAfter: 2 * time.Second}, true, nil
		}
		depName := fmt.Sprintf("%s-r%d", gd.Name, newRB.Spec.ReplicaIndex)
		dep, err := memberCS.AppsV1().Deployments(gd.Namespace).Get(ctx, depName, metav1.GetOptions{})
		if err != nil {
			log.Info("rolling migration: member deployment not found yet", "cluster", newRB.Spec.TargetCluster, "deployment", depName, "err", err.Error())
			return ctrl.Result{RequeueAfter: 1 * time.Second}, true, nil
		}
		if dep.Status.AvailableReplicas < 1 || dep.Status.UpdatedReplicas < 1 {
			log.Info("rolling migration: member deployment not ready yet", "deployment", depName, "available", dep.Status.AvailableReplicas, "updated", dep.Status.UpdatedReplicas)
			return ctrl.Result{RequeueAfter: 800 * time.Millisecond}, true, nil
		}
		// Ready gate pods: don't hard-depend on a specific label key (e.g. app.kubernetes.io/name).
		// In real clusters, the workload template may not carry that label at all.
		// Primary: use deployment.spec.selector.matchLabels (the authoritative RS selector).
		// Fallback: list all pods for this GlobalDeployment and filter by pod name prefix (<depName>-).
		selector := ""
		if dep.Spec.Selector != nil && len(dep.Spec.Selector.MatchLabels) > 0 {
			parts := make([]string, 0, len(dep.Spec.Selector.MatchLabels))
			for k, v := range dep.Spec.Selector.MatchLabels {
				// matchLabels are exact matches; safe to format as k=v.
				parts = append(parts, fmt.Sprintf("%s=%s", k, v))
			}
			selector = strings.Join(parts, ",")
		}
		pods, err := memberCS.CoreV1().Pods(gd.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			log.Info("rolling migration: list pods failed", "selector", selector, "err", err.Error())
			return ctrl.Result{RequeueAfter: 800 * time.Millisecond}, true, nil
		}
		if len(pods.Items) == 0 {
			pods2, err2 := memberCS.CoreV1().Pods(gd.Namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("core.kubex.io/globaldeploy=%s", gd.Name)})
			if err2 == nil {
				filtered := make([]corev1.Pod, 0, len(pods2.Items))
				prefix := depName + "-"
				for i := range pods2.Items {
					p := pods2.Items[i]
					if p.Name == depName || strings.HasPrefix(p.Name, prefix) {
						filtered = append(filtered, p)
					}
				}
				pods.Items = filtered
			}
		}
		log.Info("rolling migration: checking pods", "selector", selector, "count", len(pods.Items))
		readyPod := false
		observedPodName := ""
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase != corev1.PodRunning {
				continue
			}
			if len(p.Status.ContainerStatuses) == 0 {
				continue
			}
			allReady := true
			for _, cs := range p.Status.ContainerStatuses {
				if cs.Ready {
					continue
				}
				allReady = false
				break
			}
			if allReady {
				readyPod = true
				observedPodName = p.Name
				break
			}
		}
		if !readyPod {
			log.Info("rolling migration: no ready pod yet", "selector", selector)
			return ctrl.Result{RequeueAfter: 800 * time.Millisecond}, true, nil
		}

		// 3) Delete old revision RBs (same replicaIndex, previous revision) in control-plane.
		// We delete by revision label to avoid accidentally deleting unrelated RBs.
		for _, candidate := range owned {
			if candidate.Name == trigger.Name || candidate.Name == newRBName {
				continue
			}
			if candidate.Spec.ReplicaIndex != trigger.Spec.ReplicaIndex {
				continue
			}
			candRev := ""
			if candidate.Labels != nil {
				candRev = candidate.Labels[revisionLabelKey]
			}
			// Only delete RBs that are part of the rolling revision set and are not the new revision.
			// (Trigger RB typically has no revision label and should never be deleted here.)
			if candRev == "" || candRev == nextRev {
				continue
			}
			_ = r.Delete(ctx, &candidate)
		}

		// 4) Mark trigger RB status: watermark + observed outcome.
		if err := r.markRescheduleStatusSucceeded(ctx, &trigger, observedPodName); err != nil {
			return ctrl.Result{}, true, err
		}

		// 5) Per-replicaIndex revision: do not use a GD-level observedRevision for bumps.
		// (Revisions are carried by RB labels; the next revision is derived from existing RBs.)

		// Reset trigger RB.spec.reschedule to avoid re-entering rolling state machine.
		// (The ack is in status.reschedule watermark; reschedule flag is legacy/backward-compat.)
		if trigger.Spec.Reschedule {
			latestTrigger := &corev1alpha1.ReplicaBinding{}
			if err := r.Get(ctx, types.NamespacedName{Name: trigger.Name, Namespace: trigger.Namespace}, latestTrigger); err == nil {
				if latestTrigger.Spec.Reschedule {
					patch := client.MergeFrom(latestTrigger.DeepCopy())
					latestTrigger.Spec.Reschedule = false
					_ = r.Patch(ctx, latestTrigger, patch)
				}
			}
		}
		log.Info("rolling migration completed", "gd", gd.Name, "replicaIndex", trigger.Spec.ReplicaIndex, "revision", nextRev)
		return ctrl.Result{RequeueAfter: 300 * time.Millisecond}, true, nil
	}

	return ctrl.Result{}, false, nil
}

func (r *GlobalDeploymentReconciler) bumpObservedRevision(ctx context.Context, gd *corev1alpha1.GlobalDeployment, nextRev string) error {
	if gd == nil {
		return nil
	}
	for i := 0; i < 10; i++ {
		latest := &corev1alpha1.GlobalDeployment{}
		if err := r.Get(ctx, types.NamespacedName{Name: gd.Name, Namespace: gd.Namespace}, latest); err != nil {
			return err
		}
		if latest.Status.ObservedRevision == nextRev {
			return nil
		}
		latest.Status.ObservedRevision = nextRev
		// Use Status().Update instead of Patch.
		// In envtest, Status-Patch occasionally becomes flaky with CRDs and cached clients.
		// Update is simpler and we already handle conflicts via Get+retry.
		if err := r.Status().Update(ctx, latest); err != nil {
			if apierrors.IsConflict(err) {
				time.Sleep(30 * time.Millisecond)
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("failed to bump observedRevision to %s due to repeated conflict", nextRev)
}

func (r *GlobalDeploymentReconciler) markRescheduleStatusSucceeded(ctx context.Context, trigger *corev1alpha1.ReplicaBinding, observedPodName string) error {
	if trigger == nil {
		return nil
	}
	latest := &corev1alpha1.ReplicaBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: trigger.Name, Namespace: trigger.Namespace}, latest); err != nil {
		return client.IgnoreNotFound(err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	latest.Status.Reschedule.LastHandledRequest = latest.Spec.RescheduleRequest
	latest.Status.Reschedule.LastHandledTime = metav1.Now()
	latest.Status.Reschedule.LastResult = corev1alpha1.RescheduleResultSucceeded
	latest.Status.Reschedule.Message = "reschedule completed"
	latest.Status.Reschedule.LastError = ""
	latest.Status.Reschedule.ObservedLocation.ClusterId = latest.Spec.TargetCluster
	latest.Status.Reschedule.ObservedLocation.NodeName = latest.Spec.TargetNodeName
	if observedPodName != "" {
		latest.Status.Reschedule.ObservedLocation.PodName = observedPodName
	}
	return r.Status().Patch(ctx, latest, patch)
}

func nextRevisionForReplicaIndex(owned []corev1alpha1.ReplicaBinding, replicaIndex int32) string {
	maxN := 0
	for i := range owned {
		b := owned[i]
		if b.Spec.ReplicaIndex != replicaIndex {
			continue
		}
		if b.Labels == nil {
			continue
		}
		rev := b.Labels[revisionLabelKey]
		if rev == "" {
			// Trigger RBs typically don't have a revision label and must not affect revision bump.
			continue
		}
		// Expected format: v<N>
		if len(rev) < 2 || rev[0] != 'v' {
			continue
		}
		n, err := strconv.Atoi(rev[1:])
		if err != nil {
			continue
		}
		if n > maxN {
			maxN = n
		}
	}
	return fmt.Sprintf("v%d", maxN+1)
}

func bumpRevision(cur string) string {
	if cur == "" {
		return "v1"
	}
	if strings.HasPrefix(cur, "v") {
		n, err := strconv.Atoi(strings.TrimPrefix(cur, "v"))
		if err == nil {
			return fmt.Sprintf("v%d", n+1)
		}
	}
	// fallback
	return "v2"
}

func (r *GlobalDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.GlobalDeployment{}).
		Owns(&corev1alpha1.ReplicaBinding{}).
		Named("globaldeployment").
		Complete(r)
}

// orderedNode is a scheduler view of a node.
type orderedNode struct {
	ClusterID string
	NodeName  string
	Ready     bool
	FreeCPU   resource.Quantity
	FreeMem   resource.Quantity
}

type reqResources struct {
	CPU resource.Quantity
	Mem resource.Quantity
}

func (r *GlobalDeploymentReconciler) buildOrderedNodes(ctx context.Context) ([]orderedNode, error) {
	// Scheduler input: all Cluster CRs in all namespaces? For now: same namespace as GD.
	// We list across all namespaces could be large; design can be refined later.
	var list corev1alpha1.ClusterList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}

	// Filter: reachable, lastProbeTime fresh, node Ready, enough free resources.
	// TTL: 2 minutes (configurable later)
	ttl := 2 * time.Minute
	now := time.Now()

	byCluster := map[string][]orderedNode{}
	for _, c := range list.Items {
		// Only Ready clusters
		if c.Status.Phase != corev1alpha1.ClusterPhaseReady {
			continue
		}
		if !c.Status.LastProbeTime.IsZero() {
			if now.Sub(c.Status.LastProbeTime.Time) > ttl {
				continue
			}
		}
		clusterID := c.Name
		for _, n := range c.Status.Nodes {
			// Basic sanity filters.
			if !n.Ready {
				continue
			}
			if n.Name == "" {
				continue
			}
			// If free resources aren't reported, skip to avoid scheduling onto unknown capacity.
			if n.Free.CPU.IsZero() && n.Free.Memory.IsZero() {
				continue
			}
			item := orderedNode{
				ClusterID: clusterID,
				NodeName:  n.Name,
				Ready:     n.Ready,
				FreeCPU:   n.Free.CPU.DeepCopy(),
				FreeMem:   n.Free.Memory.DeepCopy(),
			}
			byCluster[clusterID] = append(byCluster[clusterID], item)
		}
	}

	// Sort clusterIDs
	clusterIDs := make([]string, 0, len(byCluster))
	for id := range byCluster {
		clusterIDs = append(clusterIDs, id)
	}
	sort.Strings(clusterIDs)

	// Sort nodes within cluster by free resource (desc)
	for _, id := range clusterIDs {
		ns := byCluster[id]
		sort.Slice(ns, func(i, j int) bool {
			// prefer larger free CPU, then larger free Mem
			c := ns[i].FreeCPU.Cmp(ns[j].FreeCPU)
			if c != 0 {
				return c > 0
			}
			return ns[i].FreeMem.Cmp(ns[j].FreeMem) > 0
		})
		byCluster[id] = ns
	}

	// Interleave: [A1,B1,C1,A2,B2,A3]
	maxLen := 0
	for _, id := range clusterIDs {
		if l := len(byCluster[id]); l > maxLen {
			maxLen = l
		}
	}
	ordered := make([]orderedNode, 0)
	for i := 0; i < maxLen; i++ {
		for _, id := range clusterIDs {
			ns := byCluster[id]
			if i < len(ns) {
				ordered = append(ordered, ns[i])
			}
		}
	}
	return ordered, nil
}

func fits(n orderedNode, req reqResources) bool {
	if req.CPU.IsZero() && req.Mem.IsZero() {
		return true
	}
	if n.FreeCPU.Cmp(req.CPU) < 0 {
		return false
	}
	if n.FreeMem.Cmp(req.Mem) < 0 {
		return false
	}
	return true
}

func sumDeploymentPodRequests(gd *corev1alpha1.GlobalDeployment) reqResources {
	// We interpret gd.Spec.Template as DeploymentSpec YAML/JSON stored in RawExtension.
	// Requests are summed from container resource requests.
	depSpec, err := decodeDeploymentSpecFromRaw(gd.Spec.Template)
	if err != nil {
		// If template can't be decoded, treat it as 0 requests so scheduler doesn't crash.
		// (A separate validation/webhook can enforce stricter checks later.)
		return reqResources{}
	}
	var cpu, mem resource.Quantity
	for _, c := range depSpec.Template.Spec.Containers {
		if c.Resources.Requests != nil {
			if q, ok := c.Resources.Requests["cpu"]; ok {
				cpu.Add(q)
			}
			if q, ok := c.Resources.Requests["memory"]; ok {
				mem.Add(q)
			}
		}
	}
	// NOTE: initContainers are ignored in MVP.
	return reqResources{CPU: cpu, Mem: mem}
}

func decodeDeploymentSpecFromRaw(raw runtime.RawExtension) (appsv1.DeploymentSpec, error) {
	var depSpec appsv1.DeploymentSpec
	if len(raw.Raw) == 0 {
		return depSpec, errors.New("empty template")
	}
	// raw.Raw may contain either YAML (from kubeX-server) or JSON.
	if err := yaml.Unmarshal(raw.Raw, &depSpec); err != nil {
		return depSpec, err
	}
	return depSpec, nil
}

// pickNextFit implements Topology-aware Next Fit (TANF).
// It scans ordered nodes starting from cursor+1 (wrap around) and picks the first that passes ok().
func pickNextFit(order []orderedNode, cursor int, ok func(orderedNode) bool) (orderedNode, int, error) {
	if len(order) == 0 {
		return orderedNode{}, cursor, errors.New("no candidate nodes")
	}
	start := cursor + 1
	for i := 0; i < len(order); i++ {
		idx := (start + i) % len(order)
		if ok(order[idx]) {
			return order[idx], idx, nil
		}
	}
	return orderedNode{}, cursor, errors.New("no fit node")
}

func (r *GlobalDeploymentReconciler) getCursor(key string) int {
	r.cursorMu.Lock()
	defer r.cursorMu.Unlock()
	return r.cursors[key]
}

func (r *GlobalDeploymentReconciler) setCursor(key string, v int) {
	r.cursorMu.Lock()
	defer r.cursorMu.Unlock()
	r.cursors[key] = v
}
