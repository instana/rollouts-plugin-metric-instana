package plugin

import "testing"

// A pod-template-hash is short and not globally unique, so the query must be
// scoped to a cluster and namespace when they are supplied.
func TestEffectiveTagFilters_PrependsClusterAndNamespace(t *testing.T) {
	cfg := &Config{
		ClusterName: "k-llama",
		Namespace:   "shop-demo",
		TagFilters: []TagFilter{{
			Name: PodLabelTag, Key: "rollouts-pod-template-hash", Operator: "EQUALS", Value: "abc123",
		}},
	}

	got := effectiveTagFilters(cfg)
	if len(got) != 3 {
		t.Fatalf("expected 3 filters, got %d: %+v", len(got), got)
	}
	if got[0].Name != ClusterNameTag || got[0].Value != "k-llama" {
		t.Errorf("filter[0] = %+v, want cluster scope", got[0])
	}
	if got[1].Name != NamespaceTag || got[1].Value != "shop-demo" {
		t.Errorf("filter[1] = %+v, want namespace scope", got[1])
	}
	if got[2].Key != "rollouts-pod-template-hash" {
		t.Errorf("filter[2] = %+v, want the canary hash filter", got[2])
	}
}

func TestEffectiveTagFilters_OmitsUnsetScopes(t *testing.T) {
	cfg := &Config{TagFilters: []TagFilter{{Name: PodLabelTag, Key: "k", Value: "v"}}}
	if got := effectiveTagFilters(cfg); len(got) != 1 {
		t.Fatalf("expected only the supplied filter, got %+v", got)
	}
}
