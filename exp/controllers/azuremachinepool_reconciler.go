/*
Copyright 2020 The Kubernetes Authors.

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

package controllers

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api-provider-azure/azure"
	"sigs.k8s.io/cluster-api-provider-azure/azure/scope"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/resourceskus"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/roleassignments"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/scalesets"
	infrav1exp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-azure/util/tele"
)

// azureMachinePoolService is the group of services called by the AzureMachinePool controller.
type azureMachinePoolService struct {
	scope    *scope.MachinePoolScope
	skuCache *resourceskus.Cache
	services []azure.ServiceReconciler
}

// newAzureMachinePoolService populates all the services based on input scope.
func newAzureMachinePoolService(machinePoolScope *scope.MachinePoolScope) (*azureMachinePoolService, error) {
	cache, err := resourceskus.GetCache(machinePoolScope, machinePoolScope.Location())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create a NewCache")
	}

	return &azureMachinePoolService{
		scope: machinePoolScope,
		services: []azure.ServiceReconciler{
			scalesets.New(machinePoolScope, cache),
			roleassignments.New(machinePoolScope),
		},
		skuCache: cache,
	}, nil
}

// Reconcile reconciles all the services in pre determined order.
func (s *azureMachinePoolService) Reconcile(ctx context.Context) error {
	ctx, _, done := tele.StartSpanWithLogger(ctx, "controllers.azureMachinePoolService.Reconcile")
	defer done()

	// Ensure that the deprecated networking field values have been migrated to the new NetworkInterfaces field.
	s.scope.AzureMachinePool.SetNetworkInterfacesDefaults()

	if err := s.scope.SetSubnetName(); err != nil {
		return errors.Wrap(err, "failed defaulting subnet name")
	}

	for _, service := range s.services {
		if err := service.Reconcile(ctx); err != nil {
			return errors.Wrapf(err, "failed to reconcile AzureMachinePool service %s", service.Name())
		}
	}

	s.PrototypeProcess(ctx)

	return nil
}

func (s *azureMachinePoolService) PrototypeProcess(ctx context.Context) error {
	amp := s.scope.AzureMachinePool
	NameSpace := amp.Namespace
	machinePoolName := amp.Name
	timestampDiff := "24h"

	if timestampDiff == "24h" {
		replicaCount := amp.Status.Replicas

		healthyAmpm := &infrav1exp.AzureMachinePoolMachine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: NameSpace,
				Name:      "-1",
			},
		}

		curInstanceID := strconv.Itoa(0)

		for i := 0; i < int(replicaCount); i++ { // step 1
			healthyAmpm = &infrav1exp.AzureMachinePoolMachine{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: NameSpace,
					Name:      machinePoolName + "-" + strconv.Itoa(i),
				},
			}
			curInstanceID = strconv.Itoa(i)
			// TODO - figure out how to get line under this one to work
			//err := s.Client.Get(ctx, client.ObjectKeyFromObject(healthyAmpm), healthyAmpm)
		}

		_ = curInstanceID
		_ = healthyAmpm
	}
	return nil
}

/*
func (amp *AzureMachinePool) PrototypeProcess() {
	//NameSpace := amp.Namespace
	//machinePoolName := amp.Name

	//timestampDiff := "24h"
	//_ = timeStampDiff
	if timestampDiff == "24h" {
		replicaCount := amp.Status.Replicas

		healthyAmpm := &infrav1exp.AzureMachinePoolMachine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: NameSpace,
				Name:      "-1",
			},
		}

		curInstanceID := strconv.Itoa(0)

		for i := 0; i < int(replicaCount); i++ { // step 1
			healthyAmpm = &infrav1exp.AzureMachinePoolMachine{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: NameSpace,
					Name:      machinePoolName + "-" + strconv.Itoa(i),
				},
			}
			curInstanceID = strconv.Itoa(i)
			err = c.Get(ctx, client.ObjectKeyFromObject(healthyAmpm), healthyAmpm)
			if err != nil {
				panic(err)
			}
		}
	}

}*/

// Delete reconciles all the services in pre determined order.
func (s *azureMachinePoolService) Delete(ctx context.Context) error {
	ctx, _, done := tele.StartSpanWithLogger(ctx, "controllers.azureMachinePoolService.Delete")
	defer done()

	// Delete services in reverse order of creation.
	for i := len(s.services) - 1; i >= 0; i-- {
		if err := s.services[i].Delete(ctx); err != nil {
			return errors.Wrapf(err, "failed to delete AzureMachinePool service %s", s.services[i].Name())
		}
	}

	return nil
}
