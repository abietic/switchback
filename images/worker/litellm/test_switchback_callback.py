import asyncio
import json
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
import sys
import types
import unittest
from unittest import mock


class FakeCustomLogger:
    def __init__(self, **kwargs):
        self.custom_logger_options = kwargs


fake_litellm = types.ModuleType("litellm")
fake_integrations = types.ModuleType("litellm.integrations")
fake_custom_logger = types.ModuleType("litellm.integrations.custom_logger")
fake_custom_logger.CustomLogger = FakeCustomLogger
sys.modules.setdefault("litellm", fake_litellm)
sys.modules.setdefault("litellm.integrations", fake_integrations)
sys.modules.setdefault("litellm.integrations.custom_logger", fake_custom_logger)

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
os.environ["SWITCHBACK_MODEL_SINK_TOKEN"] = "sink-secret"

import switchback_callback  # noqa: E402


class FakeHTTPResponse:
    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_value, traceback):
        return False

    def read(self, size=-1):
        return b'{"status":"accepted"}'


class ProviderRateLimitError(Exception):
    code = "rate_limit"
    status_code = 429


class ThrowingDump:
    def model_dump(self):
        raise RuntimeError("model-dump-secret")

    def dict(self):
        raise RuntimeError("legacy-dict-secret")


class ThrowingNumber:
    def __int__(self):
        raise RuntimeError("integer-secret")

    def __float__(self):
        raise RuntimeError("float-secret")


class SwitchbackCallbackTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.requests = []

        def capture(request, timeout):
            self.requests.append((request, timeout))
            return FakeHTTPResponse()

        self.urlopen = mock.patch.object(
            switchback_callback.urllib.request,
            "urlopen",
            side_effect=capture,
        )
        self.urlopen.start()

    def tearDown(self):
        self.urlopen.stop()

    async def test_success_callback_maps_typed_observation_and_litellm_correlation_id(self):
        start = datetime(2026, 7, 17, 1, 2, 3, tzinfo=timezone.utc)
        end = start + timedelta(milliseconds=1500)
        kwargs = {
            "litellm_call_id": "litellm-call-1",
            "model": "gpt-5.2-codex",
            "response_cost": 0.0042,
            "proxy_server_request": {
                "headers": {
                    "X-Request-ID": "request-1",
                    "X-Switchback-Trace-ID": "trace-1",
                    "X-Switchback-Worker-Session-ID": "session-1",
                    "X-Switchback-Worker-Run-ID": "run-1",
                    "X-Switchback-Agent-Turn-ID": "turn-1",
                    "Authorization": "must-not-be-forwarded",
                },
                "body": {
                    "model": "switchback-codex",
                    "messages": [
                        {"role": "user", "content": "complete-model-input"}
                    ],
                    "tools": [{"type": "function", "name": "exec_command"}],
                },
            },
            "metadata": {"model_group": "switchback-codex"},
        }
        response = {
            "id": "response-1",
            "choices": [
                {
                    "message": {
                        "role": "assistant",
                        "content": "complete-model-output",
                        "reasoning_content": "complete-reasoning-output",
                    }
                }
            ],
            "usage": {
                "prompt_tokens": 12,
                "completion_tokens": 7,
                "total_tokens": 19,
            },
        }

        await switchback_callback.switchback_callback.async_log_success_event(
            kwargs,
            response,
            start,
            end,
        )

        payload, request = self.assert_single_request()
        self.assertEqual(
            payload,
            {
                "model": "gpt-5.2-codex",
                "route": "switchback-codex",
                "started_at": "2026-07-17T01:02:03+00:00",
                "ended_at": "2026-07-17T01:02:04.500000+00:00",
                "latency_ms": 1500.0,
                "usage": {
                    "input_tokens": 12,
                    "output_tokens": 7,
                    "total_tokens": 19,
                },
                "cost_usd": 0.0042,
                "error": "",
                "input": {
                    "model": "switchback-codex",
                    "messages": [
                        {"role": "user", "content": "complete-model-input"}
                    ],
                    "tools": [{"type": "function", "name": "exec_command"}],
                },
                "output": response,
                "correlation": {
                    "request_id": "litellm-call-1",
                },
            },
        )
        self.assertNotIn("must-not-be-forwarded", json.dumps(payload))
        self.assertEqual(request.full_url, "http://127.0.0.1:7777/")
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.get_header("Content-type"), "application/json")
        self.assertEqual(
            request.get_header("X-switchback-model-sink-token"),
            "sink-secret",
        )

    async def test_success_callback_reads_complete_request_from_nested_litellm_params(self):
        request_body = {
            "model": "switchback-codex",
            "input": [{"role": "user", "content": "nested-complete-input"}],
            "tools": [{"type": "function", "name": "exec_command"}],
        }
        kwargs = {
            "litellm_call_id": "nested-request-call",
            "model": "deepseek/deepseek-chat",
            "litellm_params": {
                "litellm_call_id": "nested-request-call",
                "proxy_server_request": {
                    "headers": {"Authorization": "must-not-be-forwarded"},
                    "body": request_body,
                },
            },
        }

        await switchback_callback.switchback_callback.async_log_success_event(
            kwargs,
            {"id": "nested-response", "output": [{"type": "message"}]},
            datetime(2026, 7, 24, tzinfo=timezone.utc),
            datetime(2026, 7, 24, 0, 0, 1, tzinfo=timezone.utc),
        )

        payload, _ = self.assert_single_request()
        self.assertEqual(payload["input"], request_body)
        self.assertNotIn("must-not-be-forwarded", json.dumps(payload))

    async def test_success_callback_falls_back_to_litellm_exact_input(self):
        exact_input = [
            {"role": "user", "content": "fallback-complete-input"},
            {
                "type": "function_call",
                "name": "exec_command",
                "arguments": '{"cmd":"pwd"}',
            },
        ]
        kwargs = {
            "litellm_call_id": "exact-input-call",
            "model": "deepseek/deepseek-chat",
            "input": exact_input,
        }

        await switchback_callback.switchback_callback.async_log_success_event(
            kwargs,
            {"id": "fallback-response", "output_text": "done"},
            datetime(2026, 7, 24, tzinfo=timezone.utc),
            datetime(2026, 7, 24, 0, 0, 1, tzinfo=timezone.utc),
        )

        payload, _ = self.assert_single_request()
        self.assertEqual(payload["input"], exact_input)

    async def test_pre_call_binds_litellm_identity_before_a_late_callback_without_switchback_headers(self):
        start = datetime(2026, 7, 20, 1, 2, 3, tzinfo=timezone.utc)
        first_request = {
            "litellm_call_id": "litellm-call-turn-1",
            "model": "gpt-5.2-codex",
            "proxy_server_request": {
                "headers": {"Authorization": "gateway-token"},
                "body": {"model": "switchback-codex"},
            },
        }
        second_request = {
            "litellm_call_id": "litellm-call-turn-2",
            "model": "gpt-5.2-codex",
            "proxy_server_request": {"headers": {}, "body": {"model": "switchback-codex"}},
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, first_request, "responses"
        )
        # The next Turn can begin before LiteLLM schedules Turn 1's callback.
        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, second_request, "responses"
        )
        await switchback_callback.switchback_callback.async_log_success_event(
            first_request, None, start, start + timedelta(milliseconds=25)
        )

        self.assertEqual(len(self.requests), 3)
        bind_one = self.assert_request(0)
        bind_two = self.assert_request(1)
        observation = self.assert_request(2)
        self.assertEqual(bind_one[0], "http://127.0.0.1:7777/bind")
        self.assertEqual(bind_one[1], {"litellm_call_id": "litellm-call-turn-1"})
        self.assertEqual(bind_two[1], {"litellm_call_id": "litellm-call-turn-2"})
        self.assertEqual(observation[0], "http://127.0.0.1:7777/")
        self.assertEqual(
            observation[1]["correlation"],
            {"request_id": "litellm-call-turn-1"},
        )
        self.assertNotIn("x-switchback-agent-turn-id", json.dumps(observation[1]))
        self.assertNotIn("gateway-token", json.dumps(observation[1]))

    async def test_pre_call_removes_only_empty_assistant_message_between_matching_tool_events(self):
        function_call = {
            "type": "function_call",
            "call_id": "call-1",
            "name": "exec_command",
            "arguments": '{"cmd":"ls"}',
        }
        empty_assistant = {
            "type": "message",
            "role": "assistant",
            "content": [{"type": "output_text", "text": ""}],
        }
        function_output = {
            "type": "function_call_output",
            "call_id": "call-1",
            "output": "counter\ngo.mod\n",
        }
        meaningful_assistant = {
            "type": "message",
            "role": "assistant",
            "content": [{"type": "output_text", "text": "keep me"}],
        }
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "input": [
                function_call,
                empty_assistant,
                function_output,
                meaningful_assistant,
            ],
        }

        result = await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "responses"
        )

        self.assertIs(result, data)
        self.assertEqual(
            data["input"],
            [function_call, function_output, meaningful_assistant],
        )

    async def test_pre_call_removes_empty_assistant_between_matching_tool_batches(self):
        calls = [
            {
                "type": "function_call",
                "call_id": f"call-{index}",
                "name": "exec_command",
                "arguments": '{"cmd":"ls"}',
            }
            for index in (1, 2)
        ]
        empty_assistant = {
            "type": "message",
            "role": "assistant",
            "content": [{"type": "output_text", "text": ""}],
        }
        outputs = [
            {
                "type": "function_call_output",
                "call_id": f"call-{index}",
                "output": "ok",
            }
            for index in (1, 2)
        ]
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "input": [*calls, empty_assistant, *outputs],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "responses"
        )

        self.assertEqual(data["input"], [*calls, *outputs])

    async def test_pre_call_normalizes_responses_input_for_async_call_type(self):
        function_call = {
            "type": "function_call",
            "call_id": "call-1",
            "name": "exec_command",
            "arguments": '{"cmd":"ls"}',
        }
        empty_assistant = {
            "type": "message",
            "role": "assistant",
            "content": [{"type": "output_text", "text": ""}],
        }
        function_output = {
            "type": "function_call_output",
            "call_id": "call-1",
            "output": "ok",
        }
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "input": [function_call, empty_assistant, function_output],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "aresponses"
        )

        self.assertEqual(data["input"], [function_call, function_output])

    async def test_pre_call_moves_meaningful_assistant_before_responses_tool_batch(self):
        function_call = {
            "type": "function_call",
            "call_id": "call-1",
            "name": "update_plan",
            "arguments": "{}",
        }
        assistant_message = {
            "type": "message",
            "role": "assistant",
            "content": [{"type": "output_text", "text": "Updating the plan."}],
        }
        function_output = {
            "type": "function_call_output",
            "call_id": "call-1",
            "output": "ok",
        }
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "input": [function_call, assistant_message, function_output],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "aresponses"
        )

        self.assertEqual(
            data["input"],
            [assistant_message, function_call, function_output],
        )

    async def test_pre_call_normalizes_mixed_function_and_custom_tool_batch(self):
        calls = [
            {
                "type": "function_call",
                "call_id": "call-function",
                "name": "exec_command",
                "arguments": "{}",
            },
            {
                "type": "custom_tool_call",
                "id": "call-custom",
                "name": "apply_patch",
                "input": "*** Begin Patch\n*** End Patch",
            },
        ]
        assistant_message = {
            "type": "message",
            "role": "assistant",
            "content": [{"type": "output_text", "text": "Applying changes."}],
        }
        outputs = [
            {
                "type": "function_call_output",
                "call_id": "call-function",
                "output": "ok",
            },
            {
                "type": "custom_tool_call_output",
                "call_id": "call-custom",
                "output": "ok",
            },
        ]
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "input": [*calls, assistant_message, *outputs],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "aresponses"
        )

        self.assertEqual(data["input"], [assistant_message, *calls, *outputs])

    async def test_pre_call_leaves_mismatched_responses_tool_batch_unchanged(self):
        original = [
            {
                "type": "function_call",
                "call_id": "call-1",
                "name": "exec_command",
                "arguments": "{}",
            },
            {
                "type": "message",
                "role": "assistant",
                "content": [{"type": "output_text", "text": ""}],
            },
            {
                "type": "function_call_output",
                "call_id": "different-call",
                "output": "ok",
            },
        ]
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "input": list(original),
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "aresponses"
        )

        self.assertEqual(data["input"], original)

    async def test_pre_call_removes_empty_chat_assistant_between_matching_tool_messages(self):
        tool_call = {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "exec_command", "arguments": '{"cmd":"ls"}'},
                }
            ],
        }
        empty_assistant = {"role": "assistant", "content": ""}
        tool_output = {
            "role": "tool",
            "tool_call_id": "call-1",
            "content": "counter\ngo.mod\n",
        }
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "messages": [tool_call, empty_assistant, tool_output],
        }

        result = await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "acompletion"
        )

        self.assertIs(result, data)
        self.assertEqual(data["messages"], [tool_call, tool_output])

    async def test_pre_call_removes_empty_chat_assistant_before_complete_tool_batch(self):
        tool_call = {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": f"call-{index}",
                    "type": "function",
                    "function": {"name": "exec_command", "arguments": '{"cmd":"ls"}'},
                }
                for index in (1, 2)
            ],
        }
        empty_assistant = {"role": "assistant", "content": ""}
        tool_outputs = [
            {
                "role": "tool",
                "tool_call_id": f"call-{index}",
                "content": "ok",
            }
            for index in (1, 2)
        ]
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "messages": [tool_call, empty_assistant, *tool_outputs],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "acompletion"
        )

        self.assertEqual(data["messages"], [tool_call, *tool_outputs])

    async def test_pre_call_moves_meaningful_chat_assistant_before_tool_batch(self):
        tool_call = {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "update_plan", "arguments": "{}"},
                }
            ],
        }
        assistant_message = {"role": "assistant", "content": "Updating the plan."}
        tool_output = {
            "role": "tool",
            "tool_call_id": "call-1",
            "content": "ok",
        }
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "messages": [tool_call, assistant_message, tool_output],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "acompletion"
        )

        self.assertEqual(
            data["messages"],
            [assistant_message, tool_call, tool_output],
        )

    async def test_pre_call_preserves_chat_reasoning_before_tool_batch(self):
        tool_call = {
            "role": "assistant",
            "content": None,
            "tool_calls": [
                {
                    "id": "call-1",
                    "type": "function",
                    "function": {"name": "exec_command", "arguments": "{}"},
                }
            ],
        }
        reasoning = {
            "role": "assistant",
            "content": None,
            "reasoning_content": "The command output determines the next step.",
        }
        tool_output = {
            "role": "tool",
            "tool_call_id": "call-1",
            "content": "ok",
        }
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "messages": [tool_call, reasoning, tool_output],
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "acompletion"
        )

        self.assertEqual(data["messages"], [reasoning, tool_call, tool_output])

    async def test_pre_call_leaves_non_string_tool_ids_unchanged(self):
        original = [
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [
                    {
                        "id": {"unexpected": "id"},
                        "type": "function",
                        "function": {"name": "exec_command", "arguments": "{}"},
                    }
                ],
            },
            {"role": "assistant", "content": "keep me"},
            {
                "role": "tool",
                "tool_call_id": {"unexpected": "id"},
                "content": "ok",
            },
        ]
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "messages": list(original),
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "acompletion"
        )

        self.assertEqual(data["messages"], original)

    async def test_pre_call_leaves_incomplete_chat_tool_batch_unchanged(self):
        original = [
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [
                    {
                        "id": f"call-{index}",
                        "type": "function",
                        "function": {"name": "exec_command", "arguments": "{}"},
                    }
                    for index in (1, 2)
                ],
            },
            {"role": "assistant", "content": ""},
            {"role": "tool", "tool_call_id": "call-1", "content": "ok"},
        ]
        data = {
            "litellm_call_id": "litellm-call-turn-1",
            "messages": list(original),
        }

        await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, data, "acompletion"
        )

        self.assertEqual(data["messages"], original)

    async def test_pre_call_rejects_missing_or_unbound_litellm_identity(self):
        missing = await switchback_callback.switchback_callback.async_pre_call_hook(
            None, None, {"model": "gpt-5.2-codex"}, "responses"
        )
        self.assertEqual(missing, "model observation correlation rejected")

        with mock.patch.object(
            switchback_callback.urllib.request,
            "urlopen",
            side_effect=OSError("sink rejected the binding"),
        ):
            rejected = await switchback_callback.switchback_callback.async_pre_call_hook(
                None,
                None,
                {"litellm_call_id": "unbound-call", "model": "gpt-5.2-codex"},
                "responses",
            )
        self.assertEqual(rejected, "model observation correlation rejected")

    async def test_pre_call_rejects_conflicting_litellm_identity_without_binding(self):
        rejected = await switchback_callback.switchback_callback.async_pre_call_hook(
            None,
            None,
            {
                "litellm_call_id": "top-level-call",
                "litellm_params": {"litellm_call_id": "nested-call"},
            },
            "responses",
        )

        self.assertEqual(rejected, "model observation correlation rejected")
        self.assertEqual(self.requests, [])

    async def test_failure_callback_maps_error_without_raising_on_sink_failure(self):
        start = datetime(2026, 7, 17, 2, 0, 0, tzinfo=timezone.utc)
        end = start + timedelta(milliseconds=250)
        kwargs = {
            "litellm_call_id": "litellm-call-2",
            "model": "gpt-5.2-codex",
            "exception": ProviderRateLimitError(
                "provider-message-must-not-be-forwarded"
            ),
            "proxy_server_request": {
                "headers": {"x-request-id": "request-2"},
                "body": {"model": "switchback-codex"},
            },
        }

        await switchback_callback.switchback_callback.async_log_failure_event(
            kwargs,
            None,
            start,
            end,
        )
        payload, _ = self.assert_single_request()
        self.assertEqual(
            payload["error"],
            "type=ProviderRateLimitError;code=rate_limit;status=429",
        )
        self.assertNotIn(
            "provider-message-must-not-be-forwarded",
            json.dumps(payload),
        )
        self.assertEqual(payload["correlation"], {"request_id": "litellm-call-2"})
        self.assertEqual(payload["usage"]["total_tokens"], 0)
        self.assertEqual(payload["cost_usd"], 0.0)

        with mock.patch.object(
            switchback_callback.urllib.request,
            "urlopen",
            side_effect=OSError("sink unavailable"),
        ):
            await switchback_callback.switchback_callback.async_log_failure_event(
                kwargs,
                None,
                start,
                end,
            )

    async def test_conflicting_litellm_identity_callbacks_do_not_emit_observations(self):
        conflicting = {
            "litellm_call_id": "top-level-call",
            "litellm_params": {"litellm_call_id": "nested-call"},
        }

        await switchback_callback.switchback_callback.async_log_success_event(
            conflicting,
            None,
            datetime(2026, 7, 20, tzinfo=timezone.utc),
            datetime(2026, 7, 20, tzinfo=timezone.utc),
        )
        await switchback_callback.switchback_callback.async_log_failure_event(
            conflicting,
            None,
            datetime(2026, 7, 20, tzinfo=timezone.utc),
            datetime(2026, 7, 20, tzinfo=timezone.utc),
        )

        self.assertEqual(self.requests, [])

    async def test_success_hook_is_total_for_throwing_mapping_and_malformed_usage(self):
        kwargs = {
            "litellm_call_id": "malformed-call",
            "model": ThrowingDump(),
            "standard_logging_object": ThrowingDump(),
            "response_cost": ThrowingNumber(),
            "usage": {
                "prompt_tokens": ThrowingNumber(),
                "completion_tokens": -5,
                "total_tokens": "not-an-integer",
            },
            "proxy_server_request": {
                "headers": {"x-request-id": ThrowingDump()},
                "body": {"model": ThrowingDump()},
            },
        }

        await switchback_callback.switchback_callback.async_log_success_event(
            kwargs,
            ThrowingDump(),
            float("nan"),
            float("inf"),
        )

        payload, _ = self.assert_single_request()
        self.assertEqual(payload["model"], "unknown")
        self.assertEqual(payload["route"], "switchback-codex")
        self.assertEqual(payload["usage"], {
            "input_tokens": 0,
            "output_tokens": 0,
            "total_tokens": 0,
        })
        self.assertEqual(payload["cost_usd"], 0.0)
        self.assertEqual(payload["latency_ms"], 0.0)
        self.assertEqual(payload["started_at"], "1970-01-01T00:00:00+00:00")
        self.assertEqual(payload["ended_at"], "1970-01-01T00:00:00+00:00")
        self.assertEqual(payload["correlation"], {"request_id": "malformed-call"})
        self.assertNotIn("secret", json.dumps(payload))

    async def test_timestamps_normalize_numeric_naive_aware_and_invalid_values(self):
        await switchback_callback.switchback_callback.async_log_success_event(
            {"litellm_call_id": "numeric-call", "model": "gpt-5.2-codex"},
            None,
            100.25,
            101.75,
        )
        numeric, _ = self.assert_single_request()
        self.assertEqual(numeric["started_at"], "1970-01-01T00:01:40.250000+00:00")
        self.assertEqual(numeric["ended_at"], "1970-01-01T00:01:41.750000+00:00")
        self.assertEqual(numeric["latency_ms"], 1500.0)

        self.requests.clear()
        naive = datetime(2026, 7, 17, 3, 0, 0)
        aware = datetime(2026, 7, 17, 3, 0, 2, tzinfo=timezone.utc)
        await switchback_callback.switchback_callback.async_log_success_event(
            {"litellm_call_id": "mixed-call", "model": "gpt-5.2-codex"},
            None,
            naive,
            aware,
        )
        mixed, _ = self.assert_single_request()
        self.assertEqual(mixed["started_at"], "2026-07-17T03:00:00+00:00")
        self.assertEqual(mixed["ended_at"], "2026-07-17T03:00:02+00:00")
        self.assertEqual(mixed["latency_ms"], 2000.0)

        self.requests.clear()
        await switchback_callback.switchback_callback.async_log_success_event(
            {"litellm_call_id": "invalid-call", "model": "gpt-5.2-codex"},
            None,
            object(),
            -float("inf"),
        )
        invalid, _ = self.assert_single_request()
        self.assertEqual(invalid["started_at"], "1970-01-01T00:00:00+00:00")
        self.assertEqual(invalid["ended_at"], "1970-01-01T00:00:00+00:00")
        self.assertEqual(invalid["latency_ms"], 0.0)

    async def test_both_hooks_are_non_throwing_when_mapping_and_post_fail(self):
        malformed = {
            "model": ThrowingDump(),
            "standard_logging_object": ThrowingDump(),
            "exception": ProviderRateLimitError("provider-secret"),
        }
        with mock.patch.object(
            switchback_callback.urllib.request,
            "urlopen",
            side_effect=RuntimeError("sink-secret"),
        ):
            await switchback_callback.switchback_callback.async_log_success_event(
                malformed,
                ThrowingDump(),
                object(),
                object(),
            )
            await switchback_callback.switchback_callback.async_log_failure_event(
                malformed,
                ThrowingDump(),
                object(),
                object(),
            )

    def assert_single_request(self):
        self.assertEqual(len(self.requests), 1)
        request, timeout = self.requests[0]
        self.assertEqual(timeout, 2)
        return json.loads(request.data.decode("utf-8")), request

    def assert_request(self, index):
        request, timeout = self.requests[index]
        self.assertEqual(timeout, 2)
        return request.full_url, json.loads(request.data.decode("utf-8"))


