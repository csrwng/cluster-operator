/*
Copyright 2017 The Kubernetes Authors.

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

package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/golang/glog"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	jsonserializer "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/selection"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/tools/cache"

	"github.com/openshift/cluster-operator/pkg/api"
	cov1 "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
	colisters "github.com/openshift/cluster-operator/pkg/client/listers_generated/clusteroperator/v1alpha1"
	capiv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	capilister "sigs.k8s.io/cluster-api/pkg/client/listers_generated/cluster/v1alpha1"
)

const (
	// 32 = maximum ELB name length
	// 7 = length of longest ELB name suffix ("-cp-ext")
	maxELBBasenameLen = 32 - 7

	// JobTypeLabel is the label to apply to jobs and configmaps that are used
	// to execute the type of job.
	JobTypeLabel = "job-type"
)

var (
	// KeyFunc returns the key identifying a cluster-operator resource.
	KeyFunc = cache.DeletionHandlingMetaNamespaceKeyFunc

	// ClusterDeploymentKind is the GVK for a ClusterDeployment.
	ClusterDeploymentKind = cov1.SchemeGroupVersion.WithKind("ClusterDeployment")

	clusterKind = capiv1.SchemeGroupVersion.WithKind("Cluster")
)

// WaitForCacheSync is a wrapper around cache.WaitForCacheSync that generates log messages
// indicating that the controller identified by controllerName is waiting for syncs, followed by
// either a successful or failed sync.
func WaitForCacheSync(controllerName string, stopCh <-chan struct{}, cacheSyncs ...cache.InformerSynced) bool {
	glog.Infof("Waiting for caches to sync for %s controller", controllerName)

	if !cache.WaitForCacheSync(stopCh, cacheSyncs...) {
		utilruntime.HandleError(fmt.Errorf("Unable to sync caches for %s controller", controllerName))
		return false
	}

	glog.Infof("Caches are synced for %s controller", controllerName)
	return true
}

// UpdateConditionCheck tests whether a condition should be updated from the
// old condition to the new condition. Returns true if the condition should
// be updated.
type UpdateConditionCheck func(oldReason, oldMessage, newReason, newMessage string) bool

// UpdateConditionAlways returns true. The condition will always be updated.
func UpdateConditionAlways(_, _, _, _ string) bool {
	return true
}

// UpdateConditionNever return false. The condition will never be updated,
// unless there is a change in the status of the condition.
func UpdateConditionNever(_, _, _, _ string) bool {
	return false
}

// UpdateConditionIfReasonOrMessageChange returns true if there is a change
// in the reason or the message of the condition.
func UpdateConditionIfReasonOrMessageChange(oldReason, oldMessage, newReason, newMessage string) bool {
	return oldReason != newReason ||
		oldMessage != newMessage
}

func shouldUpdateCondition(
	oldStatus corev1.ConditionStatus, oldReason, oldMessage string,
	newStatus corev1.ConditionStatus, newReason, newMessage string,
	updateConditionCheck UpdateConditionCheck,
) bool {
	if oldStatus != newStatus {
		return true
	}
	return updateConditionCheck(oldReason, oldMessage, newReason, newMessage)
}

// SetClusterCondition sets the condition for the cluster and returns the new slice of conditions.
// If the cluster does not already have a condition with the specified type,
// a condition will be added to the slice if and only if the specified
// status is True.
// If the cluster does already have a condition with the specified type,
// the condition will be updated if either of the following are true.
// 1) Requested status is different than existing status.
// 2) The updateConditionCheck function returns true.
func SetClusterCondition(
	conditions []cov1.ClusterCondition,
	conditionType cov1.ClusterConditionType,
	status corev1.ConditionStatus,
	reason string,
	message string,
	updateConditionCheck UpdateConditionCheck,
) []cov1.ClusterCondition {
	now := metav1.Now()
	existingCondition := FindClusterCondition(conditions, conditionType)
	if existingCondition == nil {
		if status == corev1.ConditionTrue {
			conditions = append(
				conditions,
				cov1.ClusterCondition{
					Type:               conditionType,
					Status:             status,
					Reason:             reason,
					Message:            message,
					LastTransitionTime: now,
					LastProbeTime:      now,
				},
			)
		}
	} else {
		if shouldUpdateCondition(
			existingCondition.Status, existingCondition.Reason, existingCondition.Message,
			status, reason, message,
			updateConditionCheck,
		) {
			if existingCondition.Status != status {
				existingCondition.LastTransitionTime = now
			}
			existingCondition.Status = status
			existingCondition.Reason = reason
			existingCondition.Message = message
			existingCondition.LastProbeTime = now
		}
	}
	return conditions
}

// FindClusterCondition finds in the cluster the condition that has the
// specified condition type. If none exists, then returns nil.
func FindClusterCondition(conditions []cov1.ClusterCondition, conditionType cov1.ClusterConditionType) *cov1.ClusterCondition {
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// SetClusterDeploymentCondition sets the condition for the cluster deployment
// and returns the new slice of conditions.
// If the deployment does not already have a condition with the specified type,
// a condition will be added to the slice if and only if the specified
// status is True.
// If the deployment does already have a condition with the specified type,
// the condition will be updated if either of the following are true.
// 1) Requested status is different than existing status.
// 2) The updateConditionCheck function returns true.
func SetClusterDeploymentCondition(
	conditions []cov1.ClusterDeploymentCondition,
	conditionType cov1.ClusterDeploymentConditionType,
	status corev1.ConditionStatus,
	reason string,
	message string,
	updateConditionCheck UpdateConditionCheck,
) []cov1.ClusterDeploymentCondition {
	now := metav1.Now()
	existingCondition := FindClusterDeploymentCondition(conditions, conditionType)
	if existingCondition == nil {
		if status == corev1.ConditionTrue {
			conditions = append(
				conditions,
				cov1.ClusterDeploymentCondition{
					Type:               conditionType,
					Status:             status,
					Reason:             reason,
					Message:            message,
					LastTransitionTime: now,
					LastProbeTime:      now,
				},
			)
		}
	} else {
		if shouldUpdateCondition(
			existingCondition.Status, existingCondition.Reason, existingCondition.Message,
			status, reason, message,
			updateConditionCheck,
		) {
			if existingCondition.Status != status {
				existingCondition.LastTransitionTime = now
			}
			existingCondition.Status = status
			existingCondition.Reason = reason
			existingCondition.Message = message
			existingCondition.LastProbeTime = now
		}
	}
	return conditions
}

// FindClusterDeploymentCondition finds in the cluster deployment the condition that has the
// specified condition type. If none exists, then returns nil.
func FindClusterDeploymentCondition(conditions []cov1.ClusterDeploymentCondition, conditionType cov1.ClusterDeploymentConditionType) *cov1.ClusterDeploymentCondition {
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// SetAWSMachineCondition sets the condition for the machine and
// returns the new slice of conditions.
// If the machine does not already have a condition with the specified type,
// a condition will be added to the slice if and only if the specified
// status is True.
// If the machine does already have a condition with the specified type,
// the condition will be updated if either of the following are true.
// 1) Requested status is different than existing status.
// 2) The updateConditionCheck function returns true.
func SetAWSMachineCondition(
	conditions []cov1.AWSMachineCondition,
	conditionType cov1.AWSMachineConditionType,
	status corev1.ConditionStatus,
	reason string,
	message string,
	updateConditionCheck UpdateConditionCheck,
) []cov1.AWSMachineCondition {
	now := metav1.Now()
	existingCondition := FindAWSMachineCondition(conditions, conditionType)
	if existingCondition == nil {
		if status == corev1.ConditionTrue {
			conditions = append(
				conditions,
				cov1.AWSMachineCondition{
					Type:               conditionType,
					Status:             status,
					Reason:             reason,
					Message:            message,
					LastTransitionTime: now,
					LastProbeTime:      now,
				},
			)
		}
	} else {
		if shouldUpdateCondition(
			existingCondition.Status, existingCondition.Reason, existingCondition.Message,
			status, reason, message,
			updateConditionCheck,
		) {
			if existingCondition.Status != status {
				existingCondition.LastTransitionTime = now
			}
			existingCondition.Status = status
			existingCondition.Reason = reason
			existingCondition.Message = message
			existingCondition.LastProbeTime = now
		}
	}
	return conditions
}

// FindAWSMachineCondition finds in the machine the condition that has the
// specified condition type. If none exists, then returns nil.
func FindAWSMachineCondition(conditions []cov1.AWSMachineCondition, conditionType cov1.AWSMachineConditionType) *cov1.AWSMachineCondition {
	for i, condition := range conditions {
		if condition.Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// GetObjectController get the controlling owner for the specified object.
// If there is no controlling owner or the controller owner does not have
// the specified kind, then returns nil for the controller.
func GetObjectController(
	obj metav1.Object,
	controllerKind schema.GroupVersionKind,
	getController func(name string) (metav1.Object, error),
) (metav1.Object, error) {
	controllerRef := metav1.GetControllerOf(obj)
	if controllerRef == nil {
		return nil, nil
	}
	if apiVersion, kind := controllerKind.ToAPIVersionAndKind(); controllerRef.APIVersion != apiVersion ||
		controllerRef.Kind != kind {
		return nil, nil
	}
	controller, err := getController(controllerRef.Name)
	if err != nil {
		return nil, err
	}
	if controller == nil {
		return nil, nil
	}
	if controller.GetUID() != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil, nil
	}
	return controller, nil
}

// ClusterForMachineSet retrieves the cluster to which a machine set belongs.
func ClusterForMachineSet(machineSet *capiv1.MachineSet, clusterLister capilister.ClusterLister) (*capiv1.Cluster, error) {
	if machineSet.Labels == nil {
		return nil, fmt.Errorf("missing %s label", cov1.ClusterNameLabel)
	}
	clusterName, ok := machineSet.Labels[cov1.ClusterNameLabel]
	if !ok {
		return nil, fmt.Errorf("missing %s label", cov1.ClusterNameLabel)
	}
	return clusterLister.Clusters(machineSet.Namespace).Get(clusterName)
}

// MachineSetsForCluster retrieves the machinesets associated with the
// specified cluster name.
func MachineSetsForCluster(namespace string, clusterName string, machineSetsLister capilister.MachineSetLister) ([]*capiv1.MachineSet, error) {
	requirement, err := labels.NewRequirement(cov1.ClusterNameLabel, selection.Equals, []string{clusterName})
	if err != nil {
		return nil, err
	}
	return machineSetsLister.MachineSets(namespace).List(labels.NewSelector().Add(*requirement))
}

// ClusterDeploymentForCluster retrieves the cluster deployment that owns the cluster.
func ClusterDeploymentForCluster(cluster *capiv1.Cluster, clusterDeploymentsLister colisters.ClusterDeploymentLister) (*cov1.ClusterDeployment, error) {
	controller, err := GetObjectController(
		cluster,
		ClusterDeploymentKind,
		func(name string) (metav1.Object, error) {
			return clusterDeploymentsLister.ClusterDeployments(cluster.Namespace).Get(name)
		},
	)
	if err != nil {
		return nil, err
	}
	if controller == nil {
		return nil, nil
	}
	clusterDeployment, ok := controller.(*cov1.ClusterDeployment)
	if !ok {
		return nil, fmt.Errorf("Could not convert controller into a ClusterDeployment")
	}
	return clusterDeployment, nil
}

// ClusterDeploymentForMachineSet retrieves the cluster deployment that owns the machine set.
func ClusterDeploymentForMachineSet(machineSet *capiv1.MachineSet, clusterDeploymentsLister colisters.ClusterDeploymentLister) (*cov1.ClusterDeployment, error) {
	controller, err := GetObjectController(
		machineSet,
		ClusterDeploymentKind,
		func(name string) (metav1.Object, error) {
			return clusterDeploymentsLister.ClusterDeployments(machineSet.Namespace).Get(name)
		},
	)
	if err != nil {
		return nil, err
	}
	if controller == nil {
		return nil, nil
	}
	clusterDeployment, ok := controller.(*cov1.ClusterDeployment)
	if !ok {
		return nil, fmt.Errorf("Could not convert controller into a ClusterDeployment")
	}
	return clusterDeployment, nil
}

// JobLabelsForClusterController returns the labels to apply to a job doing a task
// for the specified cluster.
// The cluster parameter is a metav1.Object because it could be either a
// cluster-operator Cluster, and cluster-api Cluster, or a CombinedCluster.
func JobLabelsForClusterController(cluster metav1.Object, jobType string) map[string]string {
	return map[string]string{
		cov1.ClusterNameLabel: cluster.GetName(),
		JobTypeLabel:          jobType,
	}
}

// AddLabels add the additional labels to the existing labels of the object.
func AddLabels(obj metav1.Object, additionalLabels map[string]string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		obj.SetLabels(labels)
	}
	for k, v := range additionalLabels {
		labels[k] = v
	}
}

// AWSClusterProviderConfigFromCluster gets the AWSClusterProviderConfig from the
// specified cluster-api Cluster.
func AWSClusterProviderConfigFromCluster(cluster *capiv1.Cluster) (*cov1.AWSClusterProviderConfig, error) {
	if cluster.Spec.ProviderConfig.Value == nil {
		return nil, fmt.Errorf("No Value in ProviderConfig")
	}
	obj, gvk, err := api.Codecs.UniversalDecoder(cov1.SchemeGroupVersion).Decode([]byte(cluster.Spec.ProviderConfig.Value.Raw), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("could not decode ProviderConfig: %v", err)
	}
	spec, ok := obj.(*cov1.AWSClusterProviderConfig)
	if !ok {
		return nil, fmt.Errorf("Unexpected object: %#v", gvk)
	}
	return spec, nil
}

// ClusterProviderStatusFromCluster gets the cluster-operator provider status from the
// specified cluster-api Cluster.
func ClusterProviderStatusFromCluster(cluster *capiv1.Cluster) (*cov1.ClusterProviderStatus, error) {
	if cluster.Status.ProviderStatus == nil {
		return &cov1.ClusterProviderStatus{}, nil
	}
	obj, gvk, err := api.Codecs.UniversalDecoder(cov1.SchemeGroupVersion).Decode([]byte(cluster.Status.ProviderStatus.Raw), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("could not decode ProviderStatus: %v", err)
	}
	status, ok := obj.(*cov1.ClusterProviderStatus)
	if !ok {
		return nil, fmt.Errorf("Unexpected object: %#v", gvk)
	}
	return status, nil
}

// BuildAWSClusterProviderConfig returns an encoded cloud provider specific RawExtension
// for use in the cluster spec's ProviderConfig.
func BuildAWSClusterProviderConfig(clusterDeploymentSpec *cov1.ClusterDeploymentSpec, cv cov1.ClusterVersionSpec) (*runtime.RawExtension, error) {
	// TODO: drop this error when we're ready to support other clouds
	if clusterDeploymentSpec.Hardware.AWS == nil {
		return nil, fmt.Errorf("no AWS hardware was defined")
	}
	clusterProviderConfigSpec := &cov1.AWSClusterProviderConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cov1.SchemeGroupVersion.String(),
			Kind:       "AWSClusterProviderConfig",
		},
		Hardware: *clusterDeploymentSpec.Hardware.AWS,
		OpenShiftConfig: cov1.OpenShiftDistributionConfig{
			ClusterConfigSpec: clusterDeploymentSpec.Config,
			Version: cov1.OpenShiftConfigVersion{
				DeploymentType: cv.DeploymentType,
				Version:        cv.Version,
				Images:         cv.Images,
			},
		},
	}
	if clusterDeploymentSpec.DefaultHardwareSpec != nil && clusterDeploymentSpec.DefaultHardwareSpec.AWS != nil {
		clusterProviderConfigSpec.Hardware.Defaults = clusterDeploymentSpec.DefaultHardwareSpec.AWS
	}
	serializer := jsonserializer.NewSerializer(jsonserializer.DefaultMetaFactory, api.Scheme, api.Scheme, false)
	var buffer bytes.Buffer
	err := serializer.Encode(clusterProviderConfigSpec, &buffer)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{
		Raw: buffer.Bytes(),
	}, nil
}

// EncodeClusterProviderStatus gets the cluster-api ProviderStatus
// storing the cluster-operator ClusterStatus.
func EncodeClusterProviderStatus(clusterProviderStatus *cov1.ClusterProviderStatus) (*runtime.RawExtension, error) {
	clusterProviderStatus.TypeMeta = metav1.TypeMeta{
		APIVersion: cov1.SchemeGroupVersion.String(),
		Kind:       "ClusterProviderStatus",
	}

	serializer := jsonserializer.NewSerializer(jsonserializer.DefaultMetaFactory, api.Scheme, api.Scheme, false)
	var buffer bytes.Buffer
	err := serializer.Encode(clusterProviderStatus, &buffer)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{
		Raw: bytes.TrimSpace(buffer.Bytes()),
	}, nil
}

// MachineSetSpecFromClusterAPIMachineSpec gets the cluster-operator MachineSetSpec from the
// specified cluster-api MachineSet.
func MachineSetSpecFromClusterAPIMachineSpec(ms *capiv1.MachineSpec) (*cov1.MachineSetSpec, error) {
	if ms.ProviderConfig.Value == nil {
		return nil, fmt.Errorf("No Value in ProviderConfig")
	}
	obj, gvk, err := api.Codecs.UniversalDecoder(cov1.SchemeGroupVersion).Decode([]byte(ms.ProviderConfig.Value.Raw), nil, nil)
	if err != nil {
		return nil, err
	}
	spec, ok := obj.(*cov1.MachineSetProviderConfigSpec)
	if !ok {
		return nil, fmt.Errorf("Unexpected object: %#v", gvk)
	}
	return &spec.MachineSetSpec, nil
}

// BuildCluster builds a cluster for the given cluster deployment.
func BuildCluster(clusterDeployment *cov1.ClusterDeployment, cv cov1.ClusterVersionSpec) (*capiv1.Cluster, error) {
	cluster := &capiv1.Cluster{}
	cluster.Name = clusterDeployment.Spec.ClusterName
	cluster.Labels = clusterDeployment.Labels
	cluster.Annotations = clusterDeployment.Annotations
	cluster.Namespace = clusterDeployment.Namespace
	if cluster.Labels == nil {
		cluster.Labels = make(map[string]string)
	}
	if cluster.Annotations == nil {
		cluster.Annotations = make(map[string]string)
	}
	cluster.Labels[cov1.ClusterDeploymentLabel] = clusterDeployment.Name
	blockOwnerDeletion := false
	controllerRef := metav1.NewControllerRef(clusterDeployment, ClusterDeploymentKind)
	controllerRef.BlockOwnerDeletion = &blockOwnerDeletion
	cluster.OwnerReferences = []metav1.OwnerReference{*controllerRef}
	providerConfig, err := BuildAWSClusterProviderConfig(&clusterDeployment.Spec, cv)
	if err != nil {
		return nil, err
	}
	cluster.Spec.ProviderConfig.Value = providerConfig

	// Set networking defaults
	cluster.Spec.ClusterNetwork = clusterDeployment.Spec.NetworkConfig
	return cluster, nil
}

// MachineProviderConfigFromMachineSetSpec gets the cluster-api ProviderConfig for a Machine template
// to store the cluster-operator MachineSetSpec.
func MachineProviderConfigFromMachineSetSpec(machineSetSpec *cov1.MachineSetSpec) (*runtime.RawExtension, error) {
	msProviderConfigSpec := &cov1.MachineSetProviderConfigSpec{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cov1.SchemeGroupVersion.String(),
			Kind:       "MachineSetProviderConfigSpec",
		},
		MachineSetSpec: *machineSetSpec,
	}
	serializer := jsonserializer.NewSerializer(jsonserializer.DefaultMetaFactory, api.Scheme, api.Scheme, false)
	var buffer bytes.Buffer
	err := serializer.Encode(msProviderConfigSpec, &buffer)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{
		Raw: bytes.TrimSpace(buffer.Bytes()),
	}, nil
}

// AWSMachineProviderStatusFromClusterAPIMachine gets the cluster-operator MachineSetSpec from the
// specified cluster-api MachineSet.
func AWSMachineProviderStatusFromClusterAPIMachine(m *capiv1.Machine) (*cov1.AWSMachineProviderStatus, error) {
	return AWSMachineProviderStatusFromMachineStatus(&m.Status)
}

// AWSMachineProviderStatusFromMachineStatus gets the cluster-operator AWSMachineProviderStatus from the
// specified cluster-api MachineStatus.
func AWSMachineProviderStatusFromMachineStatus(s *capiv1.MachineStatus) (*cov1.AWSMachineProviderStatus, error) {
	if s.ProviderStatus == nil {
		return &cov1.AWSMachineProviderStatus{}, nil
	}
	obj, gvk, err := api.Codecs.UniversalDecoder(cov1.SchemeGroupVersion).Decode([]byte(s.ProviderStatus.Raw), nil, nil)
	if err != nil {
		return nil, err
	}
	status, ok := obj.(*cov1.AWSMachineProviderStatus)
	if !ok {
		return nil, fmt.Errorf("Unexpected object: %#v", gvk)
	}
	return status, nil
}

// EncodeAWSMachineProviderStatus gets the cluster-api ProviderConfig for a Machine template
// to store the cluster-operator MachineSetSpec.
func EncodeAWSMachineProviderStatus(awsStatus *cov1.AWSMachineProviderStatus) (*runtime.RawExtension, error) {
	awsStatus.TypeMeta = metav1.TypeMeta{
		APIVersion: cov1.SchemeGroupVersion.String(),
		Kind:       "AWSMachineProviderStatus",
	}
	serializer := jsonserializer.NewSerializer(jsonserializer.DefaultMetaFactory, api.Scheme, api.Scheme, false)
	var buffer bytes.Buffer
	err := serializer.Encode(awsStatus, &buffer)
	if err != nil {
		return nil, err
	}
	return &runtime.RawExtension{
		Raw: bytes.TrimSpace(buffer.Bytes()),
	}, nil
}

// MachineProviderConfigFromMachineSetConfig returns a RawExtension with a machine ProviderConfig from a MachineSetConfig
func MachineProviderConfigFromMachineSetConfig(machineSetConfig *cov1.MachineSetConfig, clusterDeploymentSpec *cov1.ClusterDeploymentSpec, clusterVersion *cov1.ClusterVersion) (*runtime.RawExtension, error) {
	msSpec := &cov1.MachineSetSpec{
		MachineSetConfig: *machineSetConfig,
	}
	vmImage, err := getImage(clusterDeploymentSpec, clusterVersion)
	if err != nil {
		return nil, err
	}
	msSpec.VMImage = *vmImage

	// use cluster defaults for hardware spec if unset:
	hwSpec, err := ApplyDefaultMachineSetHardwareSpec(msSpec.Hardware, clusterDeploymentSpec.DefaultHardwareSpec)
	if err != nil {
		return nil, err
	}
	msSpec.Hardware = hwSpec

	// Copy cluster hardware onto the provider config as well. Needed when deleting a cluster in the actuator.
	msSpec.ClusterHardware = clusterDeploymentSpec.Hardware

	return MachineProviderConfigFromMachineSetSpec(msSpec)
}

// getImage returns a specific image for the given machine and cluster version.
func getImage(clusterSpec *cov1.ClusterDeploymentSpec, clusterVersion *cov1.ClusterVersion) (*cov1.VMImage, error) {
	if clusterSpec.Hardware.AWS == nil {
		return nil, fmt.Errorf("no AWS hardware defined for cluster")
	}

	if clusterVersion.Spec.VMImages.AWSImages == nil {
		return nil, fmt.Errorf("no AWS images defined for cluster version")
	}

	for _, regionAMI := range clusterVersion.Spec.VMImages.AWSImages.RegionAMIs {
		if regionAMI.Region == clusterSpec.Hardware.AWS.Region {
			ami := regionAMI.AMI
			return &cov1.VMImage{
				AWSImage: &ami,
			}, nil
		}
	}

	return nil, fmt.Errorf("no AWS image defined for region %s", clusterSpec.Hardware.AWS.Region)
}

// ApplyDefaultMachineSetHardwareSpec merges the cluster-wide hardware defaults with the machineset specific hardware specified.
func ApplyDefaultMachineSetHardwareSpec(machineSetHardwareSpec, defaultHardwareSpec *cov1.MachineSetHardwareSpec) (*cov1.MachineSetHardwareSpec, error) {
	if defaultHardwareSpec == nil {
		return machineSetHardwareSpec, nil
	}
	defaultHwSpecJSON, err := json.Marshal(defaultHardwareSpec)
	if err != nil {
		return nil, err
	}
	specificHwSpecJSON, err := json.Marshal(machineSetHardwareSpec)
	if err != nil {
		return nil, err
	}
	merged, err := strategicpatch.StrategicMergePatch(defaultHwSpecJSON, specificHwSpecJSON, machineSetHardwareSpec)
	mergedSpec := &cov1.MachineSetHardwareSpec{}
	if err = json.Unmarshal(merged, mergedSpec); err != nil {
		return nil, err
	}
	return mergedSpec, nil
}

// GetInfraSize gets the size of the infra machine set for the cluster.
func GetInfraSize(cluster *cov1.CombinedCluster) (int, error) {
	return 1, nil
	/*
		for _, ms := range cluster.ClusterDeploymentSpec.MachineSets {
			if ms.Infra {
				return ms.Size, nil
			}
		}
		return 0, fmt.Errorf("no machineset of type Infra found")
	*/
}

