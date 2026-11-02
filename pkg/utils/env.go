package utils

import (
	"os"
	"strings"
)

// ProjectEnvNames lists the current variable first, followed by its pre-rename aliases.
// Existing encryption and signing keys must keep the same bytes during upgrades.
func ProjectEnvNames(name string) []string {
	names := []string{name}
	if strings.HasPrefix(name, "KUBE_RES_FORESIGHT_") {
		suffix := strings.TrimPrefix(name, "KUBE_RES_FORESIGHT_")
		names = append(names, "KUBE_RESOURCE_FCST_"+suffix, "KUBEFORECAST_"+suffix)
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
