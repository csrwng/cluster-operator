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

package infra

import (
	"fmt"
	"strings"
	"time"

	v1batch "k8s.io/api/batch/v1"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	batchinformers "k8s.io/client-go/informers/batch/v1"
	kubeclientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"

	"github.com/golang/glog"
	log "github.com/sirupsen/logrus"

	"github.com/openshift/cluster-operator/pkg/ansible"
	"github.com/openshift/cluster-operator/pkg/kubernetes/pkg/util/metrics"

	clusteroperator "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
	clusteroperatorclientset "github.com/openshift/cluster-operator/pkg/client/clientset_generated/clientset"
	informers "github.com/openshift/cluster-operator/pkg/client/informers_generated/externalversions/clusteroperator/v1alpha1"
	lister "github.com/openshift/cluster-operator/pkg/client/listers_generated/clusteroperator/v1alpha1"
	"github.com/openshift/cluster-operator/pkg/controller"
)

const (
	// maxRetries is the number of times a service will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the
	// sequence of delays between successive queuings of a service.
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 15

	controllerLogName = "infra"

	infraPlaybook = "playbooks/aws/openshift-cluster/prerequisites.yml"
	// jobPrefix is used when generating a name for the configmap and job used for each
	// Ansible execution.
	jobPrefix = "job-infra-"
)

const provisionInventoryTemplate = `
[OSEv3:children]
masters
nodes
etcd

[OSEv3:vars]

[masters]

[etcd]

[nodes]
`

var clusterKind = clusteroperator.SchemeGroupVersion.WithKind("Cluster")

// NewInfraController returns a new *InfraController.
func NewInfraController(
	clusterInformer informers.ClusterInformer,
	jobInformer batchinformers.JobInformer,
	kubeClient kubeclientset.Interface,
	clusteroperatorClient clusteroperatorclientset.Interface,
	ansibleImage string,
	ansibleImagePullPolicy kapi.PullPolicy,
) *InfraController {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	if kubeClient != nil && kubeClient.CoreV1().RESTClient().GetRateLimiter() != nil {
		metrics.RegisterMetricAndTrackRateLimiterUsage("clusteroperator_cluster_controller", kubeClient.CoreV1().RESTClient().GetRateLimiter())
	}

	logger := log.WithField("controller", controllerLogName)
	c := &InfraController{
		coClient:   clusteroperatorClient,
		kubeClient: kubeClient,
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "cluster"),
		logger:     logger,
	}

	clusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addCluster,
		UpdateFunc: c.updateCluster,
	})
	c.clustersLister = clusterInformer.Lister()
	c.clustersSynced = clusterInformer.Informer().HasSynced

	jobInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addJob,
		UpdateFunc: c.updateJob,
		DeleteFunc: c.deleteJob,
	})
	c.jobsSynced = jobInformer.Informer().HasSynced
	c.jobsLister = jobInformer.Lister()

	c.syncHandler = c.syncCluster
	c.enqueueCluster = c.enqueue
	c.ansibleGenerator = ansible.NewJobGenerator(ansibleImage, ansibleImagePullPolicy)

	return c
}

// InfraController manages clusters.
type InfraController struct {
	coClient   clusteroperatorclientset.Interface
	kubeClient kubeclientset.Interface

	// To allow injection of syncCluster for testing.
	syncHandler func(hKey string) error

	// To allow injection of mock ansible generator for testing:
	ansibleGenerator ansible.JobGenerator

	// used for unit testing
	enqueueCluster func(cluster *clusteroperator.Cluster)

	// clustersLister is able to list/get clusters and is populated by the shared informer passed to
	// NewClusterController.
	clustersLister lister.ClusterLister
	// clustersSynced returns true if the cluster shared informer has been synced at least once.
	// Added as a member to the struct to allow injection for testing.
	clustersSynced cache.InformerSynced

	// jobsLister is able to list/get jobs and is populated by the shared informer passed to
	// NewClusterController.
	jobsLister batchlisters.JobLister
	// jobsSynced returns true of the job shared informer has been synced at least once.
	jobsSynced cache.InformerSynced

	// Clusters that need to be synced
	queue workqueue.RateLimitingInterface

	logger *log.Entry
}

