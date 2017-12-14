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

package validation

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/openshift/cluster-operator/pkg/apis/clusteroperator"
)

// getValidCluster gets a cluster that passes all validity checks.
func getValidCluster() *clusteroperator.Cluster {
	return &clusteroperator.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: clusteroperator.ClusterSpec{
			MachineSets: []clusteroperator.ClusterMachineSet{
				{
					Name: "master",
					Spec: clusteroperator.MachineSetSpec{
						NodeType: clusteroperator.NodeTypeMaster,
						Infra:    true,
						Size:     1,
					},
				},
			},
		},
	}
}

// getTestComputeMachineSet gets a ClusterMachineSet with node type
// Compute and the specified size.
func getTestComputeMachineSet(size int, name string, infra bool) clusteroperator.ClusterMachineSet {
	return clusteroperator.ClusterMachineSet{
		Name: name,
		Spec: clusteroperator.MachineSetSpec{
			NodeType: clusteroperator.NodeTypeCompute,
			Size:     size,
			Infra:    infra,
		},
	}
}

// getTestMasterMachineSet gets a ClusterMachineSet with node type
// Master and the specified size.
func getTestMasterMachineSet(size int, infra bool) clusteroperator.ClusterMachineSet {
	return clusteroperator.ClusterMachineSet{
		Name: "master",
		Spec: clusteroperator.MachineSetSpec{
			NodeType: clusteroperator.NodeTypeMaster,
			Size:     size,
			Infra:    infra,
		},
	}
}

// TestValidateCluster tests the ValidateCluster function.
func TestValidateCluster(t *testing.T) {
	cases := []struct {
		name    string
		cluster *clusteroperator.Cluster
		valid   bool
	}{
		{
			name:    "valid",
			cluster: getValidCluster(),
			valid:   true,
		},
		{
			name: "invalid name",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Name = "###"
				return c
			}(),
			valid: false,
		},
		{
			name: "invalid spec",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Spec.MachineSets[0].Spec.Size = 0
				return c
			}(),
			valid: false,
		},
		{
			name: "invalid status",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Status.MachineSetCount = -1
				return c
			}(),
			valid: false,
		},
		{
			name: "no master machineset",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Spec.MachineSets[0].Spec.NodeType = clusteroperator.NodeTypeCompute
				return c
			}(),
			valid: false,
		},
		{
			name: "no infra machineset",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Spec.MachineSets[0].Spec.Infra = false
				return c
			}(),
			valid: false,
		},
		{
			name: "more than one master",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Spec.MachineSets = append(c.Spec.MachineSets, clusteroperator.ClusterMachineSet{
					Name: "second",
					Spec: clusteroperator.MachineSetSpec{
						NodeType: clusteroperator.NodeTypeMaster,
						Infra:    false,
						Size:     1,
					},
				})
				return c
			}(),
			valid: false,
		},
		{
			name: "more than one infra",
			cluster: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Spec.MachineSets = append(c.Spec.MachineSets, clusteroperator.ClusterMachineSet{
					Name: "second",
					Spec: clusteroperator.MachineSetSpec{
						NodeType: clusteroperator.NodeTypeCompute,
						Infra:    true,
						Size:     1,
					},
				})
				return c
			}(),
			valid: false,
		},
	}

	for _, tc := range cases {
		errs := ValidateCluster(tc.cluster)
		if len(errs) != 0 && tc.valid {
			t.Errorf("%v: unexpected error: %v", tc.name, errs)
			continue
		} else if len(errs) == 0 && !tc.valid {
			t.Errorf("%v: unexpected success", tc.name)
		}
	}
}

// TestValidateClusterUpdate tests the ValidateClusterUpdate function.
func TestValidateClusterUpdate(t *testing.T) {
	cases := []struct {
		name  string
		old   *clusteroperator.Cluster
		new   *clusteroperator.Cluster
		valid bool
	}{
		{
			name:  "valid",
			old:   getValidCluster(),
			new:   getValidCluster(),
			valid: true,
		},
		{
			name: "invalid spec",
			old:  getValidCluster(),
			new: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Spec.MachineSets[0].Spec.Size = 0
				return c
			}(),
			valid: false,
		},
	}

	for _, tc := range cases {
		errs := ValidateClusterUpdate(tc.new, tc.old)
		if len(errs) != 0 && tc.valid {
			t.Errorf("%v: unexpected error: %v", tc.name, errs)
			continue
		} else if len(errs) == 0 && !tc.valid {
			t.Errorf("%v: unexpected success", tc.name)
		}
	}
}

