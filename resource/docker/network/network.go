// Copyright © 2016 Asteris, LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build !solaris

package network

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/asteris-llc/converge/helpers/transform"
	"github.com/asteris-llc/converge/resource"
	"github.com/asteris-llc/converge/resource/docker"
	dc "github.com/fsouza/go-dockerclient"
	"golang.org/x/net/context"
)

// State type for Network
type State string

const (
	// StatePresent indicates the network should be present
	StatePresent State = "present"

	// StateAbsent indicates the network should be absent
	StateAbsent State = "absent"

	// DefaultDriver is the default network driver
	DefaultDriver = "bridge"

	// DefaultIPAMDriver is the default IPAM driver
	DefaultIPAMDriver = "default"
)

// Network is responsible for managing docker networks
type Network struct {
	client docker.NetworkClient

	// name of the network
	Name string `export:"name"`

	// network drive configured in the hcl
	Driver string `export:"driver"`

	// labels set on the network
	Labels map[string]string `export:"labels"`

	// driver-specific options that have been configured
	Options map[string]interface{} `export:"options"`

	// docker client IPAM options
	IPAM dc.IPAMOptions `export:"ipam"`

	// restricted to internal networking
	Internal bool `export:"internal"`

	// uses ipv6
	IPv6 bool `export:"ipv6"`

	// the configured state
	State State `export:"state"`

	// true if 'force' was specified in the hcl
	Force bool `export:"force"`
}

// Check system for docker network
func (n *Network) Check(context.Context, resource.Renderer) (resource.TaskStatus, error) {
	status := resource.NewStatus()
	nw, err := n.client.FindNetwork(n.Name)

	if err != nil {
		status.RaiseLevel(resource.StatusFatal)
		return status, err
	}

	status.AddDifference(n.Name, string(networkState(nw)), string(n.State), "")

	if n.State == StatePresent {
		if nw != nil && n.Force {
			n.diffNetwork(nw, status)
		}

		invalidGWs, err := n.findConflicting(nw, status)
		if err != nil {
			status.RaiseLevel(resource.StatusFatal)
			return status, err
		}

		if len(invalidGWs) > 0 {
			status.RaiseLevel(resource.StatusCantChange)
			return status, fmt.Errorf("%s: gateway(s) are already in use", strings.Join(invalidGWs, ", "))

		}
	}

	status.RaiseLevelForDiffs()

	return status, nil
}

// Apply ensures the network matches the desired state
func (n *Network) Apply(context.Context) (resource.TaskStatus, error) {
	status := resource.NewStatus()

	var (
		nw  *dc.Network
		err error
	)

	nw, err = n.client.FindNetwork(n.Name)
	if err != nil {
		status.RaiseLevel(resource.StatusFatal)
		return status, err
	}

	if n.State == StatePresent {
		if nw != nil {
			if !n.Force {
				return status, nil
			}

			err = n.client.RemoveNetwork(n.Name)
			if err != nil {
				status.RaiseLevel(resource.StatusFatal)
				return status, err
			}
			status.AddMessage(fmt.Sprintf("removed network %s", n.Name))
		}

		opts := dc.CreateNetworkOptions{
			Name:       n.Name,
			Driver:     n.Driver,
			Labels:     n.Labels,
			Options:    n.Options,
			IPAM:       n.IPAM,
			Internal:   n.Internal,
			EnableIPv6: n.IPv6,
		}

		nw, err = n.client.CreateNetwork(opts)
		if err != nil {
			status.RaiseLevel(resource.StatusFatal)
			return status, err
		}
		status.AddMessage(fmt.Sprintf("created network %s", n.Name))
		status.RaiseLevel(resource.StatusWillChange)
	} else {
		if nw != nil {
			err = n.client.RemoveNetwork(n.Name)
			if err != nil {
				status.RaiseLevel(resource.StatusFatal)
				return status, err
			}
			status.AddMessage(fmt.Sprintf("removed network %s", n.Name))
			status.RaiseLevel(resource.StatusWillChange)
		}
	}

	status.AddDifference(n.Name, string(networkState(nw)), string(n.State), "")
	return status, nil
}

// SetClient injects a docker api client
func (n *Network) SetClient(client docker.NetworkClient) {
	n.client = client
}