// GetMasterMachineSet gets the master machine set for the cluster.
func GetMasterMachineSet(cluster *cov1.CombinedCluster, machineSetLister capilister.MachineSetLister) (*capiv1.MachineSet, error) {
	machineSets, err := MachineSetsForCluster(cluster.Namespace, cluster.Name, machineSetLister)
	if err != nil {
		return nil, err
	}
	// Given that compute/infra machinesets get created on the target cluster, only
	// the master machineset is expected to exist.
	for _, machineSet := range machineSets {
		if machineSet.DeletionTimestamp.IsZero() {
			return machineSet, nil
		}
	}
	return nil, fmt.Errorf("no master machineset found")
}

// MachineIsMaster returns true if the machine is part of a cluster's control plane
func MachineIsMaster(machine *capiv1.Machine) (bool, error) {
	coMachineSetSpec, err := MachineSetSpecFromClusterAPIMachineSpec(&machine.Spec)
	if err != nil {
		return false, err
	}
	return coMachineSetSpec.NodeType == cov1.NodeTypeMaster, nil
}

// MachineIsInfra returns true if the machine is part of a cluster's control plane
func MachineIsInfra(machine *capiv1.Machine) (bool, error) {
	coMachineSetSpec, err := MachineSetSpecFromClusterAPIMachineSpec(&machine.Spec)
	if err != nil {
		return false, err
	}
	return coMachineSetSpec.Infra, nil
}

