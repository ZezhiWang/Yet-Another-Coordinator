#!/bin/sh

# create namespace
kubectl apply -f yac_namespace.json

# establish namespace context
kubectl config set-context --current --namespace=yac

# k8s client permission
kubectl apply -f client_perm.yaml

# create deployment
kubectl apply -f coordinator_deployment.yaml

# create service
kubectl apply -f coordinator_service.yaml

# restore namespace
kubectl config set-context --current --namespace=default