func (c *InfraController) addCluster(obj interface{}) {
	cluster := obj.(*clusteroperator.Cluster)
	c.logger.Debugf("enqueueing added cluster %s/%s", cluster.Namespace, cluster.Name)
	c.enqueueCluster(cluster)
}

func (c *InfraController) updateCluster(old, obj interface{}) {
	cluster := obj.(*clusteroperator.Cluster)
	c.logger.Debugf("enqueueing updated cluster %s/%s", cluster.Namespace, cluster.Name)
	c.enqueueCluster(cluster)
}

func (c *InfraController) isInfraJob(job *v1batch.Job) bool {
	controllerRef := metav1.GetControllerOf(job)
	if controllerRef == nil {
		return false
	}
	if controllerRef.Kind != clusterKind.Kind {
		return false
	}
	return strings.HasPrefix(job.Name, jobPrefix)
}

func (c *InfraController) clusterForJob(job *v1batch.Job) (*clusteroperator.Cluster, error) {
	controllerRef := metav1.GetControllerOf(job)
	if controllerRef.Kind != clusterKind.Kind {
		return nil, nil
	}
	cluster, err := c.clustersLister.Clusters(job.Namespace).Get(controllerRef.Name)
	if err != nil {
		return nil, err
	}
	if cluster.UID != controllerRef.UID {
		// The controller we found with this Name is not the same one that the
		// ControllerRef points to.
		return nil, nil
	}
	return cluster, nil
}

func (c *InfraController) addJob(obj interface{}) {
	job := obj.(*v1batch.Job)
	if c.isInfraJob(job) {
		cluster, err := c.clusterForJob(job)
		if err != nil {
			c.logger.Errorf("Cannot retrieve cluster for job %s/%s: %v", job.Namespace, job.Name, err)
			utilruntime.HandleError(err)
			return
		}
		c.logger.Debugf("enqueueing cluster %s/%s for created infra job %s/%s", cluster.Namespace, cluster.Name, job.Namespace, job.Name)
		c.enqueueCluster(cluster)
	}
}

func (c *InfraController) updateJob(old, obj interface{}) {
	job := obj.(*v1batch.Job)
	if c.isInfraJob(job) {
		cluster, err := c.clusterForJob(job)
		if err != nil {
			c.logger.Errorf("Cannot retrieve cluster for job %s/%s: %v", job.Namespace, job.Name, err)
			utilruntime.HandleError(err)
			return
		}
		c.logger.Debugf("enqueueing cluster %s/%s for updated infra job %s/%s", cluster.Namespace, cluster.Name, job.Namespace, job.Name)
		c.enqueueCluster(cluster)
	}
}

func (c *InfraController) deleteJob(obj interface{}) {
	job, ok := obj.(*v1batch.Job)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get object from tombstone %#v", obj))
			return
		}
		job, ok = tombstone.Obj.(*v1batch.Job)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("tombstone contained object that is not a Job %#v", obj))
			return
		}
	}
	if !c.isInfraJob(job) {
		return
	}
	cluster, err := c.clusterForJob(job)
	if err != nil || cluster == nil {
		utilruntime.HandleError(fmt.Errorf("could not get cluster for deleted job %s/%s", job.Namespace, job.Name))
	}
	c.enqueueCluster(cluster)
}

// Runs c; will not return until stopCh is closed. workers determines how many
// clusters will be handled in parallel.
func (c *InfraController) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	c.logger.Infof("starting infra controller")
	defer c.logger.Infof("shutting down infra controller")

	if !controller.WaitForCacheSync("infra", stopCh, c.clustersSynced, c.jobsSynced) {
		c.logger.Errorf("Could not sync caches for infra controller")
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (c *InfraController) enqueue(cluster *clusteroperator.Cluster) {
	key, err := controller.KeyFunc(cluster)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", cluster, err))
		return
	}

	c.queue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (c *InfraController) worker() {
	for c.processNextWorkItem() {
	}
}

func (c *InfraController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.syncHandler(key.(string))
	c.handleErr(err, key)

	return true
}

func (c *InfraController) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}

	logger := c.logger.WithField("cluster", key)

	logger.Errorf("error syncing cluster: %v", err)
	if c.queue.NumRequeues(key) < maxRetries {
		logger.Errorf("retrying cluster")
		c.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	logger.Infof("dropping cluster out of the queue: %v", err)
	c.queue.Forget(key)
}

