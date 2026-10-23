package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const containerName = "kubeforecast-tg-bot-api"

// ObservedState is what's actually running locally, as seen by `docker
// inspect`.
type ObservedState struct {
	Running     bool
	ContainerID string
	Image       string
}

// DesiredState is what the container should look like, pulled from the
// server's config snapshot.
type DesiredState struct {
	Running   bool
	Port      int
	LocalMode bool
	Image     string
	APIID     string
	APIHash   string
}

// Inspect reports the current state of the managed container. A missing
// container is not an error — it just means Running=false.
func Inspect(ctx context.Context) (ObservedState, error) {
	out, err := runDocker(ctx, "inspect", "--format", "{{.State.Running}}|{{.Id}}|{{.Config.Image}}", containerName)
	if err != nil {
		if strings.Contains(err.Error(), "No such") || strings.Contains(out, "No such") {
			return ObservedState{}, nil
		}
		return ObservedState{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 3)
	if len(parts) != 3 {
		return ObservedState{}, fmt.Errorf("unexpected docker inspect output: %q", out)
	}
	running, _ := strconv.ParseBool(parts[0])
	return ObservedState{Running: running, ContainerID: parts[1], Image: parts[2]}, nil
}

// Reconcile makes the local container match desired. It only touches Docker
// when something is actually out of sync: a stopped-but-should-run or
// running-but-should-stop container, or a running container whose image/
// port/mode no longer matches desired (which forces a recreate).
func Reconcile(ctx context.Context, desired DesiredState, observed ObservedState) (ObservedState, error) {
	if !desired.Running {
		if observed.Running {
			if _, err := runDocker(ctx, "rm", "-f", containerName); err != nil {
				return observed, err
			}
		}
		return ObservedState{}, nil
	}

	needsRecreate := !observed.Running || observed.Image != desired.Image
	if needsRecreate {
		_, _ = runDocker(ctx, "rm", "-f", containerName) // best-effort; fine if it didn't exist
		args := []string{
			"run", "-d", "--name", containerName, "--restart", "unless-stopped",
			"-p", fmt.Sprintf("%d:8081", desired.Port),
			desired.Image,
			"--api-id=" + desired.APIID,
			"--api-hash=" + desired.APIHash,
			"--http-port=8081",
		}
		if desired.LocalMode {
			args = append(args, "--local")
		}
		out, err := runDocker(ctx, args...)
		if err != nil {
			return ObservedState{}, fmt.Errorf("docker run failed: %w (%s)", err, out)
		}
	}
	return Inspect(ctx)
}

func runDocker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(stdout.String() + " " + stderr.String())
		return combined, fmt.Errorf("%s: %w", combined, err)
	}
	return stdout.String(), nil
}
