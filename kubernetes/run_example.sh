#!/bin/bash

CLUSTER_NAME=k8s-service-broadcasting

minikube start -p $CLUSTER_NAME
kubectl apply -f ./
# Push metric over the broadcast to both instances
echo "some_metric 3.14" | curl --data-binary @- $(minikube service -p $CLUSTER_NAME prometheus-pushgateway-ha --url)/metrics/job/test

minikube service -p $CLUSTER_NAME prometheus-pushgateway-ha --url
minikube service -p $CLUSTER_NAME prometheus-pushgateway --url

# minikube delete -p k8s-service-broadcasting
