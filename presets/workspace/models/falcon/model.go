// Copyright (c) Microsoft Corporation.
// Licensed under the MIT license.
package falcon

import (
	"time"

	kaitov1alpha1 "github.com/kaito-project/kaito/api/v1alpha1"
	"github.com/kaito-project/kaito/pkg/model"
	"github.com/kaito-project/kaito/pkg/utils/plugin"
	"github.com/kaito-project/kaito/pkg/workspace/inference"
	"github.com/kaito-project/kaito/pkg/workspace/tuning"
)

func init() {
	plugin.KaitoModelRegister.Register(&plugin.Registration{
		Name:     PresetFalcon7BModel,
		Instance: &falconA,
	})
	plugin.KaitoModelRegister.Register(&plugin.Registration{
		Name:     PresetFalcon7BInstructModel,
		Instance: &falconB,
	})
	plugin.KaitoModelRegister.Register(&plugin.Registration{
		Name:     PresetFalcon40BModel,
		Instance: &falconC,
	})
	plugin.KaitoModelRegister.Register(&plugin.Registration{
		Name:     PresetFalcon40BInstructModel,
		Instance: &falconD,
	})
}

var (
	PresetFalcon7BModel          = "falcon-7b"
	PresetFalcon40BModel         = "falcon-40b"
	PresetFalcon7BInstructModel  = PresetFalcon7BModel + "-instruct"
	PresetFalcon40BInstructModel = PresetFalcon40BModel + "-instruct"

	PresetFalconTagMap = map[string]string{
		"Falcon7B":          "0.0.7",
		"Falcon7BInstruct":  "0.0.7",
		"Falcon40B":         "0.0.8",
		"Falcon40BInstruct": "0.0.8",
	}

	baseCommandPresetFalconInference = "accelerate launch"
	baseCommandPresetFalconTuning    = "cd /workspace/tfs/ && python3 metrics_server.py & accelerate launch"
	falconRunParams                  = map[string]string{
		"torch_dtype":   "bfloat16",
		"pipeline":      "text-generation",
		"chat_template": "/workspace/chat_templates/falcon-instruct.jinja",
	}
	falconRunParamsVLLM = map[string]string{
		"dtype":         "float16",
		"chat-template": "/workspace/chat_templates/falcon-instruct.jinja",
	}
)

var falconA falcon7b

type falcon7b struct{}

func (*falcon7b) GetInferenceParameters() *model.PresetParam {
	return &model.PresetParam{
		ModelFamilyName:           "Falcon",
		ImageAccessMode:           string(kaitov1alpha1.ModelImageAccessModePublic),
		DiskStorageRequirement:    "50Gi",
		GPUCountRequirement:       "1",
		TotalGPUMemoryRequirement: "14Gi",
		PerGPUMemoryRequirement:   "0Gi", // We run Falcon using native vertical model parallel, no per GPU memory requirement.
		RuntimeParam: model.RuntimeParam{
			Transformers: model.HuggingfaceTransformersParam{
				BaseCommand:       baseCommandPresetFalconInference,
				TorchRunParams:    inference.DefaultAccelerateParams,
				InferenceMainFile: inference.DefautTransformersMainFile,
				ModelRunParams:    falconRunParams,
			},
			VLLM: model.VLLMParam{
				BaseCommand:    inference.DefaultVLLMCommand,
				ModelName:      "falcon-7b",
				ModelRunParams: falconRunParamsVLLM,
			},
			// vllm requires the model specification to be exactly divisible by
			// the number of GPUs(tensor parallel level).
			// falcon-7b have 71 attention heads, which is a prime number.
			// So, give up tensor parallel inference.
			DisableTensorParallelism: true,
		},
		ReadinessTimeout: time.Duration(30) * time.Minute,
		Tag:              PresetFalconTagMap["Falcon7B"],
	}
}
func (*falcon7b) GetTuningParameters() *model.PresetParam {
	return &model.PresetParam{
		ModelFamilyName:           "Falcon",
		ImageAccessMode:           string(kaitov1alpha1.ModelImageAccessModePublic),
		DiskStorageRequirement:    "50Gi",
		GPUCountRequirement:       "1",
		TotalGPUMemoryRequirement: "16Gi",
		PerGPUMemoryRequirement:   "16Gi",
		RuntimeParam: model.RuntimeParam{
			Transformers: model.HuggingfaceTransformersParam{
				BaseCommand:    baseCommandPresetFalconTuning,
				TorchRunParams: tuning.DefaultAccelerateParams,
				//ModelRunPrams:             falconRunTuningParams, // TODO
			},
		},
		ReadinessTimeout:              time.Duration(30) * time.Minute,
		Tag:                           PresetFalconTagMap["Falcon7B"],
		TuningPerGPUMemoryRequirement: map[string]int{"qlora": 16},
	}
}

func (*falcon7b) SupportDistributedInference() bool {
	return false
}
func (*falcon7b) SupportTuning() bool {
	return true
}

var falconB falcon7bInst

type falcon7bInst struct{}