func (c *InfraController) syncClusterStatusWithJob(cluster *clusteroperator.Cluster, job *v1batch.Job) error {
	cluster = cluster.DeepCopy()
	now := metav1.Now()
	jobCompleted := jobCondition(job, v1batch.JobComplete)
	jobFailed := jobCondition(job, v1batch.JobFailed)
	switch {
	case jobCompleted != nil && jobCompleted.Status == kapi.ConditionTrue:
		clusterProvisioning := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioning)
		if clusterProvisioning != nil &&
			clusterProvisioning.Status == kapi.ConditionTrue {
			clusterProvisioning.Status = kapi.ConditionFalse
			clusterProvisioning.LastTransitionTime = now
			clusterProvisioning.LastProbeTime = now
			clusterProvisioning.Reason = "JobCompleted"
			clusterProvisioning.Message = fmt.Sprintf("Job %s/%s completed at %v", job.Namespace, job.Name, jobCompleted.LastTransitionTime)
		}
		clusterProvisioned := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioned)
		if clusterProvisioned != nil &&
			clusterProvisioned.Status == kapi.ConditionFalse {
			clusterProvisioned.Status = kapi.ConditionTrue
			clusterProvisioned.LastTransitionTime = now
			clusterProvisioned.LastProbeTime = now
			clusterProvisioned.Reason = "JobCompleted"
			clusterProvisioning.Message = fmt.Sprintf("Job %s/%s completed at %v", job.Namespace, job.Name, jobCompleted.LastTransitionTime)
		}
		if clusterProvisioned == nil {
			cluster.Status.Conditions = append(cluster.Status.Conditions, clusteroperator.ClusterCondition{
				Type:               clusteroperator.ClusterHardwareProvisioned,
				Status:             kapi.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Reason:             "JobCompleted",
				Message:            fmt.Sprintf("Job %s/%s completed at %v", job.Namespace, job.Name, jobCompleted.LastTransitionTime),
			})
		}
		provisioningFailed := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioningFailed)
		if provisioningFailed != nil &&
			provisioningFailed.Status == kapi.ConditionTrue {
			provisioningFailed.Status = kapi.ConditionFalse
			provisioningFailed.LastTransitionTime = now
			provisioningFailed.LastProbeTime = now
			provisioningFailed.Reason = ""
			provisioningFailed.Message = ""
		}
		cluster.Status.Provisioned = true
		cluster.Status.ProvisioningJob = nil
	case jobFailed != nil && jobFailed.Status == kapi.ConditionTrue:
		clusterProvisioning := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioning)
		if clusterProvisioning != nil &&
			clusterProvisioning.Status == kapi.ConditionTrue {
			clusterProvisioning.Status = kapi.ConditionFalse
			clusterProvisioning.LastTransitionTime = now
			clusterProvisioning.LastProbeTime = now
			clusterProvisioning.Reason = "JobFailed"
			clusterProvisioning.Message = fmt.Sprintf("Job %s/%s failed at %v, reason: %s", job.Namespace, job.Name, jobFailed.LastTransitionTime, jobFailed.Reason)
		}
		cluster.Status.ProvisioningJob = nil
		provisioningFailed := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioningFailed)
		if provisioningFailed != nil {
			provisioningFailed.Status = kapi.ConditionTrue
			provisioningFailed.LastTransitionTime = now
			provisioningFailed.LastProbeTime = now
			provisioningFailed.Reason = "JobFailed"
			provisioningFailed.Message = fmt.Sprintf("Job %s/%s failed at %v, reason: %s", job.Namespace, job.Name, jobFailed.LastTransitionTime, jobFailed.Reason)
		} else {
			cluster.Status.Conditions = append(cluster.Status.Conditions, clusteroperator.ClusterCondition{
				Type:               clusteroperator.ClusterHardwareProvisioningFailed,
				Status:             kapi.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Reason:             "JobFailed",
				Message:            fmt.Sprintf("Job %s/%s failed at %v, reason: %s", job.Namespace, job.Name, jobFailed.LastTransitionTime, jobFailed.Reason),
			})
		}
	default:
		clusterProvisioning := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioning)
		if clusterProvisioning != nil {
			if clusterProvisioning.Status != kapi.ConditionTrue {
				clusterProvisioning.Status = kapi.ConditionTrue
				clusterProvisioning.LastTransitionTime = now
				clusterProvisioning.LastProbeTime = now
				clusterProvisioning.Reason = "JobRunning"
				clusterProvisioning.Message = fmt.Sprintf("Job %s/%s is running since %v. Pod completions: %d, failures: %d", job.Namespace, job.Name, job.Status.StartTime, job.Status.Succeeded, job.Status.Failed)
			}
		} else {
			cluster.Status.Conditions = append(cluster.Status.Conditions, clusteroperator.ClusterCondition{
				Type:               clusteroperator.ClusterHardwareProvisioning,
				Status:             kapi.ConditionTrue,
				LastProbeTime:      now,
				LastTransitionTime: now,
				Reason:             "JobRunning",
				Message:            fmt.Sprintf("Job %s/%s is running since %v. Pod completions: %d, failures: %d", job.Namespace, job.Name, job.Status.StartTime, job.Status.Succeeded, job.Status.Failed),
			})
		}
	}

	return c.updateClusterStatus(cluster)
}

