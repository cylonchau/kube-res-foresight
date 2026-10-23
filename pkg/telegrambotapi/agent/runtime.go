package agent

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	apputils "github.com/cylonchau/kubeforecast/pkg/app/utils"
	"log"
	"os"
	"runtime"
	"time"
)

// Config configures one agent run.
type Config struct {
	ServerURL string
	NodeName  string
	StateFile string
	Version   string
}

// Run registers with the server (if needed), then loops heartbeating and
// reconciling the local Docker container to match the server's desired
// state, until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	hostname, _ := os.Hostname()
	nodeName := cfg.NodeName
	if nodeName == "" {
		nodeName = hostname
		if nodeName == "" {
			nodeName = "bot-api-node-" + time.Now().Format("150405")
		}
	}
	sysInfo := apis.TelegramBotAPINodeSystemInfo{
		Hostname: hostname, OperatingSystem: runtime.GOOS, Architecture: runtime.GOARCH, Version: cfg.Version,
	}

	nodeID, token, err := registerUntilApproved(ctx, cfg.ServerURL, cfg.StateFile, nodeName, sysInfo)
	if err != nil {
		return err
	}
	client := NewClient(cfg.ServerURL, token)

	heartbeatTicker := time.NewTicker(30 * time.Second)
	syncTicker := time.NewTicker(10 * time.Second)
	defer heartbeatTicker.Stop()
	defer syncTicker.Stop()

	doHeartbeat := func() {
		observed, inspectErr := Inspect(ctx)
		lastErr := ""
		if inspectErr != nil {
			lastErr = inspectErr.Error()
		}
		if err := client.Heartbeat(ctx, nodeID, apis.TelegramBotAPINodeHeartbeatReq{
			ObservedRunning: observed.Running, ObservedContainer: observed.ContainerID,
			ObservedImage: observed.Image, LastError: lastErr,
			TelegramBotAPINodeSystemInfo: sysInfo,
		}); err != nil {
			log.Printf("bot-api-agent: heartbeat failed: %v", err)
		}
	}
	doSync := func() {
		snapshot, err := client.FetchConfigSnapshot(ctx, nodeID)
		if err != nil {
			log.Printf("bot-api-agent: fetch config snapshot failed: %v", err)
			return
		}
		observed, err := Inspect(ctx)
		if err != nil {
			log.Printf("bot-api-agent: docker inspect failed: %v", err)
			return
		}
		desired := DesiredState{
			Running: snapshot.DesiredRunning, Port: snapshot.Port, LocalMode: snapshot.LocalMode,
			Image: snapshot.Image, APIID: snapshot.APIID, APIHash: snapshot.APIHash,
		}
		if desired.Running == observed.Running && (!desired.Running || observed.Image == desired.Image) {
			return // already matches; Reconcile would be a no-op
		}
		if _, err := Reconcile(ctx, desired, observed); err != nil {
			log.Printf("bot-api-agent: reconcile failed: %v", err)
		} else {
			log.Printf("bot-api-agent: container reconciled to desired state (running=%v image=%s)", desired.Running, desired.Image)
		}
	}

	doHeartbeat()
	doSync()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeatTicker.C:
			doHeartbeat()
		case <-syncTicker.C:
			doSync()
		}
	}
}

// registerUntilApproved retries Register every 5s until the node is
// approved/online, persisting {node_id, token} to stateFile once granted so
// a restart resumes as the same node instead of enrolling a duplicate.
func registerUntilApproved(ctx context.Context, serverURL, stateFile, nodeName string, sysInfo apis.TelegramBotAPINodeSystemInfo) (int64, string, error) {
	var nodeID int64
	token := apputils.RandomHex(32)
	if saved, err := loadSession(stateFile); err == nil && saved != nil {
		nodeID, token = saved.NodeID, saved.Token
	}
	client := NewClient(serverURL, token)

	for {
		sess, err := client.Register(ctx, apis.TelegramBotAPINodeRegisterReq{
			NodeID: nodeID, Name: nodeName, Token: token, TelegramBotAPINodeSystemInfo: sysInfo,
		})
		if err != nil {
			log.Printf("bot-api-agent: register failed, retrying: %v", err)
		} else {
			nodeID = sess.NodeID
			if sess.Token != "" {
				token = sess.Token
				client.SetToken(token)
			}
			switch sess.Status {
			case "online", "approved":
				log.Printf("bot-api-agent: node %s approved (id=%d)", nodeName, nodeID)
				if err := saveSession(stateFile, &session{NodeID: nodeID, Token: token}); err != nil {
					log.Printf("bot-api-agent: failed to persist session: %v", err)
				}
				return nodeID, token, nil
			case "rejected":
				log.Printf("bot-api-agent: node %s registration was rejected by an administrator", nodeName)
			default:
				log.Printf("bot-api-agent: node %s pending admin approval...", nodeName)
			}
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
