# Copyright (c) KAITO authors.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Unit tests for _InflightRequestTimesCollector in inference_api.py."""

import sys
from pathlib import Path
from types import ModuleType, SimpleNamespace
from unittest.mock import MagicMock

import pytest


# Stub dependencies not available in the CI test environment. Mirrors the
# pattern in test_download_monitor.py; setdefault preserves real modules when
# they are already loaded (e.g., in a full vLLM install).
class _StubBaseHTTPMiddleware:
    def __init__(self, app=None):
        self.app = app


class _StubJSONResponse:
    def __init__(self, status_code, content):
        self.status_code = status_code
        self.content = content


_starlette_base = ModuleType("starlette.middleware.base")
_starlette_base.BaseHTTPMiddleware = _StubBaseHTTPMiddleware
_starlette_responses = ModuleType("starlette.responses")
_starlette_responses.JSONResponse = _StubJSONResponse

for _name, _mod in {
    "vllm": MagicMock(),
    "vllm.entrypoints": MagicMock(),
    "vllm.entrypoints.openai": MagicMock(),
    "vllm.entrypoints.openai.api_server": MagicMock(),
    "vllm.entrypoints.openai.models": MagicMock(),
    "vllm.entrypoints.openai.models.protocol": MagicMock(),
    "vllm.utils": MagicMock(),
    "vllm.utils.argparse_utils": MagicMock(),
    "vllm.v1": MagicMock(),
    "vllm.v1.metrics": MagicMock(),
    "vllm.v1.metrics.prometheus": MagicMock(),
    "torch": MagicMock(),
    "uvloop": MagicMock(),
    "psutil": MagicMock(),
    "yaml": MagicMock(),
    "huggingface_hub": MagicMock(),
    "starlette": ModuleType("starlette"),
    "starlette.middleware": ModuleType("starlette.middleware"),
    "starlette.middleware.base": _starlette_base,
    "starlette.responses": _starlette_responses,
}.items():
    sys.modules.setdefault(_name, _mod)

_PARENT = str(Path(__file__).resolve().parent.parent)
if _PARENT not in sys.path:
    sys.path.insert(0, _PARENT)

import inference_api  # noqa: E402
from inference_api import _InflightRequestTimesCollector  # noqa: E402

WALL_NOW = 1_000_000.0
MONO_NOW = 5_000.0


def _stats(*, arrival_time=0.0, scheduled_ts=0.0):
    return SimpleNamespace(arrival_time=arrival_time, scheduled_ts=scheduled_ts)


def _engine_client(states):
    """Build a stub engine_client with `output_processor.request_states`."""
    request_states = {rid: SimpleNamespace(stats=s) for rid, s in states.items()}
    output_processor = SimpleNamespace(request_states=request_states)
    return SimpleNamespace(output_processor=output_processor)


def _collect(engine_client, monkeypatch):
    monkeypatch.setattr(inference_api.time, "time", lambda: WALL_NOW)
    monkeypatch.setattr(inference_api.time, "monotonic", lambda: MONO_NOW)
    families = list(_InflightRequestTimesCollector(engine_client).collect())
    return {f.name: f.samples[0].value for f in families}


