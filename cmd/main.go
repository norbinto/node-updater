/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v2"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	updatev1 "norbinto/node-updater/api/v1"
	"norbinto/node-updater/internal/appconfig"
	"norbinto/node-updater/internal/azure"
	"norbinto/node-updater/internal/azuredevops"
	configmap "norbinto/node-updater/internal/configmap" // Import the configmap package
	"norbinto/node-updater/internal/controller"
	"norbinto/node-updater/internal/job"
	nodepool "norbinto/node-updater/internal/nodepool"
	pod "norbinto/node-updater/internal/pod" // Import the pod package

	"github.com/go-logr/zapr"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(updatev1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	var errorReconcileTime int
	var successReconcileTime int
	var upgradeFrequency int
	var runInVsCode bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.IntVar(&errorReconcileTime, "error-reconcile-time", 10, "Default value is 10 seconds. The time to wait before retrying a failed reconcile.")
	flag.IntVar(&successReconcileTime, "success-reconcile-time", 10, "Default value is 10 seconds. The time to wait before retrying a successful reconcile.")
	flag.IntVar(&upgradeFrequency, "upgrade-frequency", 3600, "Default value is 3600 seconds(1 hour). The time to wait before checking for a new version.")
	flag.BoolVar(&runInVsCode, "run-in-vs-code", false, "If set, the controller will run in VS Code.")

	// todo: like in keda we should use strings instead of numbers for log levels
	var logLevel int
	flag.IntVar(&logLevel, "log-level", 1, "The log level for the controller. 0=debug, 1=info, 2=warn, 3=error")
	var zapLevel zapcore.Level
	switch logLevel {
	case 0:
		zapLevel = zapcore.DebugLevel
	case 1:
		zapLevel = zapcore.InfoLevel
	case 2:
		zapLevel = zapcore.WarnLevel
	case 3:
		zapLevel = zapcore.ErrorLevel
	default:
		zapLevel = zapcore.InfoLevel
	}

	opts := zap.Options{
		Development: true,
		Level:       zapLevel,
	}

	// Create a context for the application
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	config := appconfig.NewConfig(time.Duration(errorReconcileTime)*time.Second, time.Duration(successReconcileTime)*time.Second, time.Duration(upgradeFrequency)*time.Second)

	logger := zap.NewRaw(zap.UseFlagOptions(&opts))

	//create a logger
	ctrl.SetLogger(zapr.NewLogger(logger))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Create watchers for metrics and webhooks certificates
	var metricsCertWatcher, webhookCertWatcher *certwatcher.CertWatcher

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		var err error
		webhookCertWatcher, err = certwatcher.New(
			filepath.Join(webhookCertPath, webhookCertName),
			filepath.Join(webhookCertPath, webhookCertKey),
		)
		if err != nil {
			setupLog.Error(err, "Failed to initialize webhook certificate watcher")
			os.Exit(1)
		}

		webhookTLSOpts = append(webhookTLSOpts, func(config *tls.Config) {
			config.GetCertificate = webhookCertWatcher.GetCertificate
		})
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: webhookTLSOpts,
	})

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.4/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, metricsCertName),
			filepath.Join(metricsCertPath, metricsCertKey),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "a3a1ffc7.norbinto",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	var kubeConfig *rest.Config
	var azureCred azcore.TokenCredential
	var subscriptionID, clusterResourceGroup, clusterName string
	if runInVsCode {
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = clientcmd.RecommendedHomeFile
		}
		kubeConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			setupLog.Error(err, "unable to build kubeconfig from flags")
			os.Exit(1)
		}
		azureCred, err = azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			setupLog.Error(err, "unable to create Azure credentials")
			os.Exit(1)
		}
		subscriptionID = os.Getenv("AZURE_SUBSCRIPTION_ID")
		clusterResourceGroup = os.Getenv("AZURE_CLUSTER_RESOURCE_GROUP")
		clusterName = os.Getenv("AZURE_CLUSTER_NAME")
		setupLog.Info("Running in VS Code mode", "subscriptionID", subscriptionID, "clusterResourceGroup", clusterResourceGroup, "clusterName", clusterName)
	} else {
		//todo pass doers interface instead of https client
		azureController := azure.NewAzureController(&http.Client{}, logger.Named("azure"))
		subscriptionID, clusterResourceGroup, clusterName, err = azureController.GetClusterInfo()
		if err != nil {
			setupLog.Error(err, "unable to get subsription id")
			os.Exit(1)
		}
		kubeConfig, err = rest.InClusterConfig()
		if err != nil {
			setupLog.Error(err, "unable to build in-cluster kubeconfig")
			os.Exit(1)
		}
		credOptions := azidentity.WorkloadIdentityCredentialOptions{
			TokenFilePath: os.Getenv("AZURE_FEDERATED_TOKEN_FILE"),
			ClientID:      os.Getenv("AZURE_CLIENT_ID"),
			TenantID:      os.Getenv("AZURE_TENANT_ID"),
		}

		azureCred, err = azidentity.NewWorkloadIdentityCredential(&credOptions)
		if err != nil {
			setupLog.Error(err, "unable to create workload identity credentials")
			os.Exit(1)
		}
		setupLog.Info("Using Managed Identity (workload identity) federated credentials for authentication")
	}

	// Initialize KubeClient
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		setupLog.Error(err, "unable to create kube client")
		os.Exit(1)
	}

	agentPoolClient, err := armcontainerservice.NewAgentPoolsClient(subscriptionID, azureCred, nil)
	if err != nil {
		setupLog.Error(err, "unable to create container service client")
		os.Exit(1)
	}
	if err = (&controller.SafeEvictReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		KubeClient: kubeClient,
		PodController: pod.NewPodController(
			kubeClient,
			azuredevops.NewAzureDevopsController(&http.Client{}, os.Getenv("AZURE_DEVOPS_ORG"), os.Getenv("AZURE_DEVOPS_PAT"), logger.Named("azureDevOps")),
			job.NewJobController(
				kubeClient,
				logger.Named("job")),
			logger.Named("pod")),
		NodepoolController: nodepool.NewNodePoolController(
			kubeClient,
			agentPoolClient,
			subscriptionID,
			clusterResourceGroup,
			clusterName,
			logger.Named("nodepool")),
		ConfigmapController: configmap.NewConfigMapController(
			kubeClient,
			logger.Named("configmap")),
		Config: config,
		Logger: logger.Named("safeEvict"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SafeEvict")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if metricsCertWatcher != nil {
		setupLog.Info("Adding metrics certificate watcher to manager")
		if err := mgr.Add(metricsCertWatcher); err != nil {
			setupLog.Error(err, "unable to add metrics certificate watcher to manager")
			os.Exit(1)
		}
	}

	if webhookCertWatcher != nil {
		setupLog.Info("Adding webhook certificate watcher to manager")
		if err := mgr.Add(webhookCertWatcher); err != nil {
			setupLog.Error(err, "unable to add webhook certificate watcher to manager")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
