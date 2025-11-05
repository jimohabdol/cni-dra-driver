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

// Package validation provides validation for CNI DRA driver
package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cni-dra-driver/apis/v1alpha1"
)

// Error represents a validation error with field, value, and message information.
type Error struct {
	Field   string
	Value   interface{}
	Message string
}

func (e Error) Error() string {
	return fmt.Sprintf("validation failed for field %q: %s", e.Field, e.Message)
}

type Result struct {
	Valid  bool
	Errors []Error
}

// Add validation error to the result.
func (r *Result) AddError(field string, value interface{}, message string) {
	r.Valid = false
	r.Errors = append(r.Errors, Error{
		Field:   field,
		Value:   value,
		Message: message,
	})
}

// Validator provides validation for CNI DRA driver configurations.
type Validator struct {
	driverName string
}

// New validator instance with the specified driver name.
func New(driverName string) *Validator {
	return &Validator{
		driverName: driverName,
	}
}

// validate ResourceClaim object.
func (v *Validator) ValidateResourceClaim(claim *resourcev1.ResourceClaim) *Result {
	result := &Result{Valid: true}

	if claim == nil {
		result.AddError("claim", nil, "ResourceClaim cannot be nil")
		return result
	}

	if claim.Spec.Devices.Config != nil {
		for i, config := range claim.Spec.Devices.Config {
			if config.Opaque != nil && config.Opaque.Driver == v.driverName {
				cniResult := v.ValidateCNIConfig(config.Opaque.Parameters)
				for _, err := range cniResult.Errors {
					result.AddError(fmt.Sprintf("spec.devices.config[%d].opaque.parameters.%s", i, err.Field), err.Value, err.Message)
				}

				if len(config.Requests) == 0 {
					result.AddError(fmt.Sprintf("spec.devices.config[%d].requests", i), config.Requests, "at least one request must be specified")
				}
			}
		}
	}

	if claim.Status.Allocation != nil {
		if len(claim.Status.ReservedFor) > 0 {
			if len(claim.Status.ReservedFor) != 1 {
				result.AddError("status.reservedFor", len(claim.Status.ReservedFor), "ResourceClaim must be reserved for exactly one pod")
			} else if claim.Status.ReservedFor[0].Resource != "pods" || claim.Status.ReservedFor[0].APIGroup != "" {
				result.AddError("status.reservedFor[0]", claim.Status.ReservedFor[0], "ResourceClaim must be reserved for pods resource")
			}
		}

		for i, deviceResult := range claim.Status.Allocation.Devices.Results {
			if deviceResult.Driver == v.driverName {
				if deviceResult.Request == "" {
					result.AddError(fmt.Sprintf("status.allocation.devices.results[%d].request", i), deviceResult.Request, "request name cannot be empty")
				}
				if deviceResult.Device == "" {
					result.AddError(fmt.Sprintf("status.allocation.devices.results[%d].device", i), deviceResult.Device, "device name cannot be empty")
				}
			}
		}

		for i, config := range claim.Status.Allocation.Devices.Config {
			if config.Opaque != nil && config.Opaque.Driver == v.driverName {
				cniResult := v.ValidateCNIConfig(config.Opaque.Parameters)
				for _, err := range cniResult.Errors {
					result.AddError(fmt.Sprintf("status.allocation.devices.config[%d].opaque.parameters.%s", i, err.Field), err.Value, err.Message)
				}

				if len(config.Requests) == 0 {
					result.AddError(fmt.Sprintf("status.allocation.devices.config[%d].requests", i), config.Requests, "at least one request must be specified")
				}
			}
		}
	}

	return result
}

func (v *Validator) ValidateCNIConfig(parameters runtime.RawExtension) *Result {
	result := &Result{Valid: true}

	cniConfig := &v1alpha1.CNIConfig{}
	decoder := json.NewDecoder(bytes.NewReader(parameters.Raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cniConfig); err != nil {
		result.AddError("raw", string(parameters.Raw), fmt.Sprintf("failed to unmarshal CNIConfig: %v", err))
		return result
	}

	if cniConfig.APIVersion != "cni.networking.x-k8s.io/v1alpha1" {
		result.AddError("apiVersion", cniConfig.APIVersion, "must be 'cni.networking.x-k8s.io/v1alpha1'")
	}

	if cniConfig.Kind != "CNI" {
		result.AddError("kind", cniConfig.Kind, "must be 'CNI'")
	}

	if err := v.validateInterfaceName(cniConfig.IfName); err != nil {
		result.AddError("ifName", cniConfig.IfName, err.Error())
	}

	if len(cniConfig.Config.Raw) == 0 {
		result.AddError("config", cniConfig.Config, "CNI config cannot be empty")
		return result
	}

	cniSchemaResult := v.validateCNIConfigSchema(cniConfig.Config.Raw)
	for _, err := range cniSchemaResult.Errors {
		result.AddError(fmt.Sprintf("config.%s", err.Field), err.Value, err.Message)
	}

	return result
}

// validate the basic CNI configuration schema
func (v *Validator) validateCNIConfigSchema(configData []byte) *Result {
	result := &Result{Valid: true}

	var cniConfig map[string]interface{}
	if err := json.Unmarshal(configData, &cniConfig); err != nil {
		result.AddError("raw", string(configData), fmt.Sprintf("failed to parse CNI config JSON: %v", err))
		return result
	}

	// Validate CNI version
	if version, ok := cniConfig["cniVersion"].(string); ok {
		if version == "" {
			result.AddError("cniVersion", version, "cniVersion cannot be empty")
		}
	} else {
		result.AddError("cniVersion", cniConfig["cniVersion"], "cniVersion is required and must be a string")
	}

	// Validate name
	if name, ok := cniConfig["name"].(string); ok {
		if name == "" {
			result.AddError("name", name, "name cannot be empty")
		}
	} else {
		result.AddError("name", cniConfig["name"], "name is required and must be a string")
	}

	// Validate plugins array
	if plugins, ok := cniConfig["plugins"]; ok {
		if pluginsArray, ok := plugins.([]interface{}); ok {
			if len(pluginsArray) == 0 {
				result.AddError("plugins", pluginsArray, "plugins array cannot be empty")
			}
		} else {
			result.AddError("plugins", plugins, "plugins must be an array")
		}
	}


	return result
}

// Helper validation functions

func (v *Validator) validateInterfaceName(ifName string) error {
	if ifName == "" {
		return fmt.Errorf("interface name cannot be empty")
	}
	if !v.isValidInterfaceName(ifName) {
		return fmt.Errorf("invalid interface name format")
	}
	return nil
}

func (v *Validator) isValidInterfaceName(name string) bool {
	// Linux interface name validation only allow 1-15 characters, alphanumeric and underscore
	matched, _ := regexp.MatchString(`^[a-zA-Z0-9_]{1,15}$`, name)
	return matched
}
