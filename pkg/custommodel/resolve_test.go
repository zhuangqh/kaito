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

package custommodel

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kaitov1beta1 "github.com/kaito-project/kaito/api/v1beta1"
	"github.com/kaito-project/kaito/pkg/utils/plugin"
	"github.com/kaito-project/kaito/presets/workspace/models"
)

const testConfigJSON = `{
	"architectures": ["LlamaForCausalLM"],
	"hidden_size": 4096,
	"intermediate_size": 14336,
	"num_hidden_layers": 32,
	"num_attention_heads": 32,
	"num_key_value_heads": 8,
	"max_position_embeddings": 131072,
	"vocab_size": 128256,
	"torch_dtype": "bfloat16"
}`

type fakeStatusWriter struct {
	becameReady bool
	saveErr     error
}

func (w *fakeStatusWriter) SetModelConfigCondition(context.Context, metav1.ConditionStatus, string, string) error {
	return nil
}

func (w *fakeStatusWriter) SaveResolved(context.Context, *kaitov1beta1.ResolvedModel, string, string) (bool, error) {
	return w.becameReady, w.saveErr
}

func reconcileUnmatchedReference(t *testing.T, writer StatusWriter) (*record.FakeRecorder, error) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "byo-config", Namespace: "default"},
		Immutable:  ptr.To(true),
		Data: map[string]string{
			models.CustomModelConfigKey:           testConfigJSON,
			models.CustomModelSizeKey:             "16060522496",
			models.CustomModelReferenceModelIDKey: "acme/qwn3-8b",
		},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()
	spec := &kaitov1beta1.InferenceSpec{
		Preset: &kaitov1beta1.PresetSpec{PresetMeta: kaitov1beta1.PresetMeta{Name: plugin.PresetNameCustom}},
		Config: cm.Name,
	}
	ws := &kaitov1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "ws", Namespace: "default"}}
	recorder := record.NewFakeRecorder(10)
	_, err := Reconcile(context.Background(), kubeClient, spec, "default", nil, ws, recorder, writer)
	return recorder, err
}

func TestReconcileWarnsOnlyWhenSaveMadeConditionReady(t *testing.T) {
	recorder, err := reconcileUnmatchedReference(t, &fakeStatusWriter{becameReady: true})
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	assert.Contains(t, <-recorder.Events, ReasonReferenceModelUnmatched)

	recorder, err = reconcileUnmatchedReference(t, &fakeStatusWriter{becameReady: false})
	require.NoError(t, err)
	assert.Empty(t, recorder.Events)
}

func TestReconcileDoesNotWarnWhenSaveFails(t *testing.T) {
	recorder, err := reconcileUnmatchedReference(t, &fakeStatusWriter{becameReady: true, saveErr: errors.New("conflict")})
	require.Error(t, err)
	assert.Empty(t, recorder.Events)
}

func TestSetResolvedCondition(t *testing.T) {
	var conditions []metav1.Condition
	assert.True(t, SetResolvedCondition(&conditions, ReasonResolved, "a"), "absent")
	assert.False(t, SetResolvedCondition(&conditions, ReasonResolved, "a"), "unchanged")
	assert.True(t, SetResolvedCondition(&conditions, ReasonResolved, "b"), "new configuration")

	meta.SetStatusCondition(&conditions, metav1.Condition{
		Type:   string(kaitov1beta1.WorkspaceConditionTypeModelConfigReady),
		Status: metav1.ConditionFalse, Reason: ReasonInvalid, Message: "b",
	})
	assert.True(t, SetResolvedCondition(&conditions, ReasonResolved, "b"), "recovered from False")
}
