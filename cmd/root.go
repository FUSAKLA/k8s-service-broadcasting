// Copyright 2019 FUSAKLA Martin Chodúr
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"github.com/fusakla/k8s-service-broadcasting/pkg/controller"
	"github.com/fusakla/k8s-service-broadcasting/pkg/handler"
	"github.com/fusakla/k8s-service-broadcasting/pkg/readiness"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

var (
	iface, metricsIface, kubeconfigPath, namespace, logLevel, serviceName, portName string
	keepalive, allMustSucceed                                                       bool
	timeout                                                                         time.Duration
	kubeconfig                                                                      *rest.Config

	rootCmd = &cobra.Command{
		Use:   "k8s-service-broadcasting",
		Short: "Broadcast HTTP to all service endpoints.",
		Long: "Tool allowing to broadcast/mirror/duplicate HTTP requests to all endpoints of Kubernetes service.\n" +
			"Waits for all of them to end and reports back failed request if any. If not returns last successful.",
		Run: runMultiplexer,
	}
)

func init() {
	cobra.OnInitialize(configure)
	rootCmd.Flags().StringVarP(&iface, "interface", "i", "0.0.0.0:8080", "Interface to listen on.")
	rootCmd.Flags().StringVarP(&metricsIface, "metrics-interface", "m", "0.0.0.0:8081", "Interface for exposing metrics.")
	rootCmd.Flags().StringVarP(&kubeconfigPath, "kubeconfig", "k", os.Getenv("KUBECONFIG"), "Location of the kubeconfig, default if in cluster config or value of KUBECONFIG env variable.")
	rootCmd.Flags().StringVarP(&serviceName, "service", "s", "", "Name of service to sed the requests to.")
	rootCmd.Flags().StringVarP(&portName, "port-name", "p", "", "Name of service port to sed the requests to.")
	rootCmd.Flags().StringVarP(&namespace, "namespace", "n", "", "Namespace to watch for.")
	rootCmd.Flags().BoolVar(&allMustSucceed, "all-must-succeed", true, "By default if any backend fails, the whole request fails. If disabled one succeeded response is enough.")
	rootCmd.Flags().StringVarP(&logLevel, "log-level", "l", "info", "Log level (debug, info, warning, ...) default info.")
	rootCmd.Flags().DurationVarP(&timeout, "timeout", "t", time.Second*10, "Timeout for mirrored requests.")
	rootCmd.Flags().BoolVar(&keepalive, "keepalive", true, "If keepalive should be enabled.")
}

// Execute executes the root command.
func Execute() error {
	return rootCmd.Execute()
}

func configure() {
	var err error
	log.SetFormatter(&log.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})
	log.SetOutput(os.Stdout)
	lvl, err := log.ParseLevel(logLevel)
	if err != nil {
		log.Fatalf("Failed to parse log level, error: %v", err)
	}
	log.SetLevel(lvl)

	if kubeconfigPath == "" {
		kubeconfig, err = rest.InClusterConfig()
		if err != nil {
			log.Fatalf("Failed to load kubeconfig: %v", err)
		}
	} else {
		kubeconfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			log.Fatalf("Failed to load kubeconfig: %v", err)
		}
	}
}

func runMultiplexer(cmd *cobra.Command, _ []string) {
	if err := cmd.MarkFlagRequired("service"); err != nil {
		log.Warn(err)
	}
	if err := cmd.MarkFlagRequired("port-name"); err != nil {
		log.Warn(err)
	}
	runtime.GOMAXPROCS(runtime.NumCPU())

	var status = readiness.New()

	updatesChannel := make(chan *[]string, 10)
	shutdownChannel := make(chan struct{}, 3)
	srvErrChannel := make(chan error)
	signals := make(chan os.Signal, 10)

	endpointController, err := controller.NewEndpointController(kubeconfig, &namespace, serviceName, portName, updatesChannel)
	if err != nil {
		log.Fatalf("Failed to initialize k8s endpoint watcher: %v", err)
	}

	h := handler.NewMultiplexingHandler(iface, timeout, allMustSucceed, keepalive)

	listener, err := net.Listen("tcp", iface)
	if err != nil {
		log.Fatalf("Failed to listen to %v: %v", iface, err)
	}

	server := &http.Server{
		Handler: h,
	}
	server.SetKeepAlivesEnabled(keepalive)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.HandleFunc("/-/healthy", func(w http.ResponseWriter, req *http.Request) { _, _ = fmt.Fprintf(w, "OK") })
		http.HandleFunc("/-/ready", func(w http.ResponseWriter, req *http.Request) {
			if status.IsReady() == nil {
				_, _ = fmt.Fprintf(w, "OK")
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprintf(w, "NOT READY: %v", status.IsReady())
			}
		})
		if err := http.ListenAndServe(metricsIface, nil); err != nil {
			log.Errorf("Error during serving metrics, error: %v", err)
			srvErrChannel <- err
		}
	}()

	go func() {
		err := server.Serve(listener)
		if err != nil {
			if err != http.ErrServerClosed {
				log.Error(err)
				srvErrChannel <- err
			}
		}
	}()

	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	run := true
	for run {
		select {
		case sig, ok := <-signals:
			if !ok {
				continue
			}
			log.Infof("Received signal %v, terminating...", sig)
			shutdownChannel <- struct{}{}
		case <-srvErrChannel:
			shutdownChannel <- struct{}{}
		case <-shutdownChannel:
			status.NotReady(fmt.Errorf("shutting down"))
			ctx, cancelFunc := context.WithTimeout(context.Background(), time.Second*5)
			log.Info("Stopping k8s endpoint controller...")
			endpointController.StopController()
			log.Info("Stopping web server...")
			if err := server.Shutdown(ctx); err != nil {
				log.Errorf("Failed to gracefully stop server, error: %v", err)
			}
			ctx.Done()
			cancelFunc()
			run = false
			break
		case ips, ok := <-updatesChannel:
			if ok {
				status.Ready()
				log.Infof("Updating targets with new addresses: %v", *ips)
				h.SetTargetAddresses(*ips)
				continue
			}
		}
	}
}
