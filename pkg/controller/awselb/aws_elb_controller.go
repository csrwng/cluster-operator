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

package awselb

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	kubeclientset "k8s.io/client-go/kubernetes"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	"github.com/golang/glog"
	log "github.com/sirupsen/logrus"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elb"

	capiv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	capiclient "sigs.k8s.io/cluster-api/pkg/client/clientset_generated/clientset"
	informers "sigs.k8s.io/cluster-api/pkg/client/informers_generated/externalversions/cluster/v1alpha1"
	lister "sigs.k8s.io/cluster-api/pkg/client/listers_generated/cluster/v1alpha1"

	cov1 "github.com/openshift/cluster-operator/pkg/apis/clusteroperator/v1alpha1"
	coclientset "github.com/openshift/cluster-operator/pkg/client/clientset_generated/clientset"
	coaws "github.com/openshift/cluster-operator/pkg/clusterapi/aws"
	cocontroller "github.com/openshift/cluster-operator/pkg/controller"
	cometrics "github.com/openshift/cluster-operator/pkg/kubernetes/pkg/util/metrics"
	cologging "github.com/openshift/cluster-operator/pkg/logging"
)

const (
	// maxRetries is the number of times a service will be retried before it is dropped out of the queue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the
	// sequence of delays between successive queuings of a service.
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries     = 15
	controllerName = "awselb"

	// elbResyncDuration represents period of time after which we will re-add machines to the ELBs.
	// Once successfully added we add a timestamp to status, each time the machine is synced we check if
	// this duration is exceeded and if so, re-add to the ELB to ensure we repair any machines
	// accidentally taken out of rotation.
	elbResyncDuration = 2 * time.Hour

	// ExtELBRegistrationSucceeded indicates success for external ELB registration
	ExtELBRegistrationSucceeded = "ExtELBRegistrationSucceeded"

	// ExtELBRegistrationFailed indicates that external ELB registration has failed
	ExtELBRegistrationFailed = "ExtELBRegistrationFailed"

	// IntELBRegistrationSucceeded indicates success for internal ELB registration
	IntELBRegistrationSucceeded = "IntELBRegistrationSucceeded"

	// IntELBRegistrationFailed indicates that internal ELB registration has failed
	IntELBRegistrationFailed = "IntELBRegistrationFailed"
)

// NewController returns a new *Controller.
func NewController(machineInformer informers.MachineInformer, kubeClient kubeclientset.Interface, clustopClient coclientset.Interface, capiClient capiclient.Interface) *Controller {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	// TODO: remove the wrapper when every clients have moved to use the clientset.
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: v1core.New(kubeClient.CoreV1().RESTClient()).Events("")})

	if kubeClient != nil && kubeClient.CoreV1().RESTClient().GetRateLimiter() != nil {
		cometrics.RegisterMetricAndTrackRateLimiterUsage("clusteroperator_awselb_controller", kubeClient.CoreV1().RESTClient().GetRateLimiter())
	}

	c := &Controller{
		client:        clustopClient,
		capiClient:    capiClient,
		kubeClient:    kubeClient,
		queue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "awselb"),
		clientBuilder: coaws.NewClient,
	}

	machineInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addMachine,
		UpdateFunc: c.updateMachine,
		DeleteFunc: c.deleteMachine,
	})
	c.machinesLister = machineInformer.Lister()
	c.machinesSynced = machineInformer.Informer().HasSynced

	c.syncHandler = c.syncMachine
	c.enqueueMachine = c.enqueue
	c.logger = log.WithField("controller", controllerName)

	return c
}

// Controller monitors master machines and adds to ELBs as appropriate.
type Controller struct {
	client     coclientset.Interface
	capiClient capiclient.Interface
	kubeClient kubeclientset.Interface

	// To allow injection of syncMachine for testing.
	syncHandler func(hKey string) error
	// used for unit testing
	enqueueMachine func(machine *capiv1.Machine)

	// machinesLister is able to list/get machines and is populated by the shared informer passed to
	// NewController.
	machinesLister lister.MachineLister
	// machinesSynced returns true if the machine shared informer has been synced at least once.
	// Added as a member to the struct to allow injection for testing.
	machinesSynced cache.InformerSynced

	// Machines that need to be synced
	queue workqueue.RateLimitingInterface

	logger        log.FieldLogger
	clientBuilder func(kubeClient kubernetes.Interface, secretName, namespace, region string) (coaws.Client, error)
}

func (c *Controller) addMachine(obj interface{}) {
	machine := obj.(*capiv1.Machine)

	isMaster, err := cocontroller.MachineIsMaster(machine)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't decode provider config from %#v: %v", machine, err))
		return
	}
	if !isMaster {
		cologging.WithMachine(c.logger, machine).Debug("skipping non-master machine")
		return
	}

	cologging.WithMachine(c.logger, machine).Debug("adding machine")
	c.enqueueMachine(machine)
}

