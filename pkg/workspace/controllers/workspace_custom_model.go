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

package controllers

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
	"github.com/kaito-project/kaito/pkg/utils/workspace"
)

// reconcileResolvedModel resolves a bring-your-own model and records what it
// resolved to in status. It runs before sizing and provisioning so that both
// are driven by a model identity that has already been recorded.
func (c *WorkspaceReconciler) reconcileResolvedModel(ctx context.Context, wObj *kaitov1beta1.Workspace) error {
	if !custommodel.IsCustom(wObj.Inference) {
		return nil
	}

	record, err := custommodel.Resolve(ctx, c.Client, wObj.Inference, wObj.Namespace,
		wObj.Status.ResolvedModel)
	if err != nil {
		reason := custommodel.ReasonInvalid
		var replaced *custommodel.ReplacedError
		if errors.As(err, &replaced) {
			reason = custommodel.ReasonReplaced
		}
		klog.ErrorS(err, "failed to resolve custom model", "workspace", klog.KObj(wObj), "reason", reason)
		if condErr := c.setModelConfigCondition(ctx, wObj, metav1.ConditionFalse, reason, err.Error()); condErr != nil {
			return condErr
		}
		return err
	}

	if err := workspace.UpdateWorkspaceStatus(ctx, c.Client, &client.ObjectKey{Name: wObj.Name, Namespace: wObj.Namespace},
		func(status *kaitov1beta1.WorkspaceStatus) error {
			status.ResolvedModel = record
			meta.SetStatusCondition(&status.Conditions, metav1.Condition{
				Type:   string(kaitov1beta1.WorkspaceConditionTypeModelConfigReady),
				Status: metav1.ConditionTrue,
				Reason: custommodel.ReasonResolved,
				Message: fmt.Sprintf("resolved model from ConfigMap %q (%d bytes)",
					wObj.Inference.Config, record.SizeBytes),
			})
			return nil
		}); err != nil {
		return fmt.Errorf("failed to record resolved model: %w", err)
	}

	wObj.Status.ResolvedModel = record
	return nil
}

func (c *WorkspaceReconciler) setModelConfigCondition(ctx context.Context, wObj *kaitov1beta1.Workspace,
	status metav1.ConditionStatus, reason, message string,
) error {
	return workspace.UpdateWorkspaceStatus(ctx, c.Client, &client.ObjectKey{Name: wObj.Name, Namespace: wObj.Namespace},
		func(s *kaitov1beta1.WorkspaceStatus) error {
			meta.SetStatusCondition(&s.Conditions, metav1.Condition{
				Type:    string(kaitov1beta1.WorkspaceConditionTypeModelConfigReady),
				Status:  status,
				Reason:  reason,
				Message: message,
			})
			return nil
		})
}
