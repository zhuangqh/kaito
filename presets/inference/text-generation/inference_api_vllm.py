# Copyright (c) Microsoft Corporation.
# Licensed under the MIT license.
import logging
import os
from dataclasses import asdict, dataclass, field
from typing import Annotated, Any, Dict, List, Optional

import GPUtil
import psutil
import torch
import uvicorn
from fastapi import Body, FastAPI, HTTPException
from fastapi.responses import Response
from peft import PeftModel
from pydantic import BaseModel, Extra, Field, validator

from vllm import EngineArgs, LLM, SamplingParams
from vllm.utils import FlexibleArgumentParser

# Initialize logger
logger = logging.getLogger(__name__)
debug_mode = os.environ.get('DEBUG_MODE', 'false').lower() == 'true'
logging.basicConfig(level=logging.DEBUG if debug_mode else logging.INFO)

# https://github.com/vllm-project/vllm/blob/main/vllm/engine/arg_utils.py#L82
parser = FlexibleArgumentParser(
        description='Demo on using the LLMEngine class directly')
parser = EngineArgs.add_cli_args(parser)
parser.set_defaults(model="/workspace/tfs/weights",
                    dtype="float16")
args = parser.parse_args()


# Initialize the vLLMEngine from the command line arguments
engine_args = EngineArgs.from_cli_args(args)
model_args = asdict(engine_args)
print(model_args)
model = LLM(**model_args)

app = FastAPI()

logger.info("Model loaded successfully")
logger.info("Model: %s", model)

class HomeResponse(BaseModel):
    message: str = Field(..., example="Server is running")
@app.get('/', response_model=HomeResponse, summary="Home Endpoint")
def home():
    """
    A simple endpoint that indicates the server is running.
    No parameters are required. Returns a message indicating the server status.
    """
    return {"message": "Server is running"}

class HealthStatus(BaseModel):
    status: str = Field(..., example="Healthy")
@app.get(
    "/healthz",
    response_model=HealthStatus,
    summary="Health Check Endpoint",
    responses={
        200: {
            "description": "Successful Response",
            "content": {
                "application/json": {
                    "example": {"status": "Healthy"}
                }
            }
        },
        500: {
            "description": "Error Response",
            "content": {
                "application/json": {
                    "examples": {
                        "model_uninitialized": {
                            "summary": "Model not initialized",
                            "value": {"detail": "Model not initialized"}
                        },
                        "pipeline_uninitialized": {
                            "summary": "Pipeline not initialized",
                            "value": {"detail": "Pipeline not initialized"}
                        }
                    }
                }
            }
        }
    }
)
def health_check():
    if not model:
        logger.error("Model not initialized")
        raise HTTPException(status_code=500, detail="Model not initialized")
    return {"status": "Healthy"}

# https://github.com/vllm-project/vllm/blob/main/vllm/sampling_params.py#L94
class GenerateKwargs(BaseModel):
    max_length: int = 200 # Length of input prompt+max_new_tokens
    min_length: int = 0
    do_sample: bool = True
    early_stopping: bool = False
    num_beams: int = 1
    temperature: float = 1.0
    top_k: int = 10
    top_p: float = 1
    typical_p: float = 1
    repetition_penalty: float = 1
    # pad_token_id: Optional[int] = tokenizer.pad_token_id
    # eos_token_id: Optional[int] = tokenizer.eos_token_id
    class Config:
        extra = 'allow' # Allows for additional fields not explicitly defined
        json_schema_extra = {
            "example": {
                "max_length": 200,
                "temperature": 0.7,
                "top_p": 0.9,
                "additional_param": "Example value"
            }
        }

class Message(BaseModel):
    role: str
    content: str

class UnifiedRequestModel(BaseModel):
    # Fields for text generation
    prompt: Optional[str] = Field(None, description="Prompt for text generation. Required for text-generation pipeline. Do not use with 'messages'.")
    return_full_text: Optional[bool] = Field(True, description="Return full text if True, else only added text")
    clean_up_tokenization_spaces: Optional[bool] = Field(False, description="Clean up extra spaces in text output")
    prefix: Optional[str] = Field(None, description="Prefix added to prompt")
    handle_long_generation: Optional[str] = Field(None, description="Strategy to handle long generation")
    generate_kwargs: Optional[GenerateKwargs] = Field(default_factory=GenerateKwargs, description="Additional kwargs for generate method")

    # Field for conversational model
    messages: Optional[List[Message]] = Field(None, description="Messages for conversational model. Required for conversational pipeline. Do not use with 'prompt'.")
    def messages_to_dict_list(self):
        return [message.dict() for message in self.messages] if self.messages else []

class ErrorResponse(BaseModel):
    detail: str