class SwitchbackCallbackContractTests(unittest.TestCase):
    def test_callback_subclasses_real_api_shape_and_never_references_langfuse(self):
        self.assertIsInstance(switchback_callback.switchback_callback, FakeCustomLogger)
        self.assertEqual(
            switchback_callback.switchback_callback.custom_logger_options,
            {"turn_off_message_logging": True},
        )
        self.assertTrue(
            asyncio.iscoroutinefunction(
                switchback_callback.SwitchbackModelObservationCallback.async_pre_call_hook
            )
        )
        self.assertTrue(
            asyncio.iscoroutinefunction(
                switchback_callback.SwitchbackModelObservationCallback.async_log_success_event
            )
        )
        self.assertTrue(
            asyncio.iscoroutinefunction(
                switchback_callback.SwitchbackModelObservationCallback.async_log_failure_event
            )
        )
        source = (HERE / "switchback_callback.py").read_text(encoding="utf-8")
        self.assertNotIn("langfuse", source.lower())
        self.assertNotIn("str(exception)", source)

    def test_config_is_the_fixed_server_owned_route(self):
        self.assertEqual(
            (HERE / "config.yaml").read_text(encoding="utf-8"),
            """model_list:
  - model_name: switchback-codex
    litellm_params:
      model: os.environ/SWITCHBACK_UPSTREAM_MODEL
      api_base: os.environ/SWITCHBACK_UPSTREAM_API_BASE
      api_key: os.environ/SWITCHBACK_UPSTREAM_API_KEY

general_settings:
  master_key: os.environ/SWITCHBACK_PROXY_TOKEN

litellm_settings:
  drop_params: true
  callbacks: switchback_callback.switchback_callback
""",
        )


if __name__ == "__main__":
    unittest.main()
