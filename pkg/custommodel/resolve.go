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

// Package custommodel resolves bring-your-own model deployments and detects
// when the inputs they were built from have been replaced.
//
// It exists as a separate package so that the Workspace and InferenceSet
// controllers share one implementation: the two must agree exactly on what a
// model resolves to, since an InferenceSet creates Workspaces and a divergence
// would surface as replicas that disagree about what they are serving.
package custommodel

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kaitov1beta1 "github.com/kaito-project/kaito/api/v1beta1"
	"github.com/kaito-project/kaito/pkg/utils/plugin"
	"github.com/kaito-project/kaito/presets/workspace/models"
)

// Condition reasons reported on ModelConfigReady.
const (
	ReasonResolved = "ModelConfigResolved"
	ReasonInvalid  = "ModelConfigInvalid"
	ReasonReplaced = "ModelConfigReplaced"

	// ReasonReferenceModelUnmatched is the reason of the Warning event emitted
	// when a reference model ID selects no model-family setting and is not in
	// the model catalog.
	ReasonReferenceModelUnmatched = "ReferenceModelUnmatched"
)

// IsCustom reports whether an inference spec selects a bring-your-own model.
func IsCustom(inferenceSpec *kaitov1beta1.InferenceSpec) bool {
	return inferenceSpec != nil && inferenceSpec.Preset != nil &&
		plugin.IsCustomPreset(string(inferenceSpec.Preset.Name))
}

// ReplacedError reports that the inputs a deployment was built from no longer
// match what was recorded. It is a distinct type so callers can tell a
// replacement apart from a configuration that simply failed to resolve.
//
// Only the configuration digest can trigger this: the declared size is a
// capacity hint about the same weights and is adopted rather than rejected, so
// there is no need to say which field diverged.
type ReplacedError struct {
	ConfigMapName string
	Recorded      string
	Found         string
}

func (e *ReplacedError) Error() string {
	return fmt.Sprintf(
		"ConfigMap %q no longer matches the model this deployment was sized and configured from "+
			"(recorded config digest %s, found %s); serving a different model requires a new deployment",
		e.ConfigMapName, e.Recorded, e.Found)
}

// Resolve resolves the model referenced by the inference spec and returns the
// record to store in status.
//
// When recorded is non-nil it is treated as the identity the deployment was
// already built from, and a changed configuration is reported as a
// *ReplacedError rather than adopted. A ConfigMap is immutable, but it can be deleted and recreated
// under the same name; by then the deployment has already been sized and its
// runtime arguments rendered from the original inputs, so quietly re-resolving
// would serve different weights on capacity chosen for a different model.
//
// Only the configuration digest is compared. The declared size is allowed to
// drift: it is an operator-supplied capacity hint about the same weights, not
// part of what identifies them.
func Resolve(ctx context.Context, kubeClient client.Client, inferenceSpec *kaitov1beta1.InferenceSpec,
	namespace string, recorded *kaitov1beta1.ResolvedModel,
) (*kaitov1beta1.ResolvedModel, error) {
	statusRecord, _, err := resolve(ctx, kubeClient, inferenceSpec, namespace, recorded)
	return statusRecord, err
}

func resolve(ctx context.Context, kubeClient client.Client, inferenceSpec *kaitov1beta1.InferenceSpec,
	namespace string, recorded *kaitov1beta1.ResolvedModel,
) (*kaitov1beta1.ResolvedModel, *models.ResolvedCustomModel, error) {
	resolved, err := models.ResolveCustomModel(ctx, kubeClient, inferenceSpec.Config, namespace)
	if err != nil {
		return nil, nil, err
	}

	if recorded != nil {
		if recorded.ConfigSHA256 != "" && recorded.ConfigSHA256 != resolved.Digest {
			return nil, nil, &ReplacedError{
				ConfigMapName: inferenceSpec.Config,
				Recorded:      recorded.ConfigSHA256,
				Found:         resolved.Digest,
			}
		}
		// A changed size is adopted rather than rejected. Unlike the
		// configuration, the size does not say what the model *is*, only how
		// much room it needs, so correcting a mistaken value is a correction to
		// the same model rather than a substitution of a different one. Status
		// records what it resolved to now; capacity already provisioned from the
		// previous value is not revisited, and the startup size check reports
		// the discrepancy if the running pod is affected.
		if recorded.SizeBytes != 0 && recorded.SizeBytes != resolved.SizeBytes {
			klog.InfoS("declared model size changed; adopting the new value",
				"configMap", inferenceSpec.Config,
				"recordedBytes", recorded.SizeBytes, "foundBytes", resolved.SizeBytes)
		}
	}

	return &kaitov1beta1.ResolvedModel{
		ConfigSHA256: resolved.Digest,
		SizeBytes:    resolved.SizeBytes,
	}, resolved, nil
}

