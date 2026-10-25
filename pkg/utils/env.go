package utils

import (
	"os"
	"strings"
)

// ProjectEnvNames lists the current variable first, followed by its pre-rename alias.
// Existing encryption and signing keys must keep the same bytes during upgrades.
func ProjectEnvNames(name string) []string {
	names := []string{name}
	if strings.HasPrefix(name, "KUBE_RESOURCE_FCST_") {
		names = append(names, "KUBEFORECAST_"+strings.TrimPrefix(name, "KUBE_RESOURCE_FCST_"))
	}
	return names
}

func ProjectEnv(name string) string {
	for _, key := range ProjectEnvNames(name) {
		if value, exists := os.LookupEnv(key); exists {
			return value
		}
	}
	return ""
}