func (c *InfraController) updateClusterStatus(cluster *clusteroperator.Cluster) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		oldCluster, err := c.clustersLister.Clusters(cluster.Namespace).Get(cluster.Name)
		if err != nil {
			return err
		}
		return controller.PatchClusterStatus(c.coClient, oldCluster, cluster)
	})
}

// syncCluster will sync the cluster with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (c *InfraController) syncCluster(key string) error {
	startTime := time.Now()
	cLog := c.logger.WithField("cluster", key)
	cLog.Debugln("started syncing cluster")
	defer func() {
		cLog.WithField("duration", time.Since(startTime)).Debugln("finished syncing cluster")
	}()

	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	if len(ns) == 0 || len(name) == 0 {
		return fmt.Errorf("invalid cluster key %q: either namespace or name is missing", key)
	}

	cluster, err := c.clustersLister.Clusters(ns).Get(name)
	if errors.IsNotFound(err) {
		cLog.Warnln("cluster not found")
		return nil
	}
	if err != nil {
		return err
	}

	// Determine whether provisioning is needed
	provisioning := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioning)
	if provisioning != nil && provisioning.Status == kapi.ConditionTrue {
		return c.syncProvisioningJob(cluster)
	}
	provisioned := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioned)
	if provisioned != nil && provisioned.Status == kapi.ConditionTrue {
		return nil
	}

	// Do not attempt to provision again if we've already attempted provisioning
	// for the current cluster generation
	if cluster.Status.ProvisioningJobGeneration == cluster.Generation {
		return nil
	}

	return c.provisionCluster(cluster)
}

func (c *InfraController) syncProvisioningJob(cluster *clusteroperator.Cluster) error {
	var job *v1batch.Job
	if cluster.Status.ProvisioningJob == nil {
		return c.setJobNotFoundStatus(cluster)
	}

	job, err := c.jobsLister.Jobs(cluster.Namespace).Get(cluster.Status.ProvisioningJob.Name)
	if err != nil && errors.IsNotFound(err) {
		return c.setJobNotFoundStatus(cluster)
	}
	if err != nil {
		return err
	}
	return c.syncClusterStatusWithJob(cluster, job)
}

