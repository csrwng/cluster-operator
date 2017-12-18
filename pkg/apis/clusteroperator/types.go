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

package clusteroperator

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Cluster represents a cluster that clusteroperator manages
type Cluster struct {
	// +optional
	metav1.TypeMeta
	// +optional
	metav1.ObjectMeta

	// +optional
	Spec ClusterSpec
	// +optional
	Status ClusterStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterList is a list of Clusters.
type ClusterList struct {
	metav1.TypeMeta
	// +optional
	metav1.ListMeta

	Items []Cluster
}

// ClusterSpec is the specification of a cluster's hardware and configuration
type ClusterSpec struct {
	// Hardware specifies the hardware that the cluster will run on
	Hardware ClusterHardwareSpec

	// Config specifies cluster-wide OpenShift configuration
	Config ClusterConfigSpec

	// DefaultHardwareSpec specifies hardware defaults for all machine sets
	// in this spec
	// +optional
	DefaultHardwareSpec *MachineSetHardwareSpec

	// MachineSets specifies the configuration of all machine sets for the cluster
	MachineSets []ClusterMachineSet
}

// ClusterHardwareSpec specifies hardware for a cluster. The specification will
// be specific to each cloud provider.
type ClusterHardwareSpec struct {
	// AWS specifies cluster hardware configuration on AWS
	// +optional
	AWS *AWSClusterSpec

	// TODO: Add other cloud-specific Specs as needed
}

// AWSClusterSpec contains cluster-wide configuration for a cluster on AWS
type AWSClusterSpec struct {
	// AccountSeceret refers to a secret that contains the AWS account access
	// credentials
	AccountSecret corev1.LocalObjectReference

	// Region specifies the AWS region where the cluster will be created
	Region string

	// VPCName specifies the name of the VPC to associate with the cluster.
	// If a value is specified, a VPC will be created with that name if it
	// does not already exist in the cloud provider. If it does exist, the
	// existing VPC will be used.
	// If no name is specified, a VPC name will be generated using the
	// cluster name and created in the cloud provider.
	// +optional
	VPCName string

	// VPCSubnet specifies the subnet to use for the cluster's VPC. Only used
	// when a new VPC is created for the cluster
	// +optional
	VPCSubnet string
}

// ClusterConfigSpec contains OpenShift configuration for a cluster
type ClusterConfigSpec struct {
	// DeploymentType indicates the type of OpenShift deployment to create
	DeploymentType ClusterDeploymentType

	// OpenShiftVersion is the version of OpenShift to install
	OpenshiftVersion string

	// SDNPluginName is the name of the SDN plugin to use for this install
	SDNPluginName string

	// ServiceNetworkSubnet is the CIDR to use for service IPs in the cluster
	// +optional
	ServiceNetworkSubnet string

	// PodNetworkSubnet is the CIDR to use for pod IPs in the cluster
	// +optional
	PodNetworkSubnet string
}

// ClusterDeploymentType is a valid value for ClusterConfigSpec.DeploymentType
type ClusterDeploymentType string

// These are valid values for cluster deployment type
const (
	// ClusterDeploymentTypeOrigin is a deployment type of origin
	ClusterDeploymentTypeOrigin ClusterDeploymentType = "origin"
	// ClusterDeploymentTypeAtomicOpenshift is a deployment type of atomic openshift (enterprise)
	ClusterDeploymentTypeAtomicOpenshift ClusterDeploymentType = "atomic-openshift"
)

// ClusterStatus contains the status for a cluster
type ClusterStatus struct {
	// MachineSetCount is the number of actual machine sets that are active for the cluster
	MachineSetCount int

	// MasterMachineSetName is the name of the master machine set
	MasterMachineSetName string

	// InfraMachineSetName is the name of the infra machine set
	InfraMachineSetName string

	// AdminKubeconfig points to a secret containing a cluster administrator
	// kubeconfig to access the cluster. The secret can be used for bootstrapping
	// and subsequent access to the cluster API.
	AdminKubeconfig *corev1.LocalObjectReference

	// Provisioned is true if the hardware pre-reqs for the cluster have been provisioned
	// For machine set hardware, see the status of each machine set resource.
	Provisioned bool

	// Running is true if the master of the cluster is running and can be accessed using
	// the KubeconfigSecret
	Running bool

	// Conditions includes more detailed status for the cluster
	Conditions []ClusterCondition
}

// ClusterCondition contains details for the current condition of a cluster
type ClusterCondition struct {
	// Type is the type of the condition.
	Type ClusterConditionType
	// Status is the status of the condition.
	Status corev1.ConditionStatus
	// LastProbeTime is the last time we probed the condition.
	// +optional
	LastProbeTime metav1.Time
	// LastTransitionTime is the last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time
	// Reason is a unique, one-word, CamelCase reason for the condition's last transition.
	// +optional
	Reason string
	// Message is a human-readable message indicating details about last transition.
	// +optional
	Message string
}

// ClusterConditionType is a valid value for ClusterCondition.Type
type ClusterConditionType string

// These are valid conditions for a cluster
const (
	// ClusterHardwareProvisioned represents status of the hardware provisioning status for this cluster.
	ClusterHardwareProvisioned ClusterConditionType = "HardwareProvisioned"
	// ClusterReady means the cluster is able to service requests
	ClusterReady ClusterConditionType = "Ready"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineSet represents a group of machines in a cluster that clusteroperator manages
type MachineSet struct {
	// +optional
	metav1.TypeMeta
	// +optional
	metav1.ObjectMeta

	// Spec is the specification for the MachineSet
	// +optional
	Spec MachineSetSpec

	// Status is the status for the MachineSet
	// +optional
	Status MachineSetStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineSetList is a list of MachineSets.
type MachineSetList struct {
	metav1.TypeMeta
	// +optional
	metav1.ListMeta

	Items []MachineSet
}

// ClusterMachineSet is the specification of a machine set in a cluster
type ClusterMachineSet struct {
	// Name is a unique name for the machine set within the cluster
	Name string

	// MachineSetConfig is the configuration for the MachineSet
	MachineSetConfig
}

// MachineSetConfig contains configuration for a MachineSet
type MachineSetConfig struct {
	// NodeType is the type of nodes that comprise the MachineSet
	NodeType NodeType

	// Infra indicates whether this machine set should contain infrastructure
	// pods
	Infra bool

	// Size is the number of nodes that the node group should contain
	Size int

	// Hardware defines what the hardware should look like for this
	// MachineSet. The specification will vary based on the cloud provider.
	Hardware MachineSetHardwareSpec

	// NodeLabels specifies the labels that will be applied to nodes in this
	// MachineSet
	NodeLabels map[string]string
}

// MachineSetSpec is the specification for a MachineSet
type MachineSetSpec struct {
	// MachineSetConfig is the configuration for the MachineSet
	MachineSetConfig
}

// MachineSetHardwareSpec specifies the hardware for a MachineSet
type MachineSetHardwareSpec struct {
	// AWS specifies the hardware spec for an AWS MachineSet
	// +optional
	AWS *MachineSetAWSHardwareSpec
}

// MachineSetAWSHardwareSpec specifies AWS hardware for a MachineSet
type MachineSetAWSHardwareSpec struct {
	// InstanceType is the type of instance to use for machines in this MachineSet
	InstanceType string

	// AMIName is the name of the AMI to use for machines in this MachineSet
	AMIName string
}

// MachineSetStatus is the status of a MachineSet
type MachineSetStatus struct {
	// MachinesProvisioned is the count of provisioned machines for the MachineSet
	MachinesProvisioned int

	// MachinesReady is the number of nodes that are ready
	MachinesReady int

	// Conditions includes more detailed status of the MachineSet
	Conditions []MachineSetCondition
}

// MachineSetCondition contains details for the current condition of a MachineSet
type MachineSetCondition struct {
	// Type is the type of the condition.
	Type MachineSetConditionType
	// Status is the status of the condition.
	Status corev1.ConditionStatus
	// LastProbeTime is the last time we probed the condition.
	// +optional
	LastProbeTime metav1.Time
	// LastTransitionTime is the last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time
	// Reason is a unique, one-word, CamelCase reason for the condition's last transition.
	// +optional
	Reason string
	// Message is a human-readable message indicating details about last transition.
	// +optional
	Message string
}

// MachineSetConditionType is a valid value for MachineSetCondition.Type
type MachineSetConditionType string

// These are valid conditions for a node group
const (
	// MachineSetHardwareCreated is true if the corresponding cloud resource(s) for
	// this nodegroup have been created (ie. AWS autoscaling group)
	MachineSetHardwareCreated MachineSetConditionType = "HardwareCreated"

	// MachineSetHardwareReady is true if the hardware for the nodegroup is in ready
	// state (is started and healthy)
	MachineSetHardwareReady MachineSetConditionType = "HardwareReady"

	// MachineSetReady is true if the nodes of this nodegroup are ready for work
	// (have joined the cluster and have a healthy node status)
	MachineSetReady ClusterConditionType = "Ready"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Machine represents a member of a MachineSet in a cluster that clusteroperator manages
type Machine struct {
	// +optional
	metav1.TypeMeta
	// +optional
	metav1.ObjectMeta

	// +optional
	Spec MachineSpec
	// +optional
	Status MachineStatus
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MachineList is a list of Machines.
type MachineList struct {
	metav1.TypeMeta
	// +optional
	metav1.ListMeta

	Items []Machine
}

type MachineSpec struct {
	// NodeType is the type of the node in this machine
	NodeType NodeType
}

type MachineStatus struct {
}

// NodeType is the type of the Node
type NodeType string

const (
	// NodeTypeMaster is a node that is a master in the cluster
	NodeTypeMaster NodeType = "Master"
	// NodeTypeCompute is a node that is a compute node in the cluster
	NodeTypeCompute NodeType = "Compute"
)
