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
package validation

import (
	"encoding/json"
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cni-dra-driver/apis/v1alpha1"
)

func TestValidateResourceClaim(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		claim    *resourcev1.ResourceClaim
		expected bool
	}{
		{
			name:     "nil claim",
			claim:    nil,
			expected: false,
		},
		{
			name: "valid claim",
			claim: &resourcev1.ResourceClaim{
				Status: resourcev1.ResourceClaimStatus{
					Allocation: &resourcev1.AllocationResult{
						Devices: resourcev1.DeviceAllocationResult{
							Results: []resourcev1.DeviceRequestAllocationResult{
								{
									Driver:  "cni.dra.networking.x-k8s.io",
									Request: "macvlan-eth0",
									Device:  "net1",
								},
							},
							Config: []resourcev1.DeviceAllocationConfiguration{
								{
									Requests: []string{"macvlan-eth0"},
									DeviceConfiguration: resourcev1.DeviceConfiguration{
										Opaque: &resourcev1.OpaqueDeviceConfiguration{
											Driver: "cni.dra.networking.x-k8s.io",
											Parameters: runtime.RawExtension{
												Raw: mustMarshalJSON(&v1alpha1.CNIConfig{
													TypeMeta: metav1.TypeMeta{
														APIVersion: "cni.networking.x-k8s.io/v1alpha1",
														Kind:       "CNI",
													},
													IfName: "net1",
													Config: runtime.RawExtension{
														Raw: mustMarshalJSON(map[string]interface{}{
															"cniVersion": "1.0.0",
															"name":       "macvlan-eth0",
															"plugins": []map[string]interface{}{
																{
																	"type":   "macvlan",
																	"master": "eth0",
																	"mode":   "bridge",
																	"ipam": map[string]interface{}{
																		"type": "host-local",
																		"ranges": [][]map[string]interface{}{
																			{
																				{"subnet": "10.10.1.0/24"},
																			},
																		},
																	},
																},
															},
														}),
													},
												}),
											},
										},
									},
								},
							},
						},
					},
					ReservedFor: []resourcev1.ResourceClaimConsumerReference{
						{
							Resource: "pods",
							UID:      types.UID("test-pod-uid"),
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "no allocation",
			claim: &resourcev1.ResourceClaim{
				Status: resourcev1.ResourceClaimStatus{
					ReservedFor: []resourcev1.ResourceClaimConsumerReference{
						{
							Resource: "pods",
							UID:      types.UID("test-pod-uid"),
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "multiple reserved for",
			claim: &resourcev1.ResourceClaim{
				Status: resourcev1.ResourceClaimStatus{
					Allocation: &resourcev1.AllocationResult{},
					ReservedFor: []resourcev1.ResourceClaimConsumerReference{
						{
							Resource: "pods",
							UID:      types.UID("test-pod-uid-1"),
						},
						{
							Resource: "pods",
							UID:      types.UID("test-pod-uid-2"),
						},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.ValidateResourceClaim(tt.claim)
			if result.Valid != tt.expected {
				t.Errorf("expected valid=%v, got valid=%v, errors: %v", tt.expected, result.Valid, result.Errors)
			}
		})
	}
}

func TestValidateCNIConfig(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		config   *v1alpha1.CNIConfig
		expected bool
	}{
		{
			name: "valid macvlan config",
			config: &v1alpha1.CNIConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "cni.networking.x-k8s.io/v1alpha1",
					Kind:       "CNI",
				},
				IfName: "net1",
				Config: runtime.RawExtension{
					Raw: mustMarshalJSON(map[string]interface{}{
						"cniVersion": "1.0.0",
						"name":       "macvlan-eth0",
						"plugins": []map[string]interface{}{
							{
								"type":   "macvlan",
								"master": "eth0",
								"mode":   "bridge",
								"ipam": map[string]interface{}{
									"type": "host-local",
									"ranges": [][]map[string]interface{}{
										{
											{"subnet": "10.10.1.0/24"},
										},
									},
								},
							},
						},
					}),
				},
			},
			expected: true,
		},
		{
			name: "invalid interface name",
			config: &v1alpha1.CNIConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "cni.networking.x-k8s.io/v1alpha1",
					Kind:       "CNI",
				},
				IfName: "",
				Config: runtime.RawExtension{
					Raw: mustMarshalJSON(map[string]interface{}{
						"cniVersion": "1.0.0",
						"name":       "test",
						"plugins":    []map[string]interface{}{},
					}),
				},
			},
			expected: false,
		},
		{
			name: "invalid API version",
			config: &v1alpha1.CNIConfig{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "invalid/v1",
					Kind:       "CNI",
				},
				IfName: "net1",
				Config: runtime.RawExtension{
					Raw: mustMarshalJSON(map[string]interface{}{
						"cniVersion": "1.0.0",
						"name":       "test",
						"plugins":    []map[string]interface{}{},
					}),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parameters := runtime.RawExtension{
				Raw: mustMarshalJSON(tt.config),
			}
			result := validator.ValidateCNIConfig(parameters)
			if result.Valid != tt.expected {
				t.Errorf("expected valid=%v, got valid=%v, errors: %v", tt.expected, result.Valid, result.Errors)
			}
		})
	}
}

func TestValidateCNINetworkConfig(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		config   map[string]interface{}
		expected bool
	}{
		{
			name: "valid macvlan config",
			config: map[string]interface{}{
				"cniVersion": "1.0.0",
				"name":       "macvlan-eth0",
				"plugins": []map[string]interface{}{
					{
						"type":   "macvlan",
						"master": "eth0",
						"mode":   "bridge",
						"ipam": map[string]interface{}{
							"type": "host-local",
							"ranges": [][]map[string]interface{}{
								{
									{"subnet": "10.10.1.0/24"},
								},
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "invalid CNI version",
			config: map[string]interface{}{
				"cniVersion": "2.0.0",
				"name":       "test",
				"plugins":    []map[string]interface{}{},
			},
			expected: false,
		},
		{
			name: "missing name",
			config: map[string]interface{}{
				"cniVersion": "1.0.0",
				"plugins":    []map[string]interface{}{},
			},
			expected: false,
		},
		{
			name: "empty plugins",
			config: map[string]interface{}{
				"cniVersion": "1.0.0",
				"name":       "test",
				"plugins":    []map[string]interface{}{},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configData := mustMarshalJSON(tt.config)
			result := validator.ValidateCNINetworkConfig(configData)
			if result.Valid != tt.expected {
				t.Errorf("expected valid=%v, got valid=%v, errors: %v", tt.expected, result.Valid, result.Errors)
			}
		})
	}
}

func TestValidateCNIPlugin(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		plugin   map[string]interface{}
		expected bool
	}{
		{
			name: "valid macvlan plugin",
			plugin: map[string]interface{}{
				"type":   "macvlan",
				"master": "eth0",
				"mode":   "bridge",
			},
			expected: true,
		},
		{
			name: "valid vlan plugin",
			plugin: map[string]interface{}{
				"type":   "vlan",
				"master": "eth0",
				"vlanId": float64(100),
			},
			expected: true,
		},
		{
			name: "invalid vlan ID",
			plugin: map[string]interface{}{
				"type":   "vlan",
				"master": "eth0",
				"vlanId": float64(5000), // Invalid: > 4095
			},
			expected: false,
		},
		{
			name: "missing plugin type",
			plugin: map[string]interface{}{
				"master": "eth0",
			},
			expected: false,
		},
		{
			name: "invalid interface name",
			plugin: map[string]interface{}{
				"type":   "macvlan",
				"master": "eth0@invalid", // Invalid: contains @
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.validateCNIPlugin(tt.plugin, 0)
			if result.Valid != tt.expected {
				t.Errorf("expected valid=%v, got valid=%v, errors: %v", tt.expected, result.Valid, result.Errors)
			}
		})
	}
}

func TestValidateHostLocalIPAM(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		plugin   map[string]interface{}
		expected bool
	}{
		{
			name: "valid host-local IPAM",
			plugin: map[string]interface{}{
				"type": "host-local",
				"ranges": []interface{}{
					[]interface{}{
						map[string]interface{}{"subnet": "10.10.1.0/24"},
					},
				},
			},
			expected: true,
		},
		{
			name: "invalid CIDR",
			plugin: map[string]interface{}{
				"type": "host-local",
				"ranges": []interface{}{
					[]interface{}{
						map[string]interface{}{"subnet": "10.10.1.0/33"}, // Invalid: /33 is too large
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &Result{Valid: true}
			validator.validateHostLocalIPAM(tt.plugin, result)
			if result.Valid != tt.expected {
				t.Errorf("expected valid=%v, got valid=%v, errors: %v", tt.expected, result.Valid, result.Errors)
			}
		})
	}
}

func TestIsValidInterfaceName(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid short name", "eth0", true},
		{"valid long name", "eth0123456789", true},
		{"valid with underscore", "eth_0", true},
		{"empty name", "", false},
		{"too long", "eth01234567890123", false}, // 16 chars
		{"invalid char", "eth0@", false},
		{"invalid char", "eth-0", false},
		{"invalid char", "eth.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.isValidInterfaceName(tt.input)
			if result != tt.expected {
				t.Errorf("isValidInterfaceName(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsValidCIDR(t *testing.T) {
	validator := New("cni.dra.networking.x-k8s.io")

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"valid IPv4 CIDR", "10.10.1.0/24", true},
		{"valid IPv6 CIDR", "2001:db8::/32", true},
		{"invalid CIDR", "10.10.1.0/33", false},
		{"invalid CIDR", "10.10.1.0", false},
		{"invalid CIDR", "not-a-cidr", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validator.isValidCIDR(tt.input)
			if result != tt.expected {
				t.Errorf("isValidCIDR(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

func mustMarshalJSON(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
