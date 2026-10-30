// Package operator implements the kube-resource-fcst-operator command: a controller-runtime manager that reads the modules of the
// platform over HTTP and writes the replicas of their workloads. It needs a cluster, never the platform database, and creates no
// resource of its own in the cluster: the platform modules are the only configuration.
package operator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/controller"
	"github.com/cylonchau/kube-resource-fcst/pkg/timezone"
	"github.com/cylonchau/kube-resource-fcst/pkg/timing"
	"github.com/cylonchau/kube-resource-fcst/pkg/utils"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// Options are the flags of the command.
type Options struct {
	MetricsAddr string
	ProbeAddr   string
	LeaderElect bool
	LeaderID    string
	Namespace   string

	// PlatformURL is the platform whose auto modules are scaled; it is required.
	PlatformURL            string
	PlatformCredentialFile string
	PlatformCAFile         string
	PlatformInterval       time.Duration
	// CacheTTL is how long after the platform's last answer the remembered decision and module list may stand in for an unreachable
	// platform; 0 turns the cache off. RecoverSuccesses is how many answers in a row a platform that is back must give before the
	// workload may be scaled down again.
	CacheTTL         time.Duration
	ConfigCacheTTL   time.Duration
	RecoverSuccesses int
}

// NewScheme returns a scheme with the built-in kinds.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// NewCommand builds the operator command.
func NewCommand() *cobra.Command {
	opts := &Options{}
	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Start kube-resource-fcst-operator, which scales the workloads of the platform modules",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Run(cmd.Context(), opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.MetricsAddr, "metrics-bind-address", ":8080", "Address of the controller-runtime metrics endpoint; 0 disables it.")
	f.StringVar(&opts.ProbeAddr, "health-probe-bind-address", ":8081", "Address of the /healthz and /readyz probes.")
	f.BoolVar(&opts.LeaderElect, "leader-elect", true, "Elect a leader so only one replica scales; keep it on for any replicated deployment.")
	f.StringVar(&opts.LeaderID, "leader-election-id", "kube-resource-fcst-operator.autoscaling.kube-resource-fcst.io", "Name of the leader election lease.")
	f.StringVar(&opts.Namespace, "namespace", "", "Watch only this namespace; empty watches all namespaces.")
	f.StringVar(&opts.PlatformURL, "platform-url", "", "Base URL of the kube-resource-fcst platform whose auto modules are scaled directly from the module configuration (required).")
	f.StringVar(&opts.PlatformCredentialFile, "platform-credential-file", "", "File holding the developer credential \"<access key>:<access secret>\" (mount a Secret); the KUBE_RESOURCE_FCST_PLATFORM_CREDENTIAL variable is used when empty.")
	f.StringVar(&opts.PlatformCAFile, "platform-ca-file", "", "PEM CA bundle used to verify the platform certificate.")
	f.DurationVar(&opts.PlatformInterval, "platform-sync-interval", 30*time.Second, "Starting value of how often the platform's modules are read and applied; once the platform answers, its setting operator.sync_interval_seconds is used.")
	f.DurationVar(&opts.CacheTTL, "decision-cache-ttl", controller.DefaultCacheTTL, "Starting value (the platform's setting operator.decision_cache_ttl_seconds takes over once it answers). How long after its last answer the platform's decision and module list are remembered and used when the platform cannot be reached (replicas may still rise to the remembered decision, never fall). Past it the safe degradation applies: the fallback replicas, else the current ones. 0 turns the cache off.")
	f.DurationVar(&opts.ConfigCacheTTL, "module-cache-ttl", controller.DefaultConfigTTL, "Starting value (the platform's setting operator.module_cache_ttl_seconds takes over once it answers). How long the platform's last list of modules (bounds, pace, fallback) is used when the platform cannot be reached. Longer than the decision cache, so the safe degradation after the decision has expired can use the module's own bounds and fallback. Past it nothing is evaluated and the replicas stay.")
	f.IntVar(&opts.RecoverSuccesses, "decision-recover-answers", controller.DefaultRecoverSuccesses, "Starting value (the platform's setting operator.recover_answers takes over once it answers). How many answers in a row a platform that is back after an outage must give before the workload may be scaled down again (scaling up follows at once).")
	return cmd
}

