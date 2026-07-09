package plugin

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/argoproj/argo-rollouts/pkg/apis/rollouts/v1alpha1"
)

// Config is the per-metric plugin configuration supplied under
// metric.provider.plugin["instana/metrics"] in an AnalysisTemplate.
type Config struct {
	// Host is the Instana tenant base URL, e.g. https://acme-prod.instana.io
	Host string `json:"host"`
	// Metric is the Instana metric id, e.g. "errors", "latency", "calls".
	Metric string `json:"metric"`
	// Aggregation is the rollup Instana applies, e.g. "MEAN", "SUM", "P90".
	// Sent upper-case; Instana echoes it back lower-cased in the series key.
	Aggregation string `json:"aggregation"`
	// Granularity is the data-point size in seconds. It forms part of the
	// returned series key, e.g. "errors.mean.60".
	Granularity int `json:"granularity,omitempty"`
	// TimeWindowMs is the look-back window for the query, in milliseconds.
	TimeWindowMs int64 `json:"timeWindowMs,omitempty"`

	// GroupByTag / GroupByTagKey / GroupByTagEntity control how call groups are
	// formed. The defaults split traffic by Argo Rollouts pod-template-hash,
	// which is what separates the canary variant from the stable one.
	GroupByTag       string `json:"groupByTag,omitempty"`
	GroupByTagKey    string `json:"groupByTagKey,omitempty"`
	GroupByTagEntity string `json:"groupByTagEntity,omitempty"`

	// SelectGroup picks one group by name when the query returns several
	// (e.g. the canary hash when both canary and stable are reporting).
	SelectGroup string `json:"selectGroup,omitempty"`

	// APITokenEnv is the env var on the rollouts controller holding the API
	// token. Defaults to INSTANA_API_TOKEN.
	APITokenEnv string `json:"apiTokenEnv,omitempty"`
	// TagFilters scope the query, e.g. to the canary pod-template-hash.
	TagFilters []TagFilter `json:"tagFilters,omitempty"`
	// PageSize bounds the number of groups returned. Must be >= 1.
	PageSize int `json:"pageSize,omitempty"`
	// Insecure disables TLS verification (test clusters only).
	Insecure bool `json:"insecure,omitempty"`
}

// TagFilter is a single Instana tag-filter leaf.
type TagFilter struct {
	Name     string `json:"name"`
	Operator string `json:"operator"` // EQUALS, NOT_EQUAL, CONTAINS, ...
	Value    string `json:"value"`
	Entity   string `json:"entity,omitempty"` // DESTINATION, SOURCE, NOT_APPLICABLE
	// Key is required when Name is a KEY_VALUE_PAIR tag such as
	// kubernetes.pod.label. It names the label key, e.g.
	// "rollouts-pod-template-hash". Omit for STRING tags.
	Key string `json:"key,omitempty"`
}

const (
	defaultAPITokenEnv      = "INSTANA_API_TOKEN"
	defaultGroupByTag       = "kubernetes.pod.label"
	defaultGroupByTagKey    = "rollouts-pod-template-hash"
	defaultGroupByTagEntity = "DESTINATION"
	defaultGranularity      = 60
	defaultTimeWindowMs     = 60000
	defaultPageSize         = 20
)

func parseConfig(metric v1alpha1.Metric) (*Config, error) {
	if metric.Provider.Plugin == nil {
		return nil, fmt.Errorf("metric %q has no plugin provider config", metric.Name)
	}
	raw, ok := metric.Provider.Plugin[PluginName]
	if !ok {
		return nil, fmt.Errorf("metric %q is missing provider.plugin[%q] config", metric.Name, PluginName)
	}

	cfg := &Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %q config: %w", PluginName, err)
	}

	if cfg.Host == "" {
		return nil, fmt.Errorf("config field 'host' is required")
	}
	if cfg.Metric == "" {
		return nil, fmt.Errorf("config field 'metric' is required")
	}
	if cfg.Aggregation == "" {
		cfg.Aggregation = "MEAN"
	}
	if cfg.Granularity <= 0 {
		cfg.Granularity = defaultGranularity
	}
	if cfg.TimeWindowMs <= 0 {
		cfg.TimeWindowMs = defaultTimeWindowMs
	}
	if cfg.APITokenEnv == "" {
		cfg.APITokenEnv = defaultAPITokenEnv
	}
	if cfg.GroupByTag == "" {
		cfg.GroupByTag = defaultGroupByTag
	}
	if cfg.GroupByTagKey == "" {
		cfg.GroupByTagKey = defaultGroupByTagKey
	}
	if cfg.GroupByTagEntity == "" {
		cfg.GroupByTagEntity = defaultGroupByTagEntity
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = defaultPageSize
	}
	return cfg, nil
}

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
