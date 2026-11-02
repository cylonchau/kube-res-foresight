package controller

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The operator talks to two things only: the Kubernetes API and the platform's HTTP API. Every read of configuration or of a decision, and
// every record of what it did, goes through the platform; it has no database of its own to read or write, and no code path to one. This
// test holds the line on the whole dependency tree of the command: the project's own packages it may use are listed, and no database
// layer (the ORM, a driver, the platform's DAOs and models) may appear anywhere below it.
func TestTheOperatorHasNoPathToTheDatabase(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not available")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}} {{join .Imports \" \"}}",
		"github.com/cylonchau/kube-res-foresight/pkg/cmd/operator").Output()
	require.NoError(t, err)

	const own = "github.com/cylonchau/kube-res-foresight/pkg/"
	allowed := map[string]bool{"cmd/operator": true, "operator/controller": true, "timezone": true, "timing": true, "utils": true}
	forbidden := []string{"gorm.io/", "modernc.org/sqlite", "github.com/glebarez/", "github.com/go-sql-driver/", "github.com/jackc/", "github.com/lib/pq", "github.com/mattn/go-sqlite3"}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		pkg := fields[0]
		if strings.HasPrefix(pkg, own) {
			assert.True(t, allowed[strings.TrimPrefix(pkg, own)], "the operator pulls in %s: it may use only the packages it was built on, and none that reach a database", pkg)
			for _, imp := range fields[1:] {
				assert.NotEqual(t, "database/sql", imp, "%s opens a database", pkg)
			}
		}
		for _, bad := range forbidden {
			assert.False(t, strings.HasPrefix(pkg, bad), "the operator depends on %s, a database layer", pkg)
		}
	}
}
