// Package operator implements the kube-resource-fcst-operator command: a controller-runtime manager that runs the
// PredictiveScaler reconciler. It needs a cluster, never the platform database; decisions come from the
// platform over HTTP.
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

	"github.com/cylonchau/kube-resource-fcst/pkg/operator/api/v1alpha1"
	"github.com/cylonchau/kube-resource-fcst/pkg/operator/controller"
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

	// Platform-driven scaling: the workloads of the platform's auto modules are scaled without PredictiveScaler objects.
	PlatformURL            string
	PlatformCredentialFile string
	PlatformCAFile         string
	PlatformInterval       time.Duration
	// Scalers keeps the PredictiveScaler controller on; turn it off when the CRD is not installed.
	Scalers bool
}

// NewScheme returns a scheme with the built-in kinds and the PredictiveScaler API.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// NewCommand builds the operator command.
func NewCommand() *cobra.Command {
	opts := &Options{}
	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Start kube-resource-fcst-operator, the PredictiveScaler controller",
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
	f.StringVar(&opts.PlatformURL, "platform-url", "", "Base URL of the kube-resource-fcst platform. Set it to scale the workloads of the platform's auto modules directly from the module configuration: no PredictiveScaler object is needed.")
	f.StringVar(&opts.PlatformCredentialFile, "platform-credential-file", "", "File holding the developer credential \"<access key>:<access secret>\" (mount a Secret); the KUBE_RESOURCE_FCST_PLATFORM_CREDENTIAL variable is used when empty.")
	f.StringVar(&opts.PlatformCAFile, "platform-ca-file", "", "PEM CA bundle used to verify the platform certificate.")
	f.DurationVar(&opts.PlatformInterval, "platform-sync-interval", 30*time.Second, "How often the platform's modules are read and applied.")
	f.BoolVar(&opts.Scalers, "predictivescaler-crd", true, "Run the PredictiveScaler controller. Turn it off (and do not install the CRD) when the platform modules are the only configuration.")
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
	return &controller.PlatformEndpoint{URL: opts.PlatformURL, Credential: credential, Client: &http.Client{Timeout: 10 * time.Second, Transport: transport}}, nil
}

// Run starts the manager and blocks until ctx ends.
func Run(ctx context.Context, opts *Options) error {
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
	platform := &controller.PlatformDecider{Reader: mgr.GetAPIReader(), Metrics: opMetrics}
	reconciler := &controller.Reconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("kube-resource-fcst-operator"),
		Deciders: map[v1alpha1.DecisionSource]controller.Decider{
			v1alpha1.SourceStatic:      controller.StaticDecider{},
			v1alpha1.SourcePlatformAPI: platform,
			v1alpha1.SourcePrometheus:  &controller.PrometheusDecider{Reader: mgr.GetAPIReader(), Metrics: opMetrics},
		},
		Reporter: platform,
		Metrics:  opMetrics,
	}
	reconciler.NoCRD = !opts.Scalers
	if opts.Scalers {
		if err := reconciler.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("register the PredictiveScaler controller: %w", err)
		}
	}
	if opts.PlatformURL != "" {
		endpoint, err := platformEndpoint(opts)
		if err != nil {
			return err
		}
		platform.Endpoint = endpoint
		if err := mgr.Add(&controller.ModuleSync{Reconciler: reconciler, Platform: platform, Interval: opts.PlatformInterval, Namespace: opts.Namespace}); err != nil {
			return fmt.Errorf("register the platform module sync: %w", err)
		}
	} else if !opts.Scalers {
		return errors.New("nothing to run: give --platform-url, or keep --predictivescaler-crd on")
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