func (n *Network) diffNetwork(nw *dc.Network, status *resource.Status) {
	status.AddDifference("labels", mapCompareStr(nw.Labels), mapCompareStr(n.Labels), "")
	status.AddDifference("driver", nw.Driver, n.Driver, DefaultDriver)
	status.AddDifference("options", mapCompareStr(nw.Options), mapCompareStr(toStrMap(n.Options)), "")
	status.AddDifference("internal", strconv.FormatBool(nw.Internal), strconv.FormatBool(n.Internal), "false")
	status.AddDifference("ipv6", strconv.FormatBool(nw.EnableIPv6), strconv.FormatBool(n.IPv6), "false")
	status.AddDifference("ipam_driver", nw.IPAM.Driver, n.IPAM.Driver, DefaultIPAMDriver)

	// we cannot reliably detect a diff of the ipam config if the desired ipam
	// config is the default ([]) but the actual ipam config has a single
	// customized entry
	if len(n.IPAM.Config) > 0 || len(nw.IPAM.Config) > 1 {
		actualIPAMConfigs := IPAMConfigs(nw.IPAM.Config)
		sort.Sort(actualIPAMConfigs)
		expectedIPAMConfigs := IPAMConfigs(n.IPAM.Config)
		sort.Sort(expectedIPAMConfigs)
		status.AddDifference("ipam_config", actualIPAMConfigs.String(), expectedIPAMConfigs.String(), "")
	}
}

// findConflicting can validate whether the network can be created on the docker
// host. use sparingly as behavior can vary across different docker network
// plugins. in most cases, we can rely on the docker api to return errors during
// apply.  It returns a list of networks that would conflict with the network
// and prevent it from being created.
func (n *Network) findConflicting(nw *dc.Network, status *resource.Status) ([]string, error) {
	if len(n.IPAM.Config) > 0 {
		inUse, err := n.gatewaysInUse(nw, status)
		return inUse, err
	}
	return nil, nil
}

func (n *Network) gatewaysInUse(nw *dc.Network, status *resource.Status) ([]string, error) {
	var gateways []string
	var usedGWs []string
	networks, err := n.client.ListNetworks()
	if err != nil {
		return nil, err
	}

	for _, network := range networks {
		if nw == nil || network.ID != nw.ID {
			for _, ipamConfig := range network.IPAM.Config {
				if ipamConfig.Gateway != "" {
					gateways = append(gateways, ipamConfig.Gateway)
				}
			}
		}
	}

	for _, ipamConfig := range n.IPAM.Config {
		for _, gateway := range gateways {
			if strings.EqualFold(ipamConfig.Gateway, gateway) {
				usedGWs = append(usedGWs, gateway)
				status.AddMessage(fmt.Sprintf("gateway %s already in use", gateway))
			}
		}
	}

	return usedGWs, nil
}

func networkState(nw *dc.Network) State {
	if nw != nil {
		return StatePresent
	}
	return StateAbsent
}

func toStrMap(m map[string]interface{}) map[string]string {
	strmap := make(map[string]string)
	for k, v := range m {
		strmap[k] = v.(string)
	}
	return strmap
}

func mapCompareStr(m map[string]string) string {
	pairs := transform.StringsMapToStringSlice(
		m,
		func(k, v string) string {
			return fmt.Sprintf("%s=%s", k, v)
		},
	)
	sort.Strings(pairs)
	return strings.Join(pairs, ", ")
}

// IPAMConfigs is a slice of dc.IPAMConfig
type IPAMConfigs []dc.IPAMConfig

// Len implements the sort interface for IPAMConfigs
func (ic IPAMConfigs) Len() int { return len(ic) }

// Swap implements the sort interface for IPAMConfigs
func (ic IPAMConfigs) Swap(i, j int) { ic[i], ic[j] = ic[j], ic[i] }

// Less implements the sort interface for IPAMConfigs
func (ic IPAMConfigs) Less(i, j int) bool { return ic[i].Subnet < ic[j].Subnet }

// IPAMConfigString returns a string representation of the IPAMConfigs slice
func (ic IPAMConfigs) String() string {
	var configStrs []string
	for _, c := range ic {
		configStrs = append(configStrs, ipamConfigString(c))
	}
	return strings.Join(configStrs, "\n")
}

func ipamConfigString(c dc.IPAMConfig) string {
	var parts []string

	if c.Subnet != "" {
		parts = append(parts, fmt.Sprintf("subnet: %s", c.Subnet))
	}

	if c.Gateway != "" {
		parts = append(parts, fmt.Sprintf("gateway: %s", c.Gateway))
	}

	if c.IPRange != "" {
		parts = append(parts, fmt.Sprintf("ip_range: %s", c.IPRange))
	}

	if len(c.AuxAddress) > 0 {
		parts = append(parts, fmt.Sprintf("aux_addresses: [%s]", mapCompareStr(c.AuxAddress)))
	}

	return strings.Join(parts, ", ")
}