class TestCollect:
    def test_empty_when_no_requests(self, monkeypatch):
        values = _collect(_engine_client({}), monkeypatch)
        for kind in ("queuing", "running"):
            for suffix in ("max", "sum", "count"):
                assert values[f"kaito_vllm_inflight_{kind}_seconds_{suffix}"] == 0.0

    def test_running_only(self, monkeypatch):
        engine = _engine_client(
            {
                "r1": _stats(arrival_time=WALL_NOW - 100, scheduled_ts=MONO_NOW - 30),
                "r2": _stats(arrival_time=WALL_NOW - 50, scheduled_ts=MONO_NOW - 10),
            }
        )
        v = _collect(engine, monkeypatch)
        assert v["kaito_vllm_inflight_running_seconds_count"] == 2
        assert v["kaito_vllm_inflight_running_seconds_max"] == 30.0
        assert v["kaito_vllm_inflight_running_seconds_sum"] == 40.0
        assert v["kaito_vllm_inflight_queuing_seconds_count"] == 0
        assert v["kaito_vllm_inflight_queuing_seconds_max"] == 0.0

    def test_queuing_only(self, monkeypatch):
        engine = _engine_client(
            {
                "w1": _stats(arrival_time=WALL_NOW - 12.5),
                "w2": _stats(arrival_time=WALL_NOW - 2.5),
            }
        )
        v = _collect(engine, monkeypatch)
        assert v["kaito_vllm_inflight_queuing_seconds_count"] == 2
        assert v["kaito_vllm_inflight_queuing_seconds_max"] == 12.5
        assert v["kaito_vllm_inflight_queuing_seconds_sum"] == 15.0
        assert v["kaito_vllm_inflight_running_seconds_count"] == 0

    def test_mixed(self, monkeypatch):
        engine = _engine_client(
            {
                "w1": _stats(arrival_time=WALL_NOW - 5),
                "r1": _stats(arrival_time=WALL_NOW - 100, scheduled_ts=MONO_NOW - 20),
            }
        )
        v = _collect(engine, monkeypatch)
        assert v["kaito_vllm_inflight_queuing_seconds_count"] == 1
        assert v["kaito_vllm_inflight_queuing_seconds_max"] == 5.0
        assert v["kaito_vllm_inflight_running_seconds_count"] == 1
        assert v["kaito_vllm_inflight_running_seconds_max"] == 20.0

    def test_scheduled_wins_over_arrival(self, monkeypatch):
        # A scheduled request must never be double-counted as queuing.
        engine = _engine_client(
            {"r1": _stats(arrival_time=WALL_NOW - 999, scheduled_ts=MONO_NOW - 1)}
        )
        v = _collect(engine, monkeypatch)
        assert v["kaito_vllm_inflight_running_seconds_count"] == 1
        assert v["kaito_vllm_inflight_queuing_seconds_count"] == 0

    def test_skips_when_stats_none(self, monkeypatch):
        # log_stats=False on vLLM produces RequestState.stats = None.
        request_states = {"r1": SimpleNamespace(stats=None)}
        engine = SimpleNamespace(
            output_processor=SimpleNamespace(request_states=request_states)
        )
        v = _collect(engine, monkeypatch)
        assert v["kaito_vllm_inflight_queuing_seconds_count"] == 0
        assert v["kaito_vllm_inflight_running_seconds_count"] == 0

    def test_skips_when_arrival_time_zero(self, monkeypatch):
        # Freshly-added request before frontend admission recorded time.
        engine = _engine_client({"r1": _stats(arrival_time=0.0, scheduled_ts=0.0)})
        v = _collect(engine, monkeypatch)
        assert v["kaito_vllm_inflight_queuing_seconds_count"] == 0
        assert v["kaito_vllm_inflight_running_seconds_count"] == 0

    @pytest.mark.parametrize(
        "engine",
        [
            SimpleNamespace(),  # no output_processor
            SimpleNamespace(output_processor=None),  # output_processor is None
            SimpleNamespace(output_processor=SimpleNamespace()),  # no request_states
        ],
    )
    def test_missing_internal_attrs_degrades_to_zero(self, engine, monkeypatch):
        v = _collect(engine, monkeypatch)
        for kind in ("queuing", "running"):
            assert v[f"kaito_vllm_inflight_{kind}_seconds_count"] == 0


class TestEmittedMetadata:
    def test_metric_family_names(self, monkeypatch):
        monkeypatch.setattr(inference_api.time, "time", lambda: WALL_NOW)
        monkeypatch.setattr(inference_api.time, "monotonic", lambda: MONO_NOW)
        families = list(_InflightRequestTimesCollector(_engine_client({})).collect())
        names = [f.name for f in families]
        assert names == [
            "kaito_vllm_inflight_queuing_seconds_max",
            "kaito_vllm_inflight_queuing_seconds_sum",
            "kaito_vllm_inflight_queuing_seconds_count",
            "kaito_vllm_inflight_running_seconds_max",
            "kaito_vllm_inflight_running_seconds_sum",
            "kaito_vllm_inflight_running_seconds_count",
        ]
        for f in families:
            assert f.type == "gauge"
            assert f.documentation
