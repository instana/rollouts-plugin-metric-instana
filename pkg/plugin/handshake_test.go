package plugin

import "testing"

// Pins the handshake to what the Argo Rollouts controller expects. Getting this
// wrong makes the controller refuse the binary at exec time, which surfaces as
// AnalysisRun phase=Error (not Failed) and aborts the rollout for the wrong
// reason. See argo-rollouts metricproviders/plugin/client/client.go.
func TestHandshakeMatchesController(t *testing.T) {
	if HandshakeConfig.MagicCookieKey != "ARGO_ROLLOUTS_RPC_PLUGIN" {
		t.Errorf("MagicCookieKey = %q", HandshakeConfig.MagicCookieKey)
	}
	if HandshakeConfig.MagicCookieValue != "metricprovider" {
		t.Errorf("MagicCookieValue = %q, want metricprovider (NOT \"metrics\")", HandshakeConfig.MagicCookieValue)
	}
	if HandshakeConfig.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d", HandshakeConfig.ProtocolVersion)
	}
	if PluginKey != "RpcMetricProviderPlugin" {
		t.Errorf("PluginKey = %q", PluginKey)
	}
}
