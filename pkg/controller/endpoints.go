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

package controller

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

var (
	numberOfEndpoints = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "service_endpoint_count",
			Help: "Number of found endpoints for given service.",
		},
		[]string{"service"},
	)
)

func init() {
	prometheus.MustRegister(numberOfEndpoints)
}

func NewEndpointController(config *rest.Config, namespace *string, serviceName string, servicePortname string, updatesChannel chan *[]string) (*EndpointsController, error) {
	var informerFactory informers.SharedInformerFactory
	stopChannel := make(chan struct{})
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	var informerOptions []informers.SharedInformerOption
	if namespace != nil {
		informerOptions = append(informerOptions, informers.WithNamespace(*namespace))
	}
	informerFactory = informers.NewSharedInformerFactoryWithOptions(clientset, 0, informerOptions...)
	controller := EndpointsController{
		clientset: clientset,

		informer:        informerFactory.Core().V1().Endpoints().Informer(),
		lister:          informerFactory.Core().V1().Endpoints().Lister(),
		serviceName:     serviceName,
		servicePortName: servicePortname,
		updatesChannel:  updatesChannel,
		stopChannel:     stopChannel,
	}
	controller.informer.AddEventHandler(&controller)
	informerFactory.Start(stopChannel)

	return &controller, nil
}

type EndpointsController struct {
	clientset       *kubernetes.Clientset
	informer        cache.SharedIndexInformer
	lister          listers.EndpointsLister
	serviceName     string
	servicePortName string
	updatesChannel  chan *[]string
	stopChannel     chan struct{}
}

func (e *EndpointsController) ListMatchingIPs() (*[]string, error) {
	var ips []string
	endpoints, err := e.lister.List(labels.Set{}.AsSelector())
	if err != nil {
		return nil, err
	}
	for _, endpoint := range endpoints {
		if endpoint.Name != e.serviceName {
			continue
		}
		for _, subset := range endpoint.Subsets {
			var targetPort int32
			portFound := false
			for _, port := range subset.Ports {
				if port.Name == e.servicePortName {
					targetPort = port.Port
					portFound = true
				}
			}
			if !portFound {
				log.Errorf("Did not find specified port name %v in the service %v", e.servicePortName, endpoint.Name)
			}
			for _, addr := range subset.Addresses {
				ips = append(ips, fmt.Sprintf("%s:%d", addr.IP, targetPort))
			}
		}
	}
	numberOfEndpoints.WithLabelValues(e.serviceName).Set(float64(len(ips)))
	return &ips, nil
}

func (e *EndpointsController) OnAdd(obj interface{}) {
	metaObject, err := meta.Accessor(obj)
	if err != nil {
		log.Errorf("Failed to convert cache item to metaObject: %v", err)
		return
	}
	objectName := meta.AsPartialObjectMetadata(metaObject).Name
	log.Debugf("Processing endpoints update for %v", objectName)
	if e.serviceName != objectName {
		log.Debugf("Skipping non matching service: %v", objectName)
		return
	}
	ips, err := e.ListMatchingIPs()
	if err != nil {
		log.Errorf("Failed to list endpoints, error: %v", err)
		return
	}
	e.updatesChannel <- ips
}
func (e *EndpointsController) OnUpdate(_, newObj interface{}) {
	e.OnAdd(newObj)
}

func (e *EndpointsController) OnDelete(obj interface{}) {
	e.OnAdd(obj)
}

func (e *EndpointsController) StopController() {
	close(e.stopChannel)
	//close(e.updatesChannel)
}
