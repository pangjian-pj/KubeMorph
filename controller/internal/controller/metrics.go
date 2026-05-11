package controller

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	metricsOnce sync.Once

	plansCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "kubex",
		Name:      "reorchestration_plans_created_total",
		Help:      "Total number of ReOrchestrationPlan resources created by OptimizationPolicy controller.",
	})

	movesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "kubex",
		Name:      "reorchestration_moves_total",
		Help:      "Total number of plan moves processed by PlanExecutor, labeled by status.",
	}, []string{"status"})

	scoringPluginErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "kubex",
		Name:      "scoring_plugin_errors_total",
		Help:      "Total number of scoring plugin failures during optimization, labeled by plugin type.",
	}, []string{"plugin"})

	activePolicyGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "kubex",
		Name:      "active_optimization_policy",
		Help:      "Whether there is an active OptimizationPolicy (1) or not (0).",
	})

	calculationDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "kubex",
		Name:      "reorchestration_calculation_duration_seconds",
		Help:      "Duration of one optimization calculation cycle in seconds.",
		Buckets:   prometheus.DefBuckets,
	})

	moveDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "kubex",
		Name:      "reorchestration_move_duration_seconds",
		Help:      "Duration of one move execution in seconds.",
		Buckets:   prometheus.DefBuckets,
	})
)

func ensureMetricsRegistered(reg prometheus.Registerer) {
	metricsOnce.Do(func() {
		if reg == nil {
			reg = metrics.Registry
		}
		reg.MustRegister(plansCreated)
		reg.MustRegister(movesTotal)
		reg.MustRegister(scoringPluginErrors)
		reg.MustRegister(activePolicyGauge)
		reg.MustRegister(calculationDuration)
		reg.MustRegister(moveDuration)
	})
}