# curl -X POST http://127.0.0.1:5000/chat -H "accept: application/json" -H "Content-Type: application/json" -d "{\"prompt\":\"YOUR QUESTION HERE\"}"
@app.post(
    "/chat",
    summary="Chat Endpoint",
    responses={
        200: {
            "description": "Successful Response",
            "content": {
                "application/json": {
                    "examples": {
                        "text_generation": {
                            "summary": "Text Generation Response",
                            "value": {
                                "Result": "Generated text based on the prompt."
                            }
                        },
                        "conversation": {
                            "summary": "Conversation Response",
                            "value": {
                                "Result": "Response to the last message in the conversation."
                            }
                        }
                    }
                }
            }
        },
        400: {
            "model": ErrorResponse,
            "description": "Validation Error",
            "content": {
                "application/json": {
                    "examples": {
                        "missing_prompt": {
                            "summary": "Missing Prompt",
                            "value": {"detail": "Text generation parameter prompt required"}
                        },
                        "missing_messages": {
                            "summary": "Missing Messages",
                            "value": {"detail": "Conversational parameter messages required"}
                        }
                    }
                }
            }
        },
        500: {
            "model": ErrorResponse,
            "description": "Internal Server Error"
        }
    }
)
def generate_text(
        request_model: Annotated[
            UnifiedRequestModel,
            Body(
                openapi_examples={
                    "text_generation_example": {
                        "summary": "Text Generation Example",
                        "description": "An example of a text generation request.",
                        "value": {
                            "prompt": "Tell me a joke",
                            "return_full_text": True,
                            "clean_up_tokenization_spaces": False,
                            "prefix": None,
                            "handle_long_generation": None,
                            "generate_kwargs": GenerateKwargs().dict(),
                        },
                    },
                    "conversation_example": {
                        "summary": "Conversation Example",
                        "description": "An example of a conversational request.",
                        "value": {
                            "messages": [
                                {"role": "user", "content": "What is your favourite condiment?"},
                                {"role": "assistant", "content": "Well, im quite partial to a good squeeze of fresh lemon juice. It adds just the right amount of zesty flavour to whatever im cooking up in the kitchen!"},
                                {"role": "user", "content": "Do you have mayonnaise recipes?"}
                            ],
                            "return_full_text": True,
                            "clean_up_tokenization_spaces": False,
                            "prefix": None,
                            "handle_long_generation": None,
                            "generate_kwargs": GenerateKwargs().dict(),
                        },
                    },
                },
            ),
        ],
):
    """
    Processes chat requests, generating text based on the specified pipeline (text generation or conversational).
    Validates required parameters based on the pipeline and returns the generated text.
    """

    if not request_model.prompt:
        logger.error("Text generation parameter prompt required")
        raise HTTPException(status_code=400, detail="Text generation parameter prompt required")
    sampling_params = SamplingParams(temperature=0.8, top_p=0.95)
    sequences = model.generate(request_model.prompt, sampling_params)

    result = ""
    for seq in sequences:
        logger.debug(f"Result: {seq.outputs[0].text}")
        result += seq.outputs[0].text

    return {"Result": result}


class MemoryInfo(BaseModel):
    used: str
    total: str

class CPUInfo(BaseModel):
    load_percentage: float
    physical_cores: int
    total_cores: int
    memory: MemoryInfo

class GPUInfo(BaseModel):
    id: str
    name: str
    load: str
    temperature: str
    memory: MemoryInfo

class MetricsResponse(BaseModel):
    gpu_info: Optional[List[GPUInfo]] = None
    cpu_info: Optional[CPUInfo] = None

@app.get(
    "/metrics",
    response_model=MetricsResponse,
    summary="Metrics Endpoint",
    responses={
        200: {
            "description": "Successful Response",
            "content": {
                "application/json": {
                    "examples": {
                        "gpu_metrics": {
                            "summary": "Example when GPUs are available",
                            "value": {
                                "gpu_info": [{"id": "GPU-1234", "name": "GeForce GTX 950", "load": "25.00%", "temperature": "55 C", "memory": {"used": "1.00 GB", "total": "2.00 GB"}}],
                                "cpu_info": None  # Indicates CPUs info might not be present when GPUs are available
                            }
                        },
                        "cpu_metrics": {
                            "summary": "Example when only CPU is available",
                            "value": {
                                "gpu_info": None,  # Indicates GPU info might not be present when only CPU is available
                                "cpu_info": {"load_percentage": 20.0, "physical_cores": 4, "total_cores": 8, "memory": {"used": "4.00 GB", "total": "16.00 GB"}}
                            }
                        }
                    }
                }
            }
        },
        500: {
            "description": "Internal Server Error",
            "model": ErrorResponse,
        }
    }
)
def get_metrics():
    """
    Provides system metrics, including GPU details if available, or CPU and memory usage otherwise.
    Useful for monitoring the resource utilization of the server running the ML models.
    """
    try:
        if torch.cuda.is_available():
            gpus = GPUtil.getGPUs()
            gpu_info = [GPUInfo(
                id=gpu.id,
                name=gpu.name,
                load=f"{gpu.load * 100:.2f}%",
                temperature=f"{gpu.temperature} C",
                memory=MemoryInfo(
                    used=f"{gpu.memoryUsed / (1024 ** 3):.2f} GB",
                    total=f"{gpu.memoryTotal / (1024 ** 3):.2f} GB"
                )
            ) for gpu in gpus]
            return MetricsResponse(gpu_info=gpu_info)
        else:
            # Gather CPU metrics
            cpu_usage = psutil.cpu_percent(interval=1, percpu=False)
            physical_cores = psutil.cpu_count(logical=False)
            total_cores = psutil.cpu_count(logical=True)
            virtual_memory = psutil.virtual_memory()
            memory = MemoryInfo(
                used=f"{virtual_memory.used / (1024 ** 3):.2f} GB",
                total=f"{virtual_memory.total / (1024 ** 3):.2f} GB"
            )
            cpu_info = CPUInfo(
                load_percentage=cpu_usage,
                physical_cores=physical_cores,
                total_cores=total_cores,
                memory=memory
            )
            return MetricsResponse(cpu_info=cpu_info)
    except Exception as e:
        logger.error(f"Error fetching metrics: {e}")
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    local_rank = int(os.environ.get("LOCAL_RANK", 0)) # Default to 0 if not set
    port = 5000 + local_rank # Adjust port based on local rank
    logger.info(f"Starting server on port {port}")
    uvicorn.run(app=app, host='0.0.0.0', port=port)