// platformEndpoint reads the credential (and the CA bundle) the module sync uses to talk to the platform.
func platformEndpoint(opts *Options) (*controller.PlatformEndpoint, error) {
	credential := strings.TrimSpace(utils.ProjectEnv("KUBE_RESOURCE_FCST_PLATFORM_CREDENTIAL"))
	if opts.PlatformCredentialFile != "" {
		raw, err := os.ReadFile(opts.PlatformCredentialFile)
		if err != nil {
			return nil, fmt.Errorf("read the platform credential: %w", err)
		}
		credential = strings.TrimSpace(string(raw))
	}
	if !strings.Contains(credential, ":") {
		return nil, errors.New("the platform credential must be \"<access key>:<access secret>\": set --platform-credential-file or KUBE_RESOURCE_FCST_PLATFORM_CREDENTIAL")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.PlatformCAFile != "" {
		pem, err := os.ReadFile(opts.PlatformCAFile)
		if err != nil {
			return nil, fmt.Errorf("read the platform CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("the platform CA bundle holds no valid PEM certificate")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return &controller.PlatformEndpoint{URL: opts.PlatformURL, Credential: credential, // the client only carries the highest timeout the platform may set; each request is bounded by the platform's setting (see
		// controller.SettingsStore), which can change while the operator runs
		Client: &http.Client{Timeout: timing.Seconds(timing.OperatorRequestTimeout.Max), Transport: transport}}, nil
}

// Run starts the manager and blocks until ctx ends.
func Run(ctx context.Context, opts *Options) error {
	if strings.TrimSpace(opts.PlatformURL) == "" {
		return errors.New("--platform-url is required: the operator scales the workloads of the platform's modules")
	}
	timezone.Apply() // log lines in the platform's zone
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	scheme, err := NewScheme()
	if err != nil {
		return err
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load the cluster configuration: %w", err)
	}
	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress: opts.ProbeAddr,
		LeaderElection:         opts.LeaderElect,
		LeaderElectionID:       opts.LeaderID,
	}
	mgr, err := ctrl.NewManager(cfg, mgrOpts)
	if err != nil {
		return fmt.Errorf("create the manager: %w", err)
	}
	// the business metrics share the controller-runtime registry, so one endpoint serves both
	opMetrics := controller.NewMetrics(ctrlmetrics.Registry)
	// The time limits are system parameters of the platform: the operator asks for them and asks again from time to time. The flags are
	// the values it starts with and keeps while the platform has not answered.
	initial := timing.DefaultOperatorSettings()
	initial.SyncIntervalSeconds = int(opts.PlatformInterval / time.Second)
	initial.DecisionCacheTTLSeconds = int(opts.CacheTTL / time.Second)
	initial.ModuleCacheTTLSeconds = int(opts.ConfigCacheTTL / time.Second)
	initial.RecoverAnswers = opts.RecoverSuccesses
	settings := controller.NewSettingsStore(initial)
	platform := &controller.PlatformDecider{Metrics: opMetrics, Settings: settings}
	cachePolicy := settings.CachePolicy()
	cachedPlatform := &controller.CachingDecider{Inner: platform, Policy: cachePolicy, Source: settings.CachePolicy, Metrics: opMetrics}
	applier := &controller.Applier{Client: mgr.GetClient(), Decider: cachedPlatform, Reporter: platform, Metrics: opMetrics}
	endpoint, err := platformEndpoint(opts)
	if err != nil {
		return err
	}
	platform.Endpoint = endpoint
	if err := mgr.Add(&controller.SettingsRefresher{Store: settings, Platform: platform}); err != nil {
		return fmt.Errorf("register the platform settings refresh: %w", err)
	}
	if err := mgr.Add(&controller.ModuleSync{Applier: applier, Platform: platform, Interval: opts.PlatformInterval, Settings: settings, Namespace: opts.Namespace,
		Cache: controller.ModuleCache{Policy: cachePolicy, Source: settings.CachePolicy}}); err != nil {
		return fmt.Errorf("register the platform module sync: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
