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
	"fmt"
	"io"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/klog/v2"
)

// Handle Kubernetes admission webhook requests for CNI DRA driver validation.
type WebhookValidator struct {
	validator *Validator
	decoder   runtime.Decoder
}

// NewWebhookValidator creates a new WebhookValidator instance.
func NewWebhookValidator(driverName string) *WebhookValidator {
	scheme := runtime.NewScheme()
	resourcev1.AddToScheme(scheme)
	admissionv1.AddToScheme(scheme)

	codecs := serializer.NewCodecFactory(scheme)
	decoder := codecs.UniversalDeserializer()

	return &WebhookValidator{
		validator: New(driverName),
		decoder:   decoder,
	}
}

func (w *WebhookValidator) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var body []byte
	if request.Body != nil {
		if data, err := io.ReadAll(request.Body); err == nil {
			body = data
		}
	}

	if len(body) == 0 {
		klog.Error("Empty request body")
		http.Error(writer, "Empty request body", http.StatusBadRequest)
		return
	}

	// parse admission request
	var admissionReview admissionv1.AdmissionReview
	if _, _, err := w.decoder.Decode(body, nil, &admissionReview); err != nil {
		klog.Errorf("Can't decode body: %v", err)
		http.Error(writer, "Can't decode body", http.StatusBadRequest)
		return
	}

	admissionResponse := w.validateAdmissionRequest(admissionReview.Request)

	admissionReview.Response = admissionResponse
	admissionReview.Response.UID = admissionReview.Request.UID

	responseBytes, err := json.Marshal(admissionReview)
	if err != nil {
		klog.Errorf("Can't encode response: %v", err)
		http.Error(writer, "Can't encode response", http.StatusInternalServerError)
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	writer.Write(responseBytes)
}

func (w *WebhookValidator) validateAdmissionRequest(request *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	response := &admissionv1.AdmissionResponse{
		UID:     request.UID,
		Allowed: true,
	}

	if request.Kind.Kind != "ResourceClaim" {
		return response
	}

	var claim resourcev1.ResourceClaim
	if err := json.Unmarshal(request.Object.Raw, &claim); err != nil {
		response.Allowed = false
		response.Result = &metav1.Status{
			Message: fmt.Sprintf("Failed to parse ResourceClaim: %v", err),
		}
		return response
	}

	validationResult := w.validator.ValidateResourceClaim(&claim)
	if !validationResult.Valid {
		response.Allowed = false
		response.Result = &metav1.Status{
			Message: fmt.Sprintf("ResourceClaim validation failed: %v", validationResult.Errors),
		}
		return response
	}

	return response
}


func WebhookHandler(driverName string) http.Handler {
	return NewWebhookValidator(driverName)
}
