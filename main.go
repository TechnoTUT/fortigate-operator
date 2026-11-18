// Command fortigate-operator reconciles FirewallPolicy, FirewallAddress and FirewallService
// resources against a FortiGate through the FortiOS REST API.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"go.uber.org/zap/zapcore"

	"github.com/TechnoTUT/fortigate-operator/api/v1alpha1"
	"github.com/TechnoTUT/fortigate-operator/internal/controller"
	"github.com/TechnoTUT/fortigate-operator/internal/fortigate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctrl.SetLogger(zap.New(zap.UseDevMode(false), zap.Level(zapcore.Level(logLevel(env("LOG_LEVEL", "info"))))))
	logger := ctrl.Log.WithName("setup")

	token := os.Getenv("FORTIGATE_TOKEN")
	if f := os.Getenv("FORTIGATE_TOKEN_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("cannot read FORTIGATE_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(b))
	}
	insecure, err := envBool("FORTIGATE_INSECURE_SKIP_VERIFY", false)
	if err != nil {
		return err
	}
	inQuery, err := envBool("FORTIGATE_TOKEN_IN_QUERY", false)
	if err != nil {
		return err
	}
	dryRun, err := envBool("DRY_RUN", false)
	if err != nil {
		return err
	}
	leaderElect, err := envBool("LEADER_ELECT", false)
	if err != nil {
		return err
	}
	resync, err := time.ParseDuration(env("RESYNC_INTERVAL", "5m"))
	if err != nil || resync < 10*time.Second {
		return fmt.Errorf("RESYNC_INTERVAL must be a duration of at least 10s")
	}

	fg, err := fortigate.New(fortigate.Config{
		BaseURL:            os.Getenv("FORTIGATE_URL"),
		Token:              token,
		VDOM:               env("FORTIGATE_VDOM", "root"),
		TokenInQuery:       inQuery,
		CAFile:             os.Getenv("FORTIGATE_CA_FILE"),
		InsecureSkipVerify: insecure,
	})
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: env("METRICS_ADDR", ":8080")},
		HealthProbeBindAddress:  env("HEALTH_ADDR", ":8081"),
		LeaderElection:          leaderElect,
		LeaderElectionID:        "fortigate-operator.fortigate.technotut.net",
		LeaderElectionNamespace: os.Getenv("POD_NAMESPACE"),
	})
	if err != nil {
		return err
	}

	opts := controller.Options{FortiGate: fg, DryRun: dryRun, Resync: resync}
	if err := controller.NewPolicyReconciler(mgr.GetClient(), opts).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := controller.NewAddressReconciler(mgr.GetClient(), opts).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := controller.NewServiceReconciler(mgr.GetClient(), opts).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	logger.Info("starting", "fortigate", os.Getenv("FORTIGATE_URL"), "dryRun", dryRun, "resync", resync.String())
	return mgr.Start(ctrl.SetupSignalHandler())
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envBool(k string, d bool) (bool, error) {
	v := os.Getenv(k)
	if v == "" {
		return d, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not a boolean", k, v)
	}
	return b, nil
}

// logLevel maps a level name to a zap level (debug is -1, info 0, warn 1, error 2).
func logLevel(s string) int8 {
	switch strings.ToLower(s) {
	case "debug":
		return -1
	case "warn":
		return 1
	case "error":
		return 2
	default:
		return 0
	}
}
