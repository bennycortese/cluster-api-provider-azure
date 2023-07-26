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
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/profiles/latest/compute/mgmt/compute"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	kubedrain "k8s.io/kubectl/pkg/drain"
	"sigs.k8s.io/cluster-api-provider-azure/azure"
	"sigs.k8s.io/cluster-api-provider-azure/azure/scope"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/resourceskus"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/roleassignments"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/scalesets"
	infrav1exp "sigs.k8s.io/cluster-api-provider-azure/exp/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-azure/util/tele"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	return nil
}

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

func (s *azureMachinePoolService) Snapshot(subscriptionID string, cred *azidentity.DefaultAzureCredential, snapshotName string, resourceGroup string, location string, ctx context.Context, osDisk *string) error {
	snapshotFactory, err := armcompute.NewSnapshotsClient(subscriptionID, cred, nil)

	if err != nil {
		return errors.Wrapf(err, "Failed to create snapshot client")
	}

	_, err = snapshotFactory.BeginCreateOrUpdate(ctx, resourceGroup, snapshotName, armcompute.Snapshot{ // step 3
		Location: to.Ptr(location),
		Properties: &armcompute.SnapshotProperties{
			CreationData: &armcompute.CreationData{
				CreateOption: to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceURI:    osDisk,
			},
		},
	}, nil)

	if err != nil {
		return errors.Wrapf(err, "Failed to create snapshot")
	}

	return nil
}

func (s *azureMachinePoolService) MachinePoolMachineScopeFromAmpm(ampm *infrav1exp.AzureMachinePoolMachine) *scope.MachinePoolMachineScope {
	myscope, err := scope.NewMachinePoolMachineScope(scope.MachinePoolMachineScopeParams{
		Client:                  s.scope.GetClient(),
		MachinePool:             s.scope.MachinePool,
		AzureMachinePool:        s.scope.AzureMachinePool,
		AzureMachinePoolMachine: ampm,
		ClusterScope:            s.scope.ClusterScoper,
	})
	if err != nil {
		return nil
	}
	return myscope
}