func (*falcon7bInst) GetInferenceParameters() *model.PresetParam {
	return &model.PresetParam{
		ModelFamilyName:           "Falcon",
		ImageAccessMode:           string(kaitov1alpha1.ModelImageAccessModePublic),
		DiskStorageRequirement:    "50Gi",
		GPUCountRequirement:       "1",
		TotalGPUMemoryRequirement: "14Gi",
		PerGPUMemoryRequirement:   "0Gi", // We run Falcon using native vertical model parallel, no per GPU memory requirement.
		RuntimeParam: model.RuntimeParam{
			Transformers: model.HuggingfaceTransformersParam{
				BaseCommand:       baseCommandPresetFalconInference,
				TorchRunParams:    inference.DefaultAccelerateParams,
				InferenceMainFile: inference.DefautTransformersMainFile,
				ModelRunParams:    falconRunParams,
			},
			VLLM: model.VLLMParam{
				BaseCommand:    inference.DefaultVLLMCommand,
				ModelName:      "falcon-7b-instruct",
				ModelRunParams: falconRunParamsVLLM,
			},
			// vllm requires the model specification to be exactly divisible by
			// the number of GPUs(tensor parallel level).
			// falcon-7b-instruct have 71 attention heads, which is a prime number.
			// So, give up tensor parallel inference.
			DisableTensorParallelism: true,
		},
		ReadinessTimeout: time.Duration(30) * time.Minute,
		Tag:              PresetFalconTagMap["Falcon7BInstruct"],
	}

}
func (*falcon7bInst) GetTuningParameters() *model.PresetParam {
	return nil // It is not recommended/ideal to further fine-tune instruct models - Already been fine-tuned
}
func (*falcon7bInst) SupportDistributedInference() bool {
	return false
}
func (*falcon7bInst) SupportTuning() bool {
	return false
}

var falconC falcon40b

type falcon40b struct{}

func (*falcon40b) GetInferenceParameters() *model.PresetParam {
	return &model.PresetParam{
		ModelFamilyName:           "Falcon",
		ImageAccessMode:           string(kaitov1alpha1.ModelImageAccessModePublic),
		DiskStorageRequirement:    "400",
		GPUCountRequirement:       "2",
		TotalGPUMemoryRequirement: "90Gi",
		PerGPUMemoryRequirement:   "0Gi", // We run Falcon using native vertical model parallel, no per GPU memory requirement.
		RuntimeParam: model.RuntimeParam{
			Transformers: model.HuggingfaceTransformersParam{
				BaseCommand:       baseCommandPresetFalconInference,
				TorchRunParams:    inference.DefaultAccelerateParams,
				InferenceMainFile: inference.DefautTransformersMainFile,
				ModelRunParams:    falconRunParams,
			},
			VLLM: model.VLLMParam{
				BaseCommand:    inference.DefaultVLLMCommand,
				ModelName:      "falcon-40b",
				ModelRunParams: falconRunParamsVLLM,
			},
		},
		ReadinessTimeout: time.Duration(30) * time.Minute,
		Tag:              PresetFalconTagMap["Falcon40B"],
	}
}
func (*falcon40b) GetTuningParameters() *model.PresetParam {
	return &model.PresetParam{
		ModelFamilyName:           "Falcon",
		ImageAccessMode:           string(kaitov1alpha1.ModelImageAccessModePublic),
		DiskStorageRequirement:    "50Gi",
		GPUCountRequirement:       "2",
		TotalGPUMemoryRequirement: "90Gi",
		PerGPUMemoryRequirement:   "16Gi",
		RuntimeParam: model.RuntimeParam{
			Transformers: model.HuggingfaceTransformersParam{
				BaseCommand:    baseCommandPresetFalconTuning,
				TorchRunParams: tuning.DefaultAccelerateParams,
				//ModelRunPrams:             falconRunTuningParams, // TODO
			},
		},
		ReadinessTimeout: time.Duration(30) * time.Minute,
		Tag:              PresetFalconTagMap["Falcon40B"],
	}
}
func (*falcon40b) SupportDistributedInference() bool {
	return false
}
func (*falcon40b) SupportTuning() bool {
	return true
}

var falconD falcon40bInst

type falcon40bInst struct{}

func (*falcon40bInst) GetInferenceParameters() *model.PresetParam {
	return &model.PresetParam{
		ModelFamilyName:           "Falcon",
		ImageAccessMode:           string(kaitov1alpha1.ModelImageAccessModePublic),
		DiskStorageRequirement:    "400",
		GPUCountRequirement:       "2",
		TotalGPUMemoryRequirement: "90Gi",
		PerGPUMemoryRequirement:   "0Gi", // We run Falcon using native vertical model parallel, no per GPU memory requirement.
		RuntimeParam: model.RuntimeParam{
			Transformers: model.HuggingfaceTransformersParam{
				BaseCommand:       baseCommandPresetFalconInference,
				TorchRunParams:    inference.DefaultAccelerateParams,
				InferenceMainFile: inference.DefautTransformersMainFile,
				ModelRunParams:    falconRunParams,
			},
			VLLM: model.VLLMParam{
				BaseCommand:    inference.DefaultVLLMCommand,
				ModelName:      "falcon-40b-instruct",
				ModelRunParams: falconRunParamsVLLM,
			},
		},
		ReadinessTimeout: time.Duration(30) * time.Minute,
		Tag:              PresetFalconTagMap["Falcon40BInstruct"],
	}
}
func (*falcon40bInst) GetTuningParameters() *model.PresetParam {
	return nil // It is not recommended/ideal to further fine-tune instruct models - Already been fine-tuned
}
func (*falcon40bInst) SupportDistributedInference() bool {
	return false
}
func (*falcon40bInst) SupportTuning() bool {
	return false
}