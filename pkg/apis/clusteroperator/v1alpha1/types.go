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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Cluster represents a cluster that clusteroperator manages
type Cluster struct {
	// +optional
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterSpec `json:"spec,omitempty"`
	// +optional
	Status ClusterStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ClusterList is a list of Clusters.
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Cluster `json:"items"`
}

type ClusterSpec struct {
	// MasterNodeGroup specificies the configuration of the master node group
	MasterNodeGroup ClusterNodeGroup `json:"masterNodeGroup"`

	// ComputeNodeGroups specify the configurations of the compute node groups
	// +optional
	ComputeNodeGroups []ClusterComputeNodeGroup `json:"computeNodeGroups,omitempty"`
}

type ClusterStatus struct {
	// MasterNodeGroups is the number of actual master node groups that are
	// active for the cluster
	MasterNodeGroups int `json:"masterNodeGroups"`

	// ComputeNodeGroups is the number of actual compute node groups that are
	// active for the cluster
	ComputeNodeGroups int `json:"computeNodeGroups"`
}

// ClusterNodeGroup is a node group defined in a Cluster resource
type ClusterNodeGroup struct {
	Size int `json:"size"`
}

// ClusterComputeNodeGroup is a compute node group defined in a Cluster
// resource
type ClusterComputeNodeGroup struct {
	ClusterNodeGroup `json:",inline"`

	Name string `json:"name"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeGroup represents a group of nodes in a cluster that clusteroperator manages
type NodeGroup struct {
	// +optional
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec NodeGroupSpec `json:"spec,omitempty"`
	// +optional
	Status NodeGroupStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeGroupList is a list of NodeGroups.
type NodeGroupList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []NodeGroup `json:"items"`
}

type NodeGroupSpec struct {
	// NodeType is the type of nodes that comprised the NodeGroup
	NodeType NodeType `json:"nodeType"`

	// Size is the number of nodes that the node group should contain
	Size int `json:"size"`
}

type NodeGroupStatus struct {
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Node represents a node in a cluster that clusteroperator manages
type Node struct {
	// +optional
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec NodeSpec `json:"spec,omitempty"`
	// +optional
	Status NodeStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeList is a list of Nodes.
type NodeList struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []Node `json:"items"`
}

type NodeSpec struct {
	// NodeType is the type of the node
	NodeType NodeType `json:"nodeType"`
}

type NodeStatus struct {
}

// NodeType is the type of the Node
type NodeType string

const (
	// NodeTypeMaster is a node that is a master in the cluster
	NodeTypeMaster NodeType = "Master"
	// NodeTypeCompute is a node that is a compute node in the cluster
	NodeTypeCompute NodeType = "Compute"
)