func (c *Controller) updateMachine(old, cur interface{}) {
	oldMachine := old.(*capiv1.Machine)
	curMachine := cur.(*capiv1.Machine)

	isMaster, err := cocontroller.MachineIsMaster(curMachine)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't decode provider config from %#v: %v", curMachine, err))
		return
	}
	if !isMaster {
		cologging.WithMachine(c.logger, curMachine).Debug("skipping non-master machine")
		return
	}

	cologging.WithMachine(c.logger, oldMachine).Infof("updating machine")
	c.enqueueMachine(curMachine)
}

func (c *Controller) deleteMachine(obj interface{}) {

	machine, ok := obj.(*capiv1.Machine)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		machine, ok = tombstone.Obj.(*capiv1.Machine)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("Tombstone contained object that is not a Machine %#v", obj))
			return
		}
	}

	isMaster, err := cocontroller.MachineIsMaster(machine)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't decode provider config from %#v: %v", machine, err))
		return
	}

	if !isMaster {
		cologging.WithMachine(c.logger, machine).Debug("skipping non-master machine")
		return
	}

	cologging.WithMachine(c.logger, machine).Infof("deleting machine")
	c.enqueueMachine(machine)
}

// Run runs c; will not return until stopCh is closed. workers determines how
// many machines will be handled in parallel.
func (c *Controller) Run(workers int, stopCh <-chan struct{}) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	log.Infof("Starting awselb controller")
	defer log.Infof("Shutting down awselb controller")

	if !cocontroller.WaitForCacheSync("machine", stopCh, c.machinesSynced) {
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (c *Controller) enqueue(machine *capiv1.Machine) {
	key, err := cocontroller.KeyFunc(machine)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("Couldn't get key for object %#v: %v", machine, err))
		return
	}

	c.queue.Add(key)
}

// worker runs a worker thread that just dequeues items, processes them, and marks them done.
// It enforces that the syncHandler is never invoked concurrently with the same key.
func (c *Controller) worker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.syncHandler(key.(string))
	c.handleErr(err, key)

	return true
}

func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		c.queue.Forget(key)
		return
	}

	if c.queue.NumRequeues(key) < maxRetries {
		c.logger.Infof("Error syncing machine %v: %v", key, err)
		c.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	c.logger.Infof("Dropping machine %q out of the queue: %v", key, err)
	c.queue.Forget(key)
}

// syncMachine will sync the machine with the given key.
// This function is not meant to be invoked concurrently with the same key.
func (c *Controller) syncMachine(key string) error {
	startTime := time.Now()
	c.logger.WithField("key", key).Debug("syncing machine")
	defer func() {
		c.logger.WithFields(log.Fields{
			"key":      key,
			"duration": time.Now().Sub(startTime),
		}).Debug("finished syncing machine")
	}()

	ns, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}

	machine, err := c.machinesLister.Machines(ns).Get(name)
	if errors.IsNotFound(err) {
		c.logger.WithField("key", key).Info("machine has been deleted")
		return nil
	}
	if err != nil {
		return err
	}

	return c.processMachine(machine)
}

func (c *Controller) processMachine(machine *capiv1.Machine) error {
	mLog := cologging.WithMachine(c.logger, machine)

	clusterID, ok := machine.Labels[cov1.ClusterNameLabel]
	if !ok {
		return fmt.Errorf("unable to lookup cluster for machine: %s", machine.Name)
	}

	coMachineSetSpec, err := cocontroller.MachineSetSpecFromClusterAPIMachineSpec(&machine.Spec)
	if err != nil {
		return err
	}

	region := coMachineSetSpec.ClusterHardware.AWS.Region
	mLog.Debugf("Obtaining AWS clients for region %q", region)

	status, err := cocontroller.AWSMachineProviderStatusFromClusterAPIMachine(machine)
	if err != nil {
		return err
	}

	mLog.WithFields(log.Fields{
		"currentGen":    machine.Generation,
		"lastSyncedGen": status.LastELBSyncGeneration,
	}).Debugf("checking if re-sync is required")
	if status.LastELBSync != nil && status.LastELBSyncGeneration == machine.Generation {
		delta := time.Now().Sub(status.LastELBSync.Time)
		if delta < elbResyncDuration {
			mLog.WithFields(log.Fields{
				"delta":         delta,
				"currentGen":    machine.Generation,
				"lastSyncedGen": status.LastELBSyncGeneration,
			}).Debugf("no resync needed")
			return nil
		}
	}

	secretName, err := cocontroller.GetSecretNameFromMachineSetSpec(coMachineSetSpec)
	if err != nil {
		return err
	}

	client, err := c.clientBuilder(c.kubeClient, secretName, machine.Namespace, region)
	if err != nil {
		return err
	}

	instance, err := coaws.GetInstance(machine, client)
	if err != nil {
		return err
	}
	mLog = mLog.WithField("instanceID", *instance.InstanceId)

	err = c.addInstanceToELB(instance, cocontroller.ELBMasterExternalName(clusterID), client, mLog)
	if err != nil {
		updateConditionError := c.updateMachineConditions(machine, mLog, cov1.ExtELBRegistration, ExtELBRegistrationFailed, err.Error())
		if updateConditionError != nil {
			mLog.Errorf("error updating machine conditions: %v", updateConditionError)
		}
		return err
	}
	err = c.addInstanceToELB(instance, cocontroller.ELBMasterInternalName(clusterID), client, mLog)
	if err != nil {
		updateConditionError := c.updateMachineConditions(machine, mLog, cov1.IntELBRegistration, IntELBRegistrationFailed, err.Error())
		if updateConditionError != nil {
			mLog.Errorf("error updating machine conditions: %v", updateConditionError)
		}
		return err
	}

	return c.updateStatus(machine, mLog)
}