// ELBMasterExternalName gets the name of the external master ELB for the cluster
// with the specified cluster ID.
func ELBMasterExternalName(clusterID string) string {
	return trimForELBBasename(clusterID, maxELBBasenameLen) + "-cp-ext"
}

// ELBMasterInternalName gets the name of the internal master ELB for the cluster
// with the specified cluster ID.
func ELBMasterInternalName(clusterID string) string {
	return trimForELBBasename(clusterID, maxELBBasenameLen) + "-cp-int"
}

// ELBInfraName gets the name of the infra ELB for the cluster
// with the specified cluster ID.
func ELBInfraName(clusterID string) string {
	return trimForELBBasename(clusterID, maxELBBasenameLen) + "-infra"
}

// trimForELBBasename takes a string and trims it so that it can be used as the
// basename for an ELB.
func trimForELBBasename(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	lastHyphen := strings.LastIndexByte(s, '-')
	if lastHyphen < 0 {
		return s[:maxLen]
	}
	suffix := s[lastHyphen+1:]
	if len(suffix) > maxLen {
		return suffix[:maxLen]
	}
	if len(suffix)+1 >= maxLen {
		return suffix
	}
	return s[:maxLen-len(suffix)-1] + "-" + suffix
}

// BuildMachineSet returns a clusterapi.MachineSet from the combination of various clusteroperator
// objects (ClusterMachineSet/ClusterDeploymentSpec/ClusterVersion) in the provided 'namespace'
func BuildMachineSet(ms *cov1.ClusterMachineSet, clusterDeploymentSpec *cov1.ClusterDeploymentSpec, clusterVersion *cov1.ClusterVersion, namespace string) (*capiv1.MachineSet, error) {
	machineSetName := fmt.Sprintf("%s-%s", clusterDeploymentSpec.ClusterName, ms.ShortName)
	capiMachineSet := capiv1.MachineSet{}
	capiMachineSet.Name = machineSetName
	capiMachineSet.Namespace = namespace
	replicas := int32(ms.Size)
	capiMachineSet.Spec.Replicas = &replicas
	machineTemplate := capiv1.MachineTemplateSpec{}
	labels := map[string]string{
		cov1.MachineSetNameLabel: machineSetName,
		cov1.ClusterNameLabel:    clusterDeploymentSpec.ClusterName,
	}
	capiMachineSet.Labels = labels
	// Assign the same set of labels for the machine template and the selector:
	capiMachineSet.Spec.Selector.MatchLabels = labels
	machineTemplate.Labels = labels

	// We also want to apply labels to the resulting nodes:
	machineTemplate.Spec.Labels = map[string]string{}
	for k, v := range labels {
		machineTemplate.Spec.Labels[k] = v
	}
	for k, v := range ms.NodeLabels {
		machineTemplate.Spec.Labels[k] = v
	}

	machineTemplate.Spec.Taints = ms.NodeTaints

	capiMachineSet.Spec.Template = machineTemplate

	providerConfig, err := MachineProviderConfigFromMachineSetConfig(&ms.MachineSetConfig, clusterDeploymentSpec, clusterVersion)
	if err != nil {
		return nil, err
	}
	capiMachineSet.Spec.Template.Spec.ProviderConfig.Value = providerConfig

	return &capiMachineSet, nil
}

