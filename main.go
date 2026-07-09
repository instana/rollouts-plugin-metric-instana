package main

import (
	"github.com/argoproj/argo-rollouts/metricproviders/plugin/rpc"
	goPlugin "github.com/hashicorp/go-plugin"
	log "github.com/sirupsen/logrus"

	"github.com/argoproj-labs/rollouts-plugin-metric-instana/pkg/plugin"
)

// Populated at build time via -ldflags -X.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// go-plugin reserves stdout for the handshake; logrus writes to stderr.
	logCtx := log.WithFields(log.Fields{
		"plugin":  "instana-metrics",
		"version": version,
		"commit":  commit,
	})

	impl := &plugin.RpcPlugin{LogCtx: logCtx}

	pluginMap := map[string]goPlugin.Plugin{
		plugin.PluginKey: &rpc.RpcMetricProviderPlugin{Impl: impl},
	}

	goPlugin.Serve(&goPlugin.ServeConfig{
		HandshakeConfig: plugin.HandshakeConfig,
		Plugins:         pluginMap,
	})
}