func (c *Controller) updateMachineConditions(machine *capiv1.Machine, mLog log.FieldLogger, conditionType cov1.AWSMachineConditionType, reason string, message string) error {
	var (
		updateCheck cocontroller.UpdateConditionCheck
	)

	mLog.Debug("updating machine conditions")

	// Get current aws machine provider status
	awsStatus, err := cocontroller.AWSMachineProviderStatusFromClusterAPIMachine(machine)
	if err != nil {
		return err
	}

	updateCheck = cocontroller.UpdateConditionIfReasonOrMessageChange

	now := metav1.Now()
	awsStatus.LastELBSync = &now
	awsStatus.LastELBSyncGeneration = machine.Generation
	awsStatus.Conditions = cocontroller.SetAWSMachineCondition(awsStatus.Conditions, conditionType, corev1.ConditionTrue, reason, message, updateCheck)

	// Update machine with updated status
	awsStatusRaw, err := cocontroller.EncodeAWSMachineProviderStatus(awsStatus)
	if err != nil {
		mLog.Errorf("error encoding AWS provider status: %v", err)
		return err
	}

	machineCopy := machine.DeepCopy()
	machineCopy.Status.ProviderStatus = awsStatusRaw
	machineCopy.Status.LastUpdated = now
	_, err = c.capiClient.ClusterV1alpha1().Machines(machineCopy.Namespace).UpdateStatus(machineCopy)
	if err != nil {
		mLog.Errorf("error updating machine status: %v", err)
		return err
	}
	return nil
}

// updateStatus sets the LastELBSync time in status to now, and updates the machine.
func (c *Controller) updateStatus(machine *capiv1.Machine, mLog log.FieldLogger) error {
	var (
		msg         string
		updateCheck cocontroller.UpdateConditionCheck
	)

	mLog.Debug("updating machine status")

	awsStatus, err := cocontroller.AWSMachineProviderStatusFromClusterAPIMachine(machine)
	if err != nil {
		return err
	}

	msg = "successfully registered"
	updateCheck = cocontroller.UpdateConditionIfReasonOrMessageChange
	awsStatus.Conditions = cocontroller.SetAWSMachineCondition(awsStatus.Conditions, cov1.ExtELBRegistration, corev1.ConditionTrue, ExtELBRegistrationSucceeded, msg, updateCheck)
	awsStatus.Conditions = cocontroller.SetAWSMachineCondition(awsStatus.Conditions, cov1.IntELBRegistration, corev1.ConditionTrue, IntELBRegistrationSucceeded, msg, updateCheck)

	now := metav1.Now()
	awsStatus.LastELBSync = &now
	awsStatus.LastELBSyncGeneration = machine.Generation
	awsStatusRaw, err := cocontroller.EncodeAWSMachineProviderStatus(awsStatus)
	if err != nil {
		mLog.Errorf("error encoding AWS provider status: %v", err)
		return err
	}

	machineCopy := machine.DeepCopy()
	machineCopy.Status.ProviderStatus = awsStatusRaw

	machineCopy.Status.LastUpdated = now

	_, err = c.capiClient.ClusterV1alpha1().Machines(machineCopy.Namespace).UpdateStatus(machineCopy)
	if err != nil {
		mLog.Errorf("error updating machine status: %v", err)
		return err
	}
	return nil
}

func (c *Controller) addInstanceToELB(
	instance *ec2.Instance,
	elbName string,
	client coaws.Client,
	mLog log.FieldLogger) error {

	registerInput := elb.RegisterInstancesWithLoadBalancerInput{
		Instances:        []*elb.Instance{{InstanceId: instance.InstanceId}},
		LoadBalancerName: aws.String(elbName),
	}

	// This API call appears to be idempotent, so for now no need to check if the instance is
	// registered first, we can just request that it be added.
	_, err := client.RegisterInstancesWithLoadBalancer(&registerInput)
	if err != nil {
		return err
	}
	mLog.WithField("elb", elbName).Infof("instance added to ELB")
	return nil
}
