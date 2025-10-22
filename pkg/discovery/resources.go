/*
Copyright 2025 The Kubernetes Authors.

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

// Package discovery implements the discovery of network devices
// and publishing them via the PublishResourcesFunc.
package discovery

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/jaypipes/ghw"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cni-dra-driver/apis/v1alpha1"
)

// PublishResources is a function type to advertise resources.
type PublishResources func(context.Context, resourceslice.DriverResources) error

// Create a hash of the device name to avoid issues with underscores
// and other special characters in Kubernetes resource names.
func hashDeviceName(originalName string) string {
	hash := sha256.Sum256([]byte(originalName))
	return fmt.Sprintf("device-%x", hash[:8]) // Use first 8 bytes of hash for shorter names
}

// Resources periodically discovers network devices
// and publishes them via the PublishResourcesFunc.
type Resources struct {
	PublishResourcesFunc PublishResources
	Interval             time.Duration
	NodeName             string
}

// Run starts the periodic discovery of network devices
// and publishing them via the PublishResourcesFunc.
func (r *Resources) Run(ctx context.Context) {
	for {
		devices, err := r.ListDevices()
		if err != nil {
			klog.FromContext(ctx).Error(err, "failed to list devices")
			continue
		}

		klog.FromContext(ctx).Info("devices to publish", "devices", devices)

		driverResources := resourceslice.DriverResources{
			Pools: map[string]resourceslice.Pool{
				r.NodeName: {
					Slices: []resourceslice.Slice{
						{Devices: devices},
					},
				},
			},
		}

		err = r.PublishResourcesFunc(ctx, driverResources)
		if err != nil {
			klog.FromContext(ctx).Error(err, "failed to publish resources")
			continue
		}

		select {
		case <-time.After(r.Interval):
		case <-ctx.Done():
			return
		}
	}
}

// ListDevices lists the network devices available on the node using ghw library.
// It returns a slice of resourcev1.Device representing the network devices.
// The device attributes include the interface name, MAC address, and PCI address (if available).
func (r *Resources) ListDevices() ([]resourcev1.Device, error) {
	devices := []resourcev1.Device{}

	netInfo, err := ghw.Network()
	if err != nil {
		return nil, fmt.Errorf("failed to get network info: %v", err)
	}

	allowMultipleAllocations := true

	one := resource.MustParse("1")
	maxVirtualDevices := resource.MustParse("65535")

	for _, nic := range netInfo.NICs {
		hashedDeviceName := hashDeviceName(nic.Name)

		device := resourcev1.Device{
			Name: hashedDeviceName,
			Attributes: map[resourcev1.QualifiedName]resourcev1.DeviceAttribute{
				v1alpha1.InterfaceName: {StringValue: &nic.Name},
			},
			Capacity: map[resourcev1.QualifiedName]resourcev1.DeviceCapacity{
				v1alpha1.MaxVirtualDevices: {
					Value: maxVirtualDevices,
					RequestPolicy: &resourcev1.CapacityRequestPolicy{
						Default:     &one,
						ValidValues: []resource.Quantity{one},
					},
				},
			},
			NodeName:                 &r.NodeName,
			AllowMultipleAllocations: &allowMultipleAllocations,
		}

		if nic.MACAddress != "" {
			device.Attributes[v1alpha1.MacAddress] = resourcev1.DeviceAttribute{StringValue: &nic.MACAddress}
		}

		if nic.PCIAddress != nil && *nic.PCIAddress != "" {
			device.Attributes[deviceattribute.StandardDeviceAttributePCIeRoot] = resourcev1.DeviceAttribute{StringValue: nic.PCIAddress}
		}

		devices = append(devices, device)
	}

	return devices, nil
}