// StringPtrsEqual safely returns true if the value for each string pointer is equal, or both are nil.
func StringPtrsEqual(s1, s2 *string) bool {
	if s1 == s2 {
		return true
	}
	if s1 == nil || s2 == nil {
		return false
	}
	return *s1 == *s2
}

// HasFinalizer returns true if the given object has the given finalizer
func HasFinalizer(object metav1.Object, finalizer string) bool {
	for _, f := range object.GetFinalizers() {
		if f == finalizer {
			return true
		}
	}
	return false
}

// AddFinalizer adds a finalizer to the given object
func AddFinalizer(object metav1.Object, finalizer string) {
	finalizers := sets.NewString(object.GetFinalizers()...)
	finalizers.Insert(finalizer)
	object.SetFinalizers(finalizers.List())
}

// DeleteFinalizer removes a finalizer from the given object
func DeleteFinalizer(object metav1.Object, finalizer string) {
	finalizers := sets.NewString(object.GetFinalizers()...)
	finalizers.Delete(finalizer)
	object.SetFinalizers(finalizers.List())
}

// GetSecretNameFromMachineSetSpec will retrieve the secret name holding AWS credentials
// from a machcinesetspec if it exists. Otherwise it will return an empty string.
func GetSecretNameFromMachineSetSpec(msSpec *cov1.MachineSetSpec) (string, error) {
	secretName := ""
	if msSpec.ClusterHardware.AWS == nil {
		return "", fmt.Errorf("no AWS cluster hardware set on machine spec")
	}

	if msSpec.ClusterHardware.AWS.AccountSecret.Name != "" {
		secretName = msSpec.ClusterHardware.AWS.AccountSecret.Name
	}

	return secretName, nil
}

// RegistryObjectStoreName takes the clusterID and returns what the remote object store
// name should be for the cluster
func RegistryObjectStoreName(clusterID string) string {
	return clusterID + "-registry"
}

// RegistryCredsSecretName return the name we should store the registry secrets into
// given the clusterID
func RegistryCredsSecretName(clusterID string) string {
	return clusterID + "-registry"
}
