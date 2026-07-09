package plugin

import (
	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
	"github.com/argoproj/argo-rollouts/utils/evaluate"
	"github.com/argoproj/argo-rollouts/utils/plugin/types"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProviderType is the metric provider name reported back to the controller.
const ProviderType = "Instana"

// PluginName is the key used both in the argo-rollouts-config ConfigMap entry
// and as the map key under metric.provider.plugin in the AnalysisTemplate.
const PluginName = "argoproj-labs/instana"

// RpcPlugin implements the Argo Rollouts MetricProviderPlugin interface.
type RpcPlugin struct {
	LogCtx *log.Entry
}

// InitPlugin is called once when the controller loads the plugin.
func (p *RpcPlugin) InitPlugin() types.RpcError {
	return types.RpcError{}
}

// Run executes a single measurement: query Instana, then let the shared
// evaluator decide the phase against the metric's success/failure conditions.
func (p *RpcPlugin) Run(_ *v1alpha1.AnalysisRun, metric v1alpha1.Metric) v1alpha1.Measurement {
	start := metav1.Now()

	cfg, err := parseConfig(metric)
	if err != nil {
		return errorMeasurement(start, err)
	}

	client, err := newInstanaClient(cfg)
	if err != nil {
		return errorMeasurement(start, err)
	}

	value, err := client.getMetricValue(cfg)
	if err != nil {
		return errorMeasurement(start, err)
	}

	finish := metav1.Now()
	measurement := v1alpha1.Measurement{
		StartedAt:  &start,
		FinishedAt: &finish,
		Value:      formatValue(value),
	}
	phase, evalErr := evaluate.EvaluateResult(value, metric, *p.LogCtx)
	measurement.Phase = phase
	if evalErr != nil {
		measurement.Message = evalErr.Error()
	}
	return measurement
}

// Resume is unused for synchronous (single request/response) metrics.
func (p *RpcPlugin) Resume(_ *v1alpha1.AnalysisRun, _ v1alpha1.Metric, measurement v1alpha1.Measurement) v1alpha1.Measurement {
	return measurement
}

// Terminate is unused for synchronous metrics.
func (p *RpcPlugin) Terminate(_ *v1alpha1.AnalysisRun, _ v1alpha1.Metric, measurement v1alpha1.Measurement) v1alpha1.Measurement {
	return measurement
}

// GarbageCollect has nothing to clean up; Instana holds the metric data.
func (p *RpcPlugin) GarbageCollect(_ *v1alpha1.AnalysisRun, _ v1alpha1.Metric, _ int) types.RpcError {
	return types.RpcError{}
}

func (p *RpcPlugin) Type() string {
	return ProviderType
}

// GetMetadata surfaces the resolved query in the AnalysisRun for debugging.
func (p *RpcPlugin) GetMetadata(metric v1alpha1.Metric) map[string]string {
	metadata := map[string]string{}
	cfg, err := parseConfig(metric)
	if err != nil {
		return metadata
	}
	metadata["instanaMetric"] = cfg.Metric
	metadata["aggregation"] = cfg.Aggregation
	metadata["host"] = cfg.Host
	return metadata
}

func errorMeasurement(start metav1.Time, err error) v1alpha1.Measurement {
	finish := metav1.Now()
	return v1alpha1.Measurement{
		StartedAt:  &start,
		FinishedAt: &finish,
		Phase:      v1alpha1.AnalysisPhaseError,
		Message:    err.Error(),
	}
}
