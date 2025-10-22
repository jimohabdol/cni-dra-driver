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
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/containernetworking/cni/libcni"
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

// Validate ResourceClaim object.
func (v *Validator) ValidateResourceClaim(claim *resourcev1.ResourceClaim) *Result {
	result := &Result{Valid: true}

	if claim == nil {
		result.AddError("claim", nil, "ResourceClaim cannot be nil")
		return result
	}

	if claim.Status.Allocation == nil {
		result.AddError("status.allocation", nil, "ResourceClaim must be allocated")
		return result
	}

	if len(claim.Status.ReservedFor) != 1 {
		result.AddError("status.reservedFor", len(claim.Status.ReservedFor), "ResourceClaim must be reserved for exactly one pod")
		return result
	}

	if claim.Status.ReservedFor[0].Resource != "pods" || claim.Status.ReservedFor[0].APIGroup != "" {
		result.AddError("status.reservedFor[0]", claim.Status.ReservedFor[0], "ResourceClaim must be reserved for pods resource")
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

	return result
}

// Validate CNI configuration parameters.
func (v *Validator) ValidateCNIConfig(parameters runtime.RawExtension) *Result {
	result := &Result{Valid: true}

	cniConfig := &v1alpha1.CNIConfig{}
	if err := json.Unmarshal(parameters.Raw, cniConfig); err != nil {
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

	cniResult := v.ValidateCNINetworkConfig(cniConfig.Config.Raw)
	for _, err := range cniResult.Errors {
		result.AddError(fmt.Sprintf("config.%s", err.Field), err.Value, err.Message)
	}

	return result
}

// Validate the CNI network configuration.
func (v *Validator) ValidateCNINetworkConfig(configData []byte) *Result {
	result := &Result{Valid: true}

	var cniConfig map[string]interface{}
	if err := json.Unmarshal(configData, &cniConfig); err != nil {
		result.AddError("raw", string(configData), fmt.Sprintf("failed to parse CNI config JSON: %v", err))
		return result
	}

	// validate CNI version
	if version, ok := cniConfig["cniVersion"].(string); ok {
		if !v.isValidCNIVersion(version) {
			result.AddError("cniVersion", version, "unsupported CNI version")
		}
	} else {
		result.AddError("cniVersion", cniConfig["cniVersion"], "cniVersion is required and must be a string")
	}

	// validate name
	if name, ok := cniConfig["name"].(string); ok {
		if name == "" {
			result.AddError("name", name, "name cannot be empty")
		}
	} else {
		result.AddError("name", cniConfig["name"], "name is required and must be a string")
	}

	// validate plugins array
	if plugins, ok := cniConfig["plugins"].([]interface{}); ok {
		if len(plugins) == 0 {
			result.AddError("plugins", plugins, "plugins array cannot be empty")
		} else {
			for i, plugin := range plugins {
				pluginResult := v.validateCNIPlugin(plugin, i)
				for _, err := range pluginResult.Errors {
					result.AddError(fmt.Sprintf("plugins[%d].%s", i, err.Field), err.Value, err.Message)
				}
			}
		}
	} else {
		result.AddError("plugins", cniConfig["plugins"], "plugins is required and must be an array")
	}

	return result
}

// validates individual CNI plugin configuration
func (v *Validator) validateCNIPlugin(plugin interface{}, index int) *Result {
	result := &Result{Valid: true}

	pluginMap, ok := plugin.(map[string]interface{})
	if !ok {
		result.AddError("", plugin, "plugin must be an object")
		return result
	}

	// Validate plugin type
	pluginType, ok := pluginMap["type"].(string)
	if !ok || pluginType == "" {
		result.AddError("type", pluginMap["type"], "plugin type is required and must be a non-empty string")
		return result
	}

	// plugin-specific validation
	switch pluginType {
	case "bridge":
		v.validateBridgePlugin(pluginMap, result)
	case "macvlan":
		v.validateMacvlanPlugin(pluginMap, result)
	case "vlan":
		v.validateVlanPlugin(pluginMap, result)
	case "host-local":
		v.validateHostLocalIPAM(pluginMap, result)
	case "static":
		v.validateStaticIPAM(pluginMap, result)
	default:
		v.validateGenericPlugin(pluginMap, result)
	}

	return result
}

// validates bridge plugin configuration
func (v *Validator) validateBridgePlugin(plugin map[string]interface{}, result *Result) {
	// Bridge plugin specific validations
	v.validateOptionalInterfaceField(plugin, "bridge", result)

	// IPAM validation
	v.validateIPAMField(plugin, result)
}

// validates macvlan plugin configuration
func (v *Validator) validateMacvlanPlugin(plugin map[string]interface{}, result *Result) {
	// master interface validation
	v.validateRequiredInterfaceField(plugin, "master", result)

	// Mode validation
	validModes := []string{"bridge", "vepa", "private", "passthru"}
	v.validateStringField(plugin, "mode", validModes, result)

	// IPAM validation
	v.validateIPAMField(plugin, result)
}

// validates vlan plugin configuration
func (v *Validator) validateVlanPlugin(plugin map[string]interface{}, result *Result) {
	// Master interface validation
	v.validateRequiredInterfaceField(plugin, "master", result)

	// VLAN ID validation
	v.validateRequiredNumericField(plugin, "vlanId", 0, 4095, result)

	// IPAM validation
	v.validateIPAMField(plugin, result)
}

// validates IPAM plugin configuration
func (v *Validator) validateIPAMPlugin(ipam map[string]interface{}, result *Result, prefix string) {
	// Validate IPAM type
	if ipamType, ok := ipam["type"].(string); ok {
		switch ipamType {
		case "host-local":
			v.validateHostLocalIPAM(ipam, result)
		case "static":
			v.validateStaticIPAM(ipam, result)
		default:
			// For unknown IPAM types, do basic validation
			v.validateGenericIPAM(ipam, result)
		}
	} else {
		result.AddError(fmt.Sprintf("%s.type", prefix), ipam["type"], "IPAM type is required and must be a string")
	}
}

// validates generic IPAM configuration
func (v *Validator) validateGenericIPAM(ipam map[string]interface{}, result *Result) {
		// validate unknown IPAM types
		if ipamType, ok := ipam["type"].(string); ok {
			if ipamType == "" {
			result.AddError("type", ipamType, "IPAM type cannot be empty")
		}
	} else {
		result.AddError("type", ipam["type"], "IPAM type is required and must be a string")
	}

	// Validate common IPAM fields that might be present, they are common across many IPAM plugins.
	// Validate routes if present
	if routes, ok := ipam["routes"].([]interface{}); ok {
		for i, route := range routes {
			if routeMap, ok := route.(map[string]interface{}); ok {
				// Validate route destination as a CIDR.
				if dst, ok := routeMap["dst"].(string); ok {
					if !v.isValidCIDR(dst) {
						result.AddError(fmt.Sprintf("routes[%d].dst", i), dst, "invalid route destination CIDR")
					}
				} else if routeMap["dst"] != nil {
					result.AddError(fmt.Sprintf("routes[%d].dst", i), routeMap["dst"], "route destination must be a string")
				}

				// Validate route gateway if present
				if gw, ok := routeMap["gw"].(string); ok {
					if gw != "" && !v.isValidIP(gw) {
						result.AddError(fmt.Sprintf("routes[%d].gw", i), gw, "invalid route gateway IP")
					}
				}
			} else {
				result.AddError(fmt.Sprintf("routes[%d]", i), route, "route must be an object")
			}
		}
	}

	// Validate DNS if present
	if dns, ok := ipam["dns"].(map[string]interface{}); ok {
		// Validate nameservers
		if nameservers, ok := dns["nameservers"].([]interface{}); ok {
			for i, ns := range nameservers {
				if nsStr, ok := ns.(string); ok {
					if !v.isValidIP(nsStr) {
						result.AddError(fmt.Sprintf("dns.nameservers[%d]", i), nsStr, "invalid nameserver IP")
					}
				} else {
					result.AddError(fmt.Sprintf("dns.nameservers[%d]", i), ns, "nameserver must be a string")
				}
			}
		}

		// Validate search domains
		if search, ok := dns["search"].([]interface{}); ok {
			for i, domain := range search {
				if domainStr, ok := domain.(string); ok {
					if domainStr == "" {
						result.AddError(fmt.Sprintf("dns.search[%d]", i), domainStr, "search domain cannot be empty")
					}
				} else {
					result.AddError(fmt.Sprintf("dns.search[%d]", i), domain, "search domain must be a string")
				}
			}
		}
	}

	// Validate ranges if present
	if ranges, ok := ipam["ranges"].([]interface{}); ok {
		for i, rangeItem := range ranges {
			if rangeMap, ok := rangeItem.(map[string]interface{}); ok {
				// Validate subnet if present
				if subnet, ok := rangeMap["subnet"].(string); ok {
					if !v.isValidCIDR(subnet) {
						result.AddError(fmt.Sprintf("ranges[%d].subnet", i), subnet, "invalid subnet CIDR")
					}
				}

				// Validate range if present
				if rangeStr, ok := rangeMap["rangeStart"].(string); ok {
					if !v.isValidIP(rangeStr) {
						result.AddError(fmt.Sprintf("ranges[%d].rangeStart", i), rangeStr, "invalid range start IP")
					}
				}
				if rangeStr, ok := rangeMap["rangeEnd"].(string); ok {
					if !v.isValidIP(rangeStr) {
						result.AddError(fmt.Sprintf("ranges[%d].rangeEnd", i), rangeStr, "invalid range end IP")
					}
				}
			} else {
				result.AddError(fmt.Sprintf("ranges[%d]", i), rangeItem, "range must be an object")
			}
		}
	}
}

// validate host-local IPAM configuration
func (v *Validator) validateHostLocalIPAM(plugin map[string]interface{}, result *Result) {
	// Subnet validation
	if ranges, ok := plugin["ranges"].([]interface{}); ok {
		for i, rangeGroup := range ranges {
			if rangeArray, ok := rangeGroup.([]interface{}); ok {
				for j, subnet := range rangeArray {
					if subnetMap, ok := subnet.(map[string]interface{}); ok {
						if subnetStr, ok := subnetMap["subnet"].(string); ok {
							if !v.isValidCIDR(subnetStr) {
								result.AddError(fmt.Sprintf("ranges[%d][%d].subnet", i, j), subnetStr, "invalid CIDR format")
							}
						} else {
							result.AddError(fmt.Sprintf("ranges[%d][%d].subnet", i, j), subnetMap["subnet"], "subnet must be a string")
						}
					} else {
						result.AddError(fmt.Sprintf("ranges[%d][%d]", i, j), subnet, "subnet must be a map")
					}
				}
			} else {
				result.AddError(fmt.Sprintf("ranges[%d]", i), rangeGroup, "range group must be an array")
			}
		}
	} else {
		result.AddError("ranges", plugin["ranges"], "ranges must be an array")
	}
}

// validate static IPAM configuration
func (v *Validator) validateStaticIPAM(plugin map[string]interface{}, result *Result) {
	// Addresses validation
	if addresses, ok := plugin["addresses"].([]interface{}); ok {
		for i, addr := range addresses {
			if addrStr, ok := addr.(string); ok {
				if !v.isValidCIDR(addrStr) {
					result.AddError(fmt.Sprintf("addresses[%d]", i), addrStr, "invalid CIDR format")
				}
			} else {
				result.AddError(fmt.Sprintf("addresses[%d]", i), addr, "address must be a string")
			}
		}
	}
}

// validate generic plugin configuration for unknown plugin types
func (v *Validator) validateGenericPlugin(plugin map[string]interface{}, result *Result) {

	// Validate plugin type
	if pluginType, ok := plugin["type"].(string); ok {
		if pluginType == "" {
			result.AddError("type", pluginType, "plugin type cannot be empty")
		}
	} else {
		result.AddError("type", plugin["type"], "plugin type is required and must be a string")
	}

	// Validate common interface-related fields
	interfaceFields := []string{"master", "bridge", "device", "ifname", "interface"}
	for _, field := range interfaceFields {
		v.validateOptionalInterfaceField(plugin, field, result)
	}

	// Validate common network configuration fields
	v.validateNumericField(plugin, "mtu", 68, 65535, result)
	v.validateNumericField(plugin, "vlanId", 0, 4095, result)
	v.validateNumericField(plugin, "vlan", 0, 4095, result)

	// IPAM validation
	v.validateIPAMField(plugin, result)

	// Validate common boolean fields
	boolFields := []string{"promisc", "hairpin", "preserveDefaultVlan"}
	for _, field := range boolFields {
		if value, ok := plugin[field]; ok {
			if _, isBool := value.(bool); !isBool {
				result.AddError(field, value, fmt.Sprintf("%s must be a boolean", field))
			}
		}
	}

	// Validate common string fields, should be non-empty.
	stringFields := []string{"name", "cniVersion"}
	for _, field := range stringFields {
		if value, ok := plugin[field].(string); ok {
			if value == "" {
				result.AddError(field, value, fmt.Sprintf("%s cannot be empty", field))
			}
		}
	}

	// Validate common array fields
	arrayFields := []string{"routes", "addresses"}
	for _, field := range arrayFields {
		if value, ok := plugin[field]; ok {
			if _, isArray := value.([]interface{}); !isArray {
				result.AddError(field, value, fmt.Sprintf("%s must be an array", field))
			}
		}
	}

	// Validate common map fields
	mapFields := []string{"capabilities", "args", "runtimeConfig"}
	for _, field := range mapFields {
		if value, ok := plugin[field]; ok {
			if _, isMap := value.(map[string]interface{}); !isMap {
				result.AddError(field, value, fmt.Sprintf("%s must be an object", field))
			}
		}
	}

	// Validate IP addresses and CIDRs in common fields
	ipFields := []string{"gateway", "gw", "ip", "address"}
	for _, field := range ipFields {
		if value, ok := plugin[field].(string); ok {
			if value != "" {
				if !v.isValidIP(value) && !v.isValidCIDR(value) {
					result.AddError(field, value, fmt.Sprintf("invalid IP address or CIDR in %s", field))
				}
			}
		}
	}

	// Validate DNS configuration if present
	if dns, ok := plugin["dns"].(map[string]interface{}); ok {
		// Validate nameservers
		if nameservers, ok := dns["nameservers"].([]interface{}); ok {
			for i, ns := range nameservers {
				if nsStr, ok := ns.(string); ok {
					if !v.isValidIP(nsStr) {
						result.AddError(fmt.Sprintf("dns.nameservers[%d]", i), nsStr, "invalid nameserver IP")
					}
				} else {
					result.AddError(fmt.Sprintf("dns.nameservers[%d]", i), ns, "nameserver must be a string")
				}
			}
		}

		// Validate search domains
		if search, ok := dns["search"].([]interface{}); ok {
			for i, domain := range search {
				if domainStr, ok := domain.(string); ok {
					if domainStr == "" {
						result.AddError(fmt.Sprintf("dns.search[%d]", i), domainStr, "search domain cannot be empty")
					}
				} else {
					result.AddError(fmt.Sprintf("dns.search[%d]", i), domain, "search domain must be a string")
				}
			}
		}
	}

	// Validate routes if present
	if routes, ok := plugin["routes"].([]interface{}); ok {
		for i, route := range routes {
			if routeMap, ok := route.(map[string]interface{}); ok {
				// Validate route destination (CIDR)
				if dst, ok := routeMap["dst"].(string); ok {
					if !v.isValidCIDR(dst) {
						result.AddError(fmt.Sprintf("routes[%d].dst", i), dst, "invalid route destination CIDR")
					}
				} else if routeMap["dst"] != nil {
					result.AddError(fmt.Sprintf("routes[%d].dst", i), routeMap["dst"], "route destination must be a string")
				}

				// Validate route gateway if present
				if gw, ok := routeMap["gw"].(string); ok {
					if gw != "" && !v.isValidIP(gw) {
						result.AddError(fmt.Sprintf("routes[%d].gw", i), gw, "invalid route gateway IP")
					}
				}
			} else {
				result.AddError(fmt.Sprintf("routes[%d]", i), route, "route must be an object")
			}
		}
	}
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

func (v *Validator) isValidCNIVersion(version string) bool {
	validVersions := []string{"0.1.0", "0.2.0", "0.3.0", "0.3.1", "0.4.0", "1.0.0"}
	for _, v := range validVersions {
		if version == v {
			return true
		}
	}
	return false
}

func (v *Validator) isValidMode(mode string, validModes []string) bool {
	for _, m := range validModes {
		if mode == m {
			return true
		}
	}
	return false
}

func (v *Validator) isValidCIDR(cidr string) bool {
	_, _, err := net.ParseCIDR(cidr)
	return err == nil
}

func (v *Validator) isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}

// Validate and check CNI configuration.
func (v *Validator) ValidateCNIWithLibcni(configData []byte) *Result {
	result := &Result{Valid: true}

	var parseErr error

	_, parseErr = libcni.NetworkConfFromBytes(configData)
	if parseErr != nil {
		_, parseErr = libcni.NetworkPluginConfFromBytes(configData)
		if parseErr != nil {
			result.AddError("config", string(configData), fmt.Sprintf("failed to parse as CNI config: %v", parseErr))
			return result
		}
	}

	var configMap map[string]interface{}
	if err := json.Unmarshal(configData, &configMap); err == nil {
		if _, hasPlugins := configMap["plugins"]; !hasPlugins {
			singleConfig := map[string]interface{}{
				"cniVersion": configMap["cniVersion"],
				"name":       configMap["name"],
				"plugins":    []interface{}{configMap},
			}
			singleConfigBytes, _ := json.Marshal(singleConfig)
			validationResult := v.ValidateCNINetworkConfig(singleConfigBytes)
			result.Valid = validationResult.Valid
			result.Errors = append(result.Errors, validationResult.Errors...)
		} else {
			validationResult := v.ValidateCNINetworkConfig(configData)
			result.Valid = validationResult.Valid
			result.Errors = append(result.Errors, validationResult.Errors...)
		}
	} else {
		validationResult := v.ValidateCNINetworkConfig(configData)
		result.Valid = validationResult.Valid
		result.Errors = append(result.Errors, validationResult.Errors...)
	}

	return result
}

// Validate required interface field
func (v *Validator) validateRequiredInterfaceField(plugin map[string]interface{}, field string, result *Result) {
	if value, ok := plugin[field].(string); ok {
		if value == "" {
			result.AddError(field, value, fmt.Sprintf("%s interface name cannot be empty", field))
		} else if !v.isValidInterfaceName(value) {
			result.AddError(field, value, fmt.Sprintf("invalid %s interface name", field))
		}
	} else {
		result.AddError(field, plugin[field], fmt.Sprintf("%s interface name is required", field))
	}
}

// validates an optional interface field
func (v *Validator) validateOptionalInterfaceField(plugin map[string]interface{}, field string, result *Result) {
	if value, ok := plugin[field].(string); ok && value != "" {
		if !v.isValidInterfaceName(value) {
			result.AddError(field, value, fmt.Sprintf("invalid %s interface name", field))
		}
	}
}

// vlidates a numeric field with min/max constraints
func (v *Validator) validateNumericField(plugin map[string]interface{}, field string, min, max float64, result *Result) {
	if value, ok := plugin[field].(float64); ok {
		if value < min || value > max {
			result.AddError(field, value, fmt.Sprintf("%s must be between %.0f and %.0f", field, min, max))
		}
	} else if plugin[field] != nil {
		result.AddError(field, plugin[field], fmt.Sprintf("%s must be a number", field))
	}
}

// validates required numeric field
func (v *Validator) validateRequiredNumericField(plugin map[string]interface{}, field string, min, max float64, result *Result) {
	if value, ok := plugin[field].(float64); ok {
		if value < min || value > max {
			result.AddError(field, value, fmt.Sprintf("%s must be between %.0f and %.0f", field, min, max))
		}
	} else {
		result.AddError(field, plugin[field], fmt.Sprintf("%s is required and must be a number", field))
	}
}

// validates string field with valid values
func (v *Validator) validateStringField(plugin map[string]interface{}, field string, validValues []string, result *Result) {
	if value, ok := plugin[field].(string); ok {
		if len(validValues) > 0 && !v.isValidMode(value, validValues) {
			result.AddError(field, value, fmt.Sprintf("invalid %s, must be one of: %s", field, strings.Join(validValues, ", ")))
		}
	}
}

// validates IPAM configuration if present
func (v *Validator) validateIPAMField(plugin map[string]interface{}, result *Result) {
	if ipam, ok := plugin["ipam"].(map[string]interface{}); ok {
		v.validateIPAMPlugin(ipam, result, "ipam")
	}
}