// StatusWriter persists the outcome of custom-model resolution into a
// controller's own status type. Reconcile calls SetModelConfigCondition on
// failure and SaveResolved on success; the Workspace and InferenceSet
// controllers each supply an implementation over their respective status.
type StatusWriter interface {
	// SetModelConfigCondition writes the ModelConfigReady condition on its own.
	SetModelConfigCondition(ctx context.Context, status metav1.ConditionStatus, reason, message string) error
	// SaveResolved records the resolved model and a ModelConfigReady=True
	// condition together, so a reader never observes one without the other.
	// It reports whether the persisted condition became ready, as decided by
	// SetResolvedCondition against the freshly read status.
	SaveResolved(ctx context.Context, record *kaitov1beta1.ResolvedModel, reason, message string) (becameReady bool, err error)
}

// SetResolvedCondition sets ModelConfigReady=True with message on conditions
// and reports whether it became ready: it was absent, not True, or True for a
// different message, i.e. a different configuration. StatusWriter
// implementations call it on the status they are about to persist.
func SetResolvedCondition(conditions *[]metav1.Condition, reason, message string) (becameReady bool) {
	conditionType := string(kaitov1beta1.WorkspaceConditionTypeModelConfigReady)
	current := meta.FindStatusCondition(*conditions, conditionType)
	becameReady = current == nil || current.Status != metav1.ConditionTrue || current.Message != message
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:    conditionType,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	})
	return becameReady
}

// Reconcile resolves the bring-your-own model referenced by inferenceSpec and
// records the outcome through writer, returning the recorded model so the
// caller can mirror it onto its in-memory object.
//
// It is the shared body of the Workspace and InferenceSet reconcilers. Both
// must agree exactly on what a model resolves to and on the condition they
// report — an InferenceSet creates Workspaces, and a divergence would surface
// as replicas that disagree about what they are serving — so the resolution,
// reason selection, logging and message live here once. Only status
// persistence, which is typed per controller, is delegated to writer.
//
// A Warning event for an unmatched reference model is emitted through
// recorder, which may be nil, only after a save in which ModelConfigReady
// became ready. It therefore fires once per configuration, and again only if
// the condition genuinely recovers from False. Workspaces owned by an
// InferenceSet do not emit it; their InferenceSet does.
//
// For a non-custom spec it is a no-op returning (nil, nil).
func Reconcile(ctx context.Context, kubeClient client.Client, inferenceSpec *kaitov1beta1.InferenceSpec,
	namespace string, recorded *kaitov1beta1.ResolvedModel, obj client.Object,
	recorder record.EventRecorder, writer StatusWriter,
) (*kaitov1beta1.ResolvedModel, error) {
	if !IsCustom(inferenceSpec) {
		return nil, nil
	}

	statusRecord, custom, err := resolve(ctx, kubeClient, inferenceSpec, namespace, recorded)
	if err != nil {
		reason := ReasonInvalid
		var replaced *ReplacedError
		if errors.As(err, &replaced) {
			reason = ReasonReplaced
		}
		klog.ErrorS(err, "failed to resolve custom model", "object", klog.KObj(obj), "reason", reason)
		if condErr := writer.SetModelConfigCondition(ctx, metav1.ConditionFalse, reason, err.Error()); condErr != nil {
			return nil, condErr
		}
		return nil, err
	}

	message := fmt.Sprintf("resolved model from ConfigMap %q (%d bytes)", inferenceSpec.Config, statusRecord.SizeBytes)
	becameReady, err := writer.SaveResolved(ctx, statusRecord, ReasonResolved, message)
	if err != nil {
		return nil, fmt.Errorf("failed to record resolved model: %w", err)
	}
	if becameReady && recorder != nil && !custom.ReferenceModelMatched && custom.ReferenceModelID != "" && !ownedByInferenceSet(obj) {
		recorder.Eventf(obj, corev1.EventTypeWarning, ReasonReferenceModelUnmatched,
			"reference model %q in ConfigMap %q matches no known model family and is not in the model catalog; "+
				"tool-call and reasoning parsers, chat template and vLLM backends fall back to defaults derived from config.json",
			custom.ReferenceModelID, inferenceSpec.Config)
	}
	return statusRecord, nil
}

func ownedByInferenceSet(obj client.Object) bool {
	owner := metav1.GetControllerOf(obj)
	return owner != nil && owner.Kind == "InferenceSet"
}
