/*
Copyright 2018 The Kubernetes Authors.

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

package infra

import (
	"testing"
	"time"

	v1batch "k8s.io/api/batch/v1"
	kapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeinformers "k8s.io/client-go/informers"
	clientgofake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	clusteroperator "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
	clusteroperatorclientset "github.com/openshift/cluster-operator/pkg/client/clientset_generated/clientset/fake"
	informers "github.com/openshift/cluster-operator/pkg/client/informers_generated/externalversions"
	"github.com/openshift/cluster-operator/pkg/controller"

	"github.com/stretchr/testify/assert"
)

const (
	testNamespace   = "test-namespace"
	testClusterName = "test-cluster"
	testClusterUUID = types.UID("test-cluster-uuid")
)

// newTestController creates a test Controller with fake
// clients and informers.
func newTestController() (
	*Controller,
	cache.Store, // cluster store
	cache.Store, // jobs store
	*clientgofake.Clientset,
	*clusteroperatorclientset.Clientset,
) {
	kubeClient := &clientgofake.Clientset{}
	kubeInformers := kubeinformers.NewSharedInformerFactory(kubeClient, 10*time.Minute)
	clusterOperatorClient := &clusteroperatorclientset.Clientset{}
	clusterOperatorInformers := informers.NewSharedInformerFactory(clusterOperatorClient, 0)

	controller := NewController(
		clusterOperatorInformers.Clusteroperator().V1alpha1().Clusters(),
		kubeInformers.Batch().V1().Jobs(),
		kubeClient,
		clusterOperatorClient,
		"",
		"",
	)

	controller.clustersSynced = alwaysReady

	return controller,
		clusterOperatorInformers.Clusteroperator().V1alpha1().Clusters().Informer().GetStore(),
		kubeInformers.Batch().V1().Jobs().Informer().GetStore(),
		kubeClient,
		clusterOperatorClient
}

// alwaysReady is a function that can be used as a sync function that will
// always indicate that the lister has been synced.
var alwaysReady = func() bool { return true }

// getKey gets the key for the cluster to use when checking expectations
// set on a cluster.
func getKey(cluster *clusteroperator.Cluster, t *testing.T) string {
	key, err := controller.KeyFunc(cluster)
	if err != nil {
		t.Errorf("Unexpected error getting key for Cluster %v: %v", cluster.Name, err)
		return ""
	}
	return key
}

func testCluster() *clusteroperator.Cluster {
	cluster := &clusteroperator.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			UID:       testClusterUUID,
			Name:      testClusterName,
			Namespace: testNamespace,
		},
		Spec: clusteroperator.ClusterSpec{
			Hardware: clusteroperator.ClusterHardwareSpec{
				AWS: &clusteroperator.AWSClusterSpec{},
			},
			MachineSets: []clusteroperator.ClusterMachineSet{
				{
					Name: "master",
					MachineSetConfig: clusteroperator.MachineSetConfig{
						NodeType: clusteroperator.NodeTypeMaster,
						Infra:    true,
						Size:     3,
					},
				},
			},
		},
	}
	return cluster
}

func TestJobOwnerControlGetOwnerKey(t *testing.T) {
	c, _, _, _, _ := newTestController()
	joc := jobOwnerControl{controller: c}

	cluster := testCluster()
	key, err := joc.GetOwnerKey(cluster)
	assert.Nil(t, err, "unexpected error")
	expectedKey := getKey(cluster, t)
	assert.Equal(t, expectedKey, key, "expecting owner key to be foo/bar")
}

func TestJobOwnerControlGetOwner(t *testing.T) {
	c, clusterStore, _, _, _ := newTestController()
	joc := jobOwnerControl{controller: c}

	testCluster := testCluster()
	clusterStore.Add(testCluster)

	obj, err := joc.GetOwner(testNamespace, testClusterName)
	assert.Nil(t, err, "unexpected error")
	assert.Equal(t, testCluster, obj, "expecting owner object to be test cluster")
}

func TestJobOwnerControlOnOwnedJobEvent(t *testing.T) {
	c, _, _, _, _ := newTestController()
	joc := jobOwnerControl{controller: c}

	cluster := testCluster()

	joc.OnOwnedJobEvent(cluster)
	key, _ := c.queue.Get()
	expectedKey := getKey(cluster, t)
	assert.Equal(t, expectedKey, key, "expecting to have test cluster key in the queue")
}

func TestJobSyncStrategyGetOwner(t *testing.T) {
	c, clusterStore, _, _, _ := newTestController()
	jss := jobSyncStrategy{controller: c}

	testCluster := testCluster()
	clusterStore.Add(testCluster)

	obj, err := jss.GetOwner(getKey(testCluster, t))
	assert.Nil(t, err, "unexpected error")
	assert.Equal(t, testCluster, obj, "expecting owner object to be test cluster")
}

func TestJobSyncStrategyDoesOwnerNeedProcessing(t *testing.T) {

	tests := []struct {
		name                  string
		cluster               *clusteroperator.Cluster
		expectNeedsProcessing bool
	}{
		{
			name: "different generation",
			cluster: func() *clusteroperator.Cluster {
				cluster := testCluster()
				cluster.Generation = 2
				cluster.Status.ProvisionedJobGeneration = 1
				return cluster
			}(),
			expectNeedsProcessing: true,
		},
		{
			name: "same generation",
			cluster: func() *clusteroperator.Cluster {
				cluster := testCluster()
				cluster.Generation = 1
				cluster.Status.ProvisionedJobGeneration = 1
				return cluster
			}(),
			expectNeedsProcessing: false,
		},
	}
	for _, test := range tests {
		c, _, _, _, _ := newTestController()
		jss := jobSyncStrategy{controller: c}
		actual, expected := jss.DoesOwnerNeedProcessing(test.cluster), test.expectNeedsProcessing
		assert.Equal(t, actual, expected, "%s: expected result doesn't match", test.name)
	}
}

type fakeAnsibleGenerator func(string, *clusteroperator.ClusterHardwareSpec, string, string, string) (*v1batch.Job, *kapi.ConfigMap)

func (f fakeAnsibleGenerator) GeneratePlaybookJob(name string, hardware *clusteroperator.ClusterHardwareSpec, playbook, inventory, vars string) (*v1batch.Job, *kapi.ConfigMap) {
	return f(name, hardware, playbook, inventory, vars)
}

func TestJobSyncStrategyGetJobFactory(t *testing.T) {
	tests := []struct {
		name             string
		deleting         bool
		expectedPlaybook string
	}{
		{
			name:             "provisioning",
			deleting:         false,
			expectedPlaybook: infraPlaybook,
		},
		{
			name:             "deprovisioning",
			deleting:         true,
			expectedPlaybook: deprovisionInfraPlaybook,
		},
	}

	for _, test := range tests {
		c, _, _, _, _ := newTestController()
		var actualPlaybook string
		generatePlaybook := func(name string, hardware *clusteroperator.ClusterHardwareSpec, playbook, inventory, vars string) (*v1batch.Job, *kapi.ConfigMap) {
			actualPlaybook = playbook
			return nil, nil
		}
		c.ansibleGenerator = fakeAnsibleGenerator(generatePlaybook)
		testCluster := testCluster()

		jss := jobSyncStrategy{controller: c}
		factory, err := jss.GetJobFactory(testCluster, test.deleting)
		assert.Nil(t, err, "unexpected error")
		factory.BuildJob("foo")
		assert.Equal(t, actualPlaybook, test.expectedPlaybook, "unexpected playbook")
	}
}

func TestJobSyncStrategyGetOwnerCurrentJob(t *testing.T) {

}

func TestJobSyncStrategySetOwnerCurrentJob(t *testing.T) {

}

func TestJobSyncStrategyDeepCopyOwner(t *testing.T) {

}

func TestJobSyncStrategySetOwnerJobSyncCondition(t *testing.T) {

}

func TestJobSyncStrategyOnJobCompletion(t *testing.T) {

}

func TestJobSyncStrategyOnJobFailure(t *testing.T) {

}

func TestJobSyncStrategyUpdateOwnerStatus(t *testing.T) {

}

func TestJobSyncStrategyProcessDeletedOwner(t *testing.T) {

}
