//go:build e2e
// +build e2e

/*
Copyright 2022 The Kubernetes Authors.

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

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	helmVals "helm.sh/helm/v3/pkg/cli/values"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
)

const (
	cloudProviderAzureHelmRepoURL     = "https://raw.githubusercontent.com/kubernetes-sigs/cloud-provider-azure/master/helm/repo"
	cloudProviderAzureChartName       = "cloud-provider-azure"
	cloudProviderAzureHelmReleaseName = "cloud-provider-azure-oot"
	azureDiskCSIDriverHelmRepoURL     = "https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts"
	azureDiskCSIDriverChartName       = "azuredisk-csi-driver"
	azureDiskCSIDriverHelmReleaseName = "azuredisk-csi-driver-oot"
)

// InstallCalicoAndCloudProviderAzureHelmChart installs the official cloud-provider-azure helm chart
// and validates that expected pods exist and are Ready.
func InstallCalicoAndCloudProviderAzureHelmChart(ctx context.Context, input clusterctl.ApplyClusterTemplateAndWaitInput, cidrBlocks []string, hasWindows bool) {
	specName := "cloud-provider-azure-install"
	By("Installing cloud-provider-azure components via helm")
	options := &helmVals.Options{
		Values: []string{
			fmt.Sprintf("infra.clusterName=%s", input.ConfigCluster.ClusterName),
			"cloudControllerManager.logVerbosity=4",
		},
		StringValues: []string{fmt.Sprintf("cloudControllerManager.clusterCIDR=%s", strings.Join(cidrBlocks, `\,`))},
	}
	// If testing a CI version of Kubernetes, use CCM and CNM images built from source.
	if useCIArtifacts || usePRArtifacts {
		options.Values = append(options.Values, fmt.Sprintf("cloudControllerManager.imageName=%s", os.Getenv("CCM_IMAGE_NAME")))
		options.Values = append(options.Values, fmt.Sprintf("cloudNodeManager.imageName=%s", os.Getenv("CNM_IMAGE_NAME")))
		options.Values = append(options.Values, fmt.Sprintf("cloudControllerManager.imageRepository=%s", os.Getenv("IMAGE_REGISTRY")))
		options.Values = append(options.Values, fmt.Sprintf("cloudNodeManager.imageRepository=%s", os.Getenv("IMAGE_REGISTRY")))
		options.StringValues = append(options.StringValues, fmt.Sprintf("cloudControllerManager.imageTag=%s", os.Getenv("IMAGE_TAG_CCM")))
		options.StringValues = append(options.StringValues, fmt.Sprintf("cloudNodeManager.imageTag=%s", os.Getenv("IMAGE_TAG_CNM")))
	}

	if input.ConfigCluster.Flavor == "flatcar" {
		options.StringValues = append(options.StringValues, "cloudControllerManager.caCertDir=/usr/share/ca-certificates")
	}

	clusterProxy := input.ClusterProxy.GetWorkloadCluster(ctx, input.ConfigCluster.Namespace, input.ConfigCluster.ClusterName)
	InstallHelmChart(ctx, clusterProxy, defaultNamespace, cloudProviderAzureHelmRepoURL, cloudProviderAzureChartName, cloudProviderAzureHelmReleaseName, options, "")

	// We do this before waiting for the pods to be ready because there is a co-dependency between CNI (nodes ready) and cloud-provider being initialized.
	InstallCNI(ctx, input, cidrBlocks, hasWindows)

	By("Waiting for Ready cloud-controller-manager deployment pods")
	for _, d := range []string{"cloud-controller-manager"} {
		waitInput := GetWaitForDeploymentsAvailableInput(ctx, clusterProxy, d, kubesystem, specName)
		WaitForDeploymentsAvailable(ctx, waitInput, e2eConfig.GetIntervals(specName, "wait-deployment")...)
	}
}

// InstallAzureDiskCSIDriverHelmChart installs the official azure-disk CSI driver helm chart
func InstallAzureDiskCSIDriverHelmChart(ctx context.Context, input clusterctl.ApplyClusterTemplateAndWaitInput, hasWindows bool) {
	specName := "azuredisk-csi-drivers-install"
	By("Installing azure-disk CSI driver components via helm")
	options := &helmVals.Options{
		Values: []string{"controller.replicas=1", "controller.runOnControlPlane=true"},
	}
	// TODO: make this always true once HostProcessContainers are on for all supported k8s versions.
	if hasWindows {
		options.Values = append(options.Values, "windows.useHostProcessContainers=false")
	}
	clusterProxy := input.ClusterProxy.GetWorkloadCluster(ctx, input.ConfigCluster.Namespace, input.ConfigCluster.ClusterName)
	InstallHelmChart(ctx, clusterProxy, kubesystem, azureDiskCSIDriverHelmRepoURL, azureDiskCSIDriverChartName, azureDiskCSIDriverHelmReleaseName, options, "")
	By("Waiting for Ready csi-azuredisk-controller deployment pods")
	for _, d := range []string{"csi-azuredisk-controller"} {
		waitInput := GetWaitForDeploymentsAvailableInput(ctx, clusterProxy, d, kubesystem, specName)
		WaitForDeploymentsAvailable(ctx, waitInput, e2eConfig.GetIntervals(specName, "wait-deployment")...)
	}
}
