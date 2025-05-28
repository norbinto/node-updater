/*
Copyright 2025.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SafeEvictSpec defines the desired state of SafeEvict.
type SafeEvictSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make manifests" to regenerate code after modifying this file

	// only pods will be effected with this labels
	LabelSelector map[string]string `json:"labelSelector,omitempty"`
	// +kubebuilder:validation:Required
	// if this is the last line in the logs, it is safe to evict
	LastLogLines []string `json:"lastLogLines,omitempty"`
	// nodepools which will be monitored by node-updater controller
	Nodepools []string `json:"nodepools,omitempty"`
	// namespaces which will be monitored by node-updater controller
	Namespaces []string `json:"namespaces,omitempty"`
	// +kubebuilder:validation:Required
	// pool name which will be cloned for creating backup pool
	BaseForBackupPool string `json:"baseForBackupPoolName,omitempty"`
}

// SafeEvictStatus defines the observed state of SafeEvict.
type SafeEvictStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// SafeEvict is the Schema for the safeevicts API.
type SafeEvict struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SafeEvictSpec   `json:"spec,omitempty"`
	Status SafeEvictStatus `json:"status,omitempty"`
}

func (s *SafeEvict) GetConfigmapName() string {
	return "tmp" + s.Name
}

// GetTemporaryNodepoolName returns the name of the temporary nodepool. AKS allows maximum 12 chars in the nodepool name
func (s *SafeEvict) GetTemporaryNodepoolName() string {
	if len(s.Spec.BaseForBackupPool) > 9 {
		return "tmp" + s.Spec.BaseForBackupPool[:9]
	}
	return "tmp" + s.Spec.BaseForBackupPool
}

// +kubebuilder:object:root=true

// SafeEvictList contains a list of SafeEvict.
type SafeEvictList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SafeEvict `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SafeEvict{}, &SafeEvictList{})
}