// TestValidateClusterStatusUpdate tests the ValidateClusterStatusUpdate function.
func TestValidateClusterStatusUpdate(t *testing.T) {
	cases := []struct {
		name  string
		old   *clusteroperator.Cluster
		new   *clusteroperator.Cluster
		valid bool
	}{
		{
			name:  "valid",
			old:   getValidCluster(),
			new:   getValidCluster(),
			valid: true,
		},
		{
			name: "invalid status",
			old:  getValidCluster(),
			new: func() *clusteroperator.Cluster {
				c := getValidCluster()
				c.Status.MachineSetCount = -1
				return c
			}(),
			valid: false,
		},
	}

	for _, tc := range cases {
		errs := ValidateClusterStatusUpdate(tc.new, tc.old)
		if len(errs) != 0 && tc.valid {
			t.Errorf("%v: unexpected error: %v", tc.name, errs)
			continue
		} else if len(errs) == 0 && !tc.valid {
			t.Errorf("%v: unexpected success", tc.name)
		}
	}
}

// TestValidateClusterSpec tests the validateClusterSpec function.
func TestValidateClusterSpec(t *testing.T) {
	cases := []struct {
		name  string
		spec  *clusteroperator.ClusterSpec
		valid bool
	}{
		{
			name: "valid master only",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(1, true),
				},
			},
			valid: true,
		},
		{
			name: "invalid master size",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(0, true),
				},
			},
			valid: false,
		},
		{
			name: "valid single compute",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(1, false),
					getTestComputeMachineSet(1, "one", true),
				},
			},
			valid: true,
		},
		{
			name: "valid multiple computes",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(1, false),
					getTestComputeMachineSet(1, "one", true),
					getTestComputeMachineSet(5, "two", false),
					getTestComputeMachineSet(2, "three", false),
				},
			},
			valid: true,
		},
		{
			name: "invalid compute name",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(1, false),
					getTestComputeMachineSet(1, "one", true),
					getTestComputeMachineSet(5, "", false),
					getTestComputeMachineSet(2, "three", false),
				},
			},
			valid: false,
		},
		{
			name: "invalid compute size",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(1, true),
					getTestComputeMachineSet(1, "one", false),
					getTestComputeMachineSet(0, "two", false),
					getTestComputeMachineSet(2, "three", false),
				},
			},
			valid: false,
		},
		{
			name: "invalid duplicate compute name",
			spec: &clusteroperator.ClusterSpec{
				MachineSets: []clusteroperator.ClusterMachineSet{
					getTestMasterMachineSet(1, false),
					getTestComputeMachineSet(1, "one", true),
					getTestComputeMachineSet(5, "one", false),
					getTestComputeMachineSet(2, "three", false),
				},
			},
			valid: false,
		},
	}

	for _, tc := range cases {
		errs := validateClusterSpec(tc.spec, field.NewPath("spec"))
		if len(errs) != 0 && tc.valid {
			t.Errorf("%v: unexpected error: %v", tc.name, errs)
			continue
		} else if len(errs) == 0 && !tc.valid {
			t.Errorf("%v: unexpected success", tc.name)
		}
	}
}

// TestValidateClusterStatus tests the validateClusterStatus function.
func TestValidateClusterStatus(t *testing.T) {
	cases := []struct {
		name   string
		status *clusteroperator.ClusterStatus
		valid  bool
	}{
		{
			name:   "empty",
			status: &clusteroperator.ClusterStatus{},
			valid:  true,
		},
		{
			name: "positive machinesets",
			status: &clusteroperator.ClusterStatus{
				MachineSetCount: 1,
			},
			valid: true,
		},
		{
			name: "negative machinesets",
			status: &clusteroperator.ClusterStatus{
				MachineSetCount: -1,
			},
			valid: false,
		},
	}

	for _, tc := range cases {
		errs := validateClusterStatus(tc.status, field.NewPath("status"))
		if len(errs) != 0 && tc.valid {
			t.Errorf("%v: unexpected error: %v", tc.name, errs)
			continue
		} else if len(errs) == 0 && !tc.valid {
			t.Errorf("%v: unexpected success", tc.name)
		}
	}
}
