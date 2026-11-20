// Command gen writes the resource dashboard of Grafana with the default queries: go run ./pkg/grafana/gen. The files it writes are the ones shipped in
// deploy/grafana and in the Helm chart, and a test holds them to what the generator writes, so they cannot drift from the queries of the platform.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cylonchau/kube-res-foresight/pkg/grafana"
)

func main() {
	raw, err := grafana.JSON(grafana.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, path := range []string{"deploy/grafana/" + grafana.ShippedName, "deploy/helm/kube-res-foresight/files/resources-dashboard.json"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}