func (s *azureMachinePoolService) PrototypeProcess(ctx context.Context) error {
	//var c client.Client // How to avoid this, maybe config := os.Getenv("KUBECONFIG")

	c := s.scope.GetClient()
	amp := s.scope.AzureMachinePool
	NameSpace := amp.Namespace
	machinePoolName := amp.Name
	reconcilo := s.services

	_ = reconcilo
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

			err := c.Get(ctx, client.ObjectKeyFromObject(healthyAmpm), healthyAmpm)
			if err != nil {
				return err
			}
		}

		_ = curInstanceID
		_ = healthyAmpm

		subscriptionID := s.scope.ClusterScoper.SubscriptionID()
		resourceGroup := s.scope.ClusterScoper.ResourceGroup()
		clientID := s.scope.ClusterScoper.ClientID()
		clientSecret := s.scope.ClusterScoper.ClientSecret()
		tenantID := s.scope.ClusterScoper.TenantID()
		galleryLocation := s.scope.ClusterScoper.Location()
		vmssName := machinePoolName

		myscope := s.MachinePoolMachineScopeFromAmpm(healthyAmpm)

		if myscope == nil {
			log.Fatalf("failed to construct machinepoolmachinescope")
		}

		err := myscope.CordonAndDrain(ctx)
		if err != nil {
			log.Fatalf("failed to drain: %v", err)
		}

		cred, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return err
		}

		_ = galleryLocation
		_ = cred

		fmt.Println(clientID) // FIX all os.Getenv references
		credConfig := auth.NewClientCredentialsConfig(clientID, clientSecret, tenantID)
		authorizer, err := credConfig.Authorizer()
		if err != nil {
			return err
		}

		vmssClient := compute.NewVirtualMachineScaleSetsClient(subscriptionID)
		vmssClient.Authorizer = authorizer

		vmssVMsClient := compute.NewVirtualMachineScaleSetVMsClient(subscriptionID)
		vmssVMsClient.Authorizer = authorizer

		vm, err := vmssVMsClient.Get(ctx, resourceGroup, vmssName, curInstanceID, "")
		if err != nil {
			log.Fatalf("Failed to find VM")
		}
		osDisk := vm.StorageProfile.OsDisk.ManagedDisk.ID
		fmt.Println("OS DISK: ", *osDisk)

		if *osDisk == "nil" {
			panic("Disk not found")
		}

		s.Snapshot(subscriptionID, cred, "example1-snapshot", resourceGroup, galleryLocation, ctx, osDisk)

		restConfig, err := remote.RESTConfig(ctx, "azuremachinepoolmachine-scope", c, client.ObjectKey{
			Name:      myscope.ClusterName(),
			Namespace: myscope.AzureMachinePoolMachine.Namespace,
		})

		if err != nil {
			log.Fatalf("Error creating a remote client while deleting Machine, won't retry: %v", err)
		}

		kubeClient, err := kubernetes.NewForConfig(restConfig)

		if err != nil {
			log.Fatalf("Error creating a kube client while restarting Machine, won't retry: %v", err)
		}

		drainer := &kubedrain.Helper{
			Client:              kubeClient,
			Ctx:                 ctx,
			Force:               true,
			IgnoreAllDaemonSets: true,
			DeleteEmptyDirData:  true,
			GracePeriodSeconds:  -1,
			Timeout:             20 * time.Second,
			OnPodDeletedOrEvicted: func(pod *corev1.Pod, usingEviction bool) {
				usingEviction = false
			},
			Out:    writer{klog.Info},
			ErrOut: writer{klog.Error},
		}
		_ = drainer

		node, found, err := myscope.GetNode(ctx)
		if err != nil {
			log.Fatalf("failed to find node: %v", err)
		} else if !found {
			log.Fatalf("failed to find node with the ProviderID")
		}

		if err := kubedrain.RunCordonOrUncordon(drainer, node, false); err != nil { // step 4, this removes SchedulingDisabled
			fmt.Println("Failed to uncordon")
		}

		galleryName := "GalleryInstantiation3"

		gallery := armcompute.Gallery{
			Location: to.Ptr(galleryLocation),
			Properties: &armcompute.GalleryProperties{
				Description: to.Ptr("This is the gallery description."),
			},
		}

		galleryFactory, err := armcompute.NewGalleriesClient(subscriptionID, cred, nil)
		if err != nil {
			log.Fatalf("REEP " + err.Error())
			return err
		}
		_ = galleryName
		_ = gallery
		_ = galleryFactory
		_, err = galleryFactory.BeginCreateOrUpdate(ctx, resourceGroup, galleryName, gallery, nil)

		if err != nil {
			log.Fatalf("REE " + err.Error())
			return err
		}

		galleryImageFactory, err := armcompute.NewGalleryImagesClient(subscriptionID, cred, nil)
		if err != nil {
			log.Fatalf("WA")
			return err
		}

		_, err = galleryImageFactory.BeginCreateOrUpdate(ctx, resourceGroup, galleryName, "myGalleryImage", armcompute.GalleryImage{
			Location: to.Ptr(galleryLocation),
			Properties: &armcompute.GalleryImageProperties{
				HyperVGeneration: to.Ptr(armcompute.HyperVGenerationV1),
				Identifier: &armcompute.GalleryImageIdentifier{
					Offer:     to.Ptr("myOfferName"),
					Publisher: to.Ptr("myPublisherName"),
					SKU:       to.Ptr("mySkuName"),
				},
				OSState: to.Ptr(armcompute.OperatingSystemStateTypesGeneralized),
				OSType:  to.Ptr(armcompute.OperatingSystemTypesLinux),
			},
		}, nil)

		if err != nil {
			log.Fatalf("ROAR " + err.Error())
			//errors.Wrapf(error, "failed to make new image gallery")
			return err
		}

		galleryImageVersionFactory, err := armcompute.NewGalleryImageVersionsClient(subscriptionID, cred, nil)
		if err != nil {
			return err
		}

		poller, err := galleryImageVersionFactory.BeginCreateOrUpdate(ctx, resourceGroup, galleryName, "myGalleryImage", "1.0.0", armcompute.GalleryImageVersion{
			Location: to.Ptr(galleryLocation),
			Properties: &armcompute.GalleryImageVersionProperties{
				SafetyProfile: &armcompute.GalleryImageVersionSafetyProfile{
					AllowDeletionOfReplicatedLocations: to.Ptr(false),
				},
				StorageProfile: &armcompute.GalleryImageVersionStorageProfile{
					OSDiskImage: &armcompute.GalleryOSDiskImage{
						Source: &armcompute.GalleryDiskImageSource{
							ID: to.Ptr("subscriptions/" + subscriptionID + "/resourceGroups/" + resourceGroup + "/providers/Microsoft.Compute/snapshots/example1-snapshot"),
						},
					},
				},
			},
		}, nil) // step 5

		if err != nil {
			return err
		}

		_ = poller
	}

	return nil
}
