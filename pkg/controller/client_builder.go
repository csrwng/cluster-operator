/*
Copyright 2016 The Kubernetes Authors.

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
	kubeclientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"

	coclientset "github.com/openshift/cluster-operator/pkg/client/clientset_generated/clientset"
	clusterclientset "sigs.k8s.io/cluster-api/pkg/client/clientset_generated/clientset"

	"github.com/golang/glog"
)

// ClientBuilder allows you to get clients and configs for controllers
type ClientBuilder interface {
	// Config returns a new restclient.Config with the given user agent name.
	Config(name string) (*restclient.Config, error)
	// ConfigOrDie return a new restclient.Config with the given user agent
	// name, or logs a fatal error.
	ConfigOrDie(name string) *restclient.Config
	// Client returns a new coclientset.Interface with the given user agent
	// name.
	Client(name string) (coclientset.Interface, error)
	// ClusterAPIClient returns a new cluster API clientset.Interface
	// with the given user agent name.
	ClusterAPIClient(name string) (clusterclientset.Interface, error)
	// ClientOrDie returns a new coclientset.Interface with the given user agent
	// name or logs a fatal error.
	ClientOrDie(name string) coclientset.Interface
	// KubeClientOrDie returns a new kubeclientset.Interface with the given
	// user agent name or logs a fatal error.
	KubeClientOrDie(name string) kubeclientset.Interface
	// ClusterAPIClientOrDie returns a new cluster API clientset.Interface
	// with the given user agent name, or logs a fatal error.
	ClusterAPIClientOrDie(name string) clusterclientset.Interface
}

// SimpleClientBuilder returns a fixed client with different user agents
type SimpleClientBuilder struct {
	// ClientConfig is a skeleton config to clone and use as the basis for each controller client
	ClientConfig *restclient.Config
}

// Config returns a new restclient.Config with the given user agent name.
func (b SimpleClientBuilder) Config(name string) (*restclient.Config, error) {
	clientConfig := *b.ClientConfig
	return restclient.AddUserAgent(&clientConfig, name), nil
}

// ConfigOrDie return a new restclient.Config with the given user agent
// name, or logs a fatal error.
func (b SimpleClientBuilder) ConfigOrDie(name string) *restclient.Config {
	clientConfig, err := b.Config(name)
	if err != nil {
		glog.Fatal(err)
	}
	return clientConfig
}

// Client returns a new clientset.Interface with the given user agent
// name.
func (b SimpleClientBuilder) Client(name string) (coclientset.Interface, error) {
	clientConfig, err := b.Config(name)
	if err != nil {
		return nil, err
	}
	return coclientset.NewForConfig(clientConfig)
}

// ClientOrDie returns a new clientset.Interface with the given user agent
// name or logs a fatal error.
func (b SimpleClientBuilder) ClientOrDie(name string) coclientset.Interface {
	client, err := b.Client(name)
	if err != nil {
		glog.Fatal(err)
	}
	return client
}

// KubeClientOrDie returns a new kubeclientset.Interface with the given
// user agent name or logs a fatal error.
func (b SimpleClientBuilder) KubeClientOrDie(name string) kubeclientset.Interface {
	clientConfigCopy := *b.ClientConfig
	clientConfig := restclient.AddUserAgent(&clientConfigCopy, name)
	kubeClient, err := kubeclientset.NewForConfig(clientConfig)
	if err != nil {
		glog.Fatal(err)
	}
	return kubeClient
}

// ClusterAPIClient returns a new kubeclientset.Interface with the given
// user agent name.
func (b SimpleClientBuilder) ClusterAPIClient(name string) (clusterclientset.Interface, error) {
	clientConfigCopy := *b.ClientConfig
	clientConfig := restclient.AddUserAgent(&clientConfigCopy, name)
	return clusterclientset.NewForConfig(clientConfig)
}

// ClusterAPIClientOrDie returns a new kubeclientset.Interface with the given
// user agent name or logs a fatal error.
func (b SimpleClientBuilder) ClusterAPIClientOrDie(name string) clusterclientset.Interface {
	client, err := b.ClusterAPIClient(name)
	if err != nil {
		glog.Fatal(err)
	}
	return client
}