func jobCondition(job *v1batch.Job, conditionType v1batch.JobConditionType) *v1batch.JobCondition {
	for i, condition := range job.Status.Conditions {
		if condition.Type == conditionType {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

func clusterCondition(cluster *clusteroperator.Cluster, conditionType clusteroperator.ClusterConditionType) *clusteroperator.ClusterCondition {
	for i, condition := range cluster.Status.Conditions {
		if condition.Type == conditionType {
			return &cluster.Status.Conditions[i]
		}
	}
	return nil
}

func (c *InfraController) setJobNotFoundStatus(cluster *clusteroperator.Cluster) error {
	cluster = cluster.DeepCopy()
	jobName := ""
	now := metav1.Now()
	if cluster.Status.ProvisioningJob != nil {
		jobName = cluster.Status.ProvisioningJob.Name
	}
	reason := "JobMissing"
	message := "Provisioning job not found."
	if len(jobName) > 0 {
		message = fmt.Sprintf("Provisioning job %s/%s is not found.", cluster.Namespace, jobName)
	}
	cluster.Status.ProvisioningJob = nil
	if provisioning := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioning); provisioning != nil {
		provisioning.Status = kapi.ConditionFalse
		provisioning.Reason = reason
		provisioning.Message = message
		provisioning.LastTransitionTime = now
		provisioning.LastProbeTime = now
	}
	provisioningFailed := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioningFailed)
	if provisioningFailed != nil {
		provisioningFailed.Status = kapi.ConditionTrue
		provisioningFailed.Reason = reason
		provisioningFailed.Message = message
		provisioningFailed.LastTransitionTime = now
		provisioningFailed.LastProbeTime = now
	} else {
		cluster.Status.Conditions = append(cluster.Status.Conditions, clusteroperator.ClusterCondition{
			Type:               clusteroperator.ClusterHardwareProvisioningFailed,
			Status:             kapi.ConditionTrue,
			Reason:             reason,
			Message:            message,
			LastTransitionTime: now,
			LastProbeTime:      now,
		})
	}
	return c.updateClusterStatus(cluster)
}

func (c *InfraController) setProvisioningStartedStatus(cluster *clusteroperator.Cluster, job *v1batch.Job) error {
	now := metav1.Now()
	cluster = cluster.DeepCopy()
	cluster.Status.ProvisioningJob = &kapi.LocalObjectReference{
		Name: job.Name,
	}
	cluster.Status.ProvisioningJobGeneration = cluster.Generation
	condition := clusteroperator.ClusterCondition{
		Type:               clusteroperator.ClusterHardwareProvisioning,
		Status:             kapi.ConditionTrue,
		LastProbeTime:      now,
		LastTransitionTime: now,
		Reason:             "JobCreated",
		Message:            fmt.Sprintf("Job %s/%s created for infra provisioning", cluster.Namespace, job.Name),
	}
	existingCondition := clusterCondition(cluster, clusteroperator.ClusterHardwareProvisioning)
	if existingCondition != nil {
		*existingCondition = condition
	} else {
		cluster.Status.Conditions = append(cluster.Status.Conditions, condition)
	}
	return c.updateClusterStatus(cluster)
}

func (c *InfraController) provisionCluster(cluster *clusteroperator.Cluster) error {
	clusterLogger := c.logger.WithField("cluster", cluster.Name)
	clusterLogger.Infoln("provisioning cluster infrastructure")
	varsGenerator := ansible.NewVarsGenerator(cluster)
	vars, err := varsGenerator.GenerateVars()
	if err != nil {
		return err
	}

	job, cfgMap := c.ansibleGenerator.GeneratePlaybookJob(cluster, jobPrefix, infraPlaybook, provisionInventoryTemplate, vars)

	_, err = c.kubeClient.CoreV1().ConfigMaps(cluster.Namespace).Create(cfgMap)
	if err != nil {
		return err
	}

	_, err = c.kubeClient.BatchV1().Jobs(cluster.Namespace).Create(job)
	if err != nil {
		return err
	}

	return c.setProvisioningStartedStatus(cluster, job)
}
