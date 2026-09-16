// Copyright (c) KAITO authors.
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

package inferenceset

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaitov1beta1 "github.com/kaito-project/kaito/api/v1beta1"
	"github.com/kaito-project/kaito/pkg/custommodel"
	"github.com/kaito-project/kaito/pkg/utils/inferenceset"
)

// reconcileResolvedModel resolves a bring-your-own model and records what it
// resolved to in status, before any replica Workspace is created from it.
//
// Resolving here as well as in the Workspace controller is deliberate: the
// InferenceSet is where the model identity is fixed for the whole set, so a
// replaced ConfigMap must be caught before it can be propagated into new
// replicas that would then disagree with the existing ones.
func (c *InferenceSetReconciler) reconcileResolvedModel(ctx context.Context, iObj *kaitov1beta1.InferenceSet) error {
	if !custommodel.IsCustom(&iObj.Spec.Template.Inference) {
		return nil
	}

	record, err := custommodel.Resolve(ctx, c.Client, &iObj.Spec.Template.Inference, iObj.Namespace,
		iObj.Status.ResolvedModel)
	if err != nil {
		reason := custommodel.ReasonInvalid
		var replaced *custommodel.ReplacedError
		if errors.As(err, &replaced) {
			reason = custommodel.ReasonReplaced
		}
		klog.ErrorS(err, "failed to resolve custom model", "inferenceset", klog.KObj(iObj), "reason", reason)
		if condErr := inferenceset.UpdateStatusConditionIfNotMatch(ctx, c.Client, iObj,
			kaitov1beta1.WorkspaceConditionTypeModelConfigReady, metav1.ConditionFalse,
			reason, err.Error()); condErr != nil {
			return condErr
		}
		return err
	}

	if err := inferenceset.UpdateInferenceSetStatus(ctx, c.Client,
		&client.ObjectKey{Name: iObj.Name, Namespace: iObj.Namespace},
		func(status *kaitov1beta1.InferenceSetStatus) error {
			status.ResolvedModel = record
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:   string(kaitov1beta1.WorkspaceConditionTypeModelConfigReady),
				Status: metav1.ConditionTrue,
				Reason: custommodel.ReasonResolved,
				Message: fmt.Sprintf("resolved model from ConfigMap %q (%d bytes)",
					iObj.Spec.Template.Inference.Config, record.SizeBytes),
			})
			return nil
		}); err != nil {
		return fmt.Errorf("failed to record resolved model: %w", err)
	}

	iObj.Status.ResolvedModel = record
	return nil
}
