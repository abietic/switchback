import asyncio
from datetime import datetime, timezone
import json
import math
import os
import urllib.request

from litellm.integrations.custom_logger import CustomLogger


_SINK_URL = "http://127.0.0.1:7777/"
_BIND_URL = _SINK_URL + "bind"
_SINK_TOKEN_ENV = "SWITCHBACK_SPIKE_MODEL_SINK_TOKEN"
_HTTP_TIMEOUT_SECONDS = 2
_MAX_TEXT_LENGTH = 16 * 1024
_MAX_HEADER_LENGTH = 4 * 1024

class SwitchbackModelObservationCallback(CustomLogger):
    def __init__(self):
        super().__init__(turn_off_message_logging=True)

    async def async_pre_call_hook(self, user_api_key_dict, cache, data, call_type):
        call_id = _unique_litellm_call_id(data)
        if not call_id:
            return "model observation correlation rejected"
        try:
            await asyncio.to_thread(_post_binding, call_id)
        except Exception:
            return "model observation correlation rejected"
        _normalize_responses_tool_history(data)
        return data

    async def async_log_success_event(
        self,
        kwargs,
        response_obj,
        start_time,
        end_time,
    ):
        try:
            if not _unique_litellm_call_id(kwargs):
                return
            await self._post(
                _observation(kwargs, response_obj, start_time, end_time, "")
            )
        except Exception:
            return

    async def async_log_failure_event(
        self,
        kwargs,
        response_obj,
        start_time,
        end_time,
    ):
        try:
            mapped_kwargs = _mapping(kwargs)
            if not _unique_litellm_call_id(mapped_kwargs):
                return
            standard = _mapping(mapped_kwargs.get("standard_logging_object"))
            await self._post(
                _observation(
                    mapped_kwargs,
                    response_obj,
                    start_time,
                    end_time,
                    _failure_error(mapped_kwargs, standard),
                )
            )
        except Exception:
            return

    async def _post(self, observation):
        try:
            await asyncio.to_thread(_post_observation, observation)
        except Exception:
            return


def _observation(kwargs, response_obj, start_time, end_time, error):
    kwargs = _mapping(kwargs)
    standard = _mapping(kwargs.get("standard_logging_object"))
    model = _first_text(
        (kwargs.get("model"), standard.get("model")),
        "unknown",
        _MAX_TEXT_LENGTH,
    )
    route = _route(kwargs, standard)
    started_at, ended_at, latency_ms = _normalized_times(start_time, end_time)
    return {
        "model": model,
        "route": route,
        "started_at": started_at,
        "ended_at": ended_at,
        "latency_ms": latency_ms,
        "usage": _usage(kwargs, response_obj, standard),
        "cost_usd": _number(
            kwargs.get("response_cost", standard.get("response_cost", 0.0))
        ),
        "error": _text(error, _MAX_TEXT_LENGTH, "type=model_call_failed"),
        "correlation": _correlation(kwargs),
    }


def _usage(kwargs, response_obj, standard):
    response = _mapping(response_obj)
    usage = _mapping(response.get("usage"))
    if not usage and response_obj is not None:
        usage = _mapping(_safe_attribute(response_obj, "usage"))
    if not usage:
        usage = _mapping(kwargs.get("usage"))
    if not usage:
        usage = _mapping(standard.get("usage"))
    input_tokens = _integer(
        usage.get("input_tokens", usage.get("prompt_tokens", standard.get("prompt_tokens", 0)))
    )
    output_tokens = _integer(
        usage.get(
            "output_tokens",
            usage.get("completion_tokens", standard.get("completion_tokens", 0)),
        )
    )
    total_tokens = _integer(
        usage.get("total_tokens", standard.get("total_tokens", input_tokens + output_tokens))
    )
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "total_tokens": total_tokens,
    }


def _route(kwargs, standard):
    request = _mapping(kwargs.get("proxy_server_request"))
    requested_model = _mapping(request.get("body")).get("model")
    metadata = _mapping(kwargs.get("metadata"))
    litellm_metadata = _mapping(_mapping(kwargs.get("litellm_params")).get("metadata"))
    return _first_text(
        (
            requested_model,
            metadata.get("model_group"),
            litellm_metadata.get("model_group"),
            kwargs.get("litellm_model_name"),
            kwargs.get("model_group"),
            standard.get("model_group"),
        ),
        "switchback-codex",
        _MAX_TEXT_LENGTH,
    )


def _correlation(kwargs):
    call_id = _unique_litellm_call_id(kwargs)
    if not call_id:
        return {}
    return {"request_id": call_id}


def _unique_litellm_call_id(kwargs):
    kwargs = _mapping(kwargs)
    litellm_params = _mapping(kwargs.get("litellm_params"))
    has_top_level = "litellm_call_id" in kwargs
    has_nested = "litellm_call_id" in litellm_params
    if not has_top_level and not has_nested:
        return ""

    top_level = _text(kwargs.get("litellm_call_id"), _MAX_HEADER_LENGTH, "")
    nested = _text(litellm_params.get("litellm_call_id"), _MAX_HEADER_LENGTH, "")
    if (has_top_level and not top_level) or (has_nested and not nested):
        return ""
    if has_top_level and has_nested and top_level != nested:
        return ""
    if has_top_level:
        return top_level
    return nested


def _normalize_responses_tool_history(data):
    if not isinstance(data, dict):
        return
    _normalize_responses_tool_batches(data)
    _normalize_chat_tool_batches(data)


def _normalize_responses_tool_batches(data):
    input_items = data.get("input")
    if not isinstance(input_items, list) or len(input_items) < 3:
        return

    normalized = []
    index = 0
    while index < len(input_items):
        if not _is_responses_tool_call(input_items[index]):
            normalized.append(input_items[index])
            index += 1
            continue

        call_end = index
        while call_end < len(input_items) and _is_responses_tool_call(
            input_items[call_end]
        ):
            call_end += 1
        assistant_end = call_end
        while assistant_end < len(input_items) and _is_responses_assistant_message(
            input_items[assistant_end]
        ):
            assistant_end += 1
        output_end = assistant_end
        while output_end < len(input_items) and _is_responses_tool_output(
            input_items[output_end]
        ):
            output_end += 1

        call_ids = [
            _responses_tool_call_id(item) for item in input_items[index:call_end]
        ]
        output_ids = [
            item.get("call_id") for item in input_items[assistant_end:output_end]
        ]
        if assistant_end > call_end and _same_nonempty_unique_ids(
            call_ids, output_ids
        ):
            normalized.extend(
                item
                for item in input_items[call_end:assistant_end]
                if not _is_empty_assistant_message(item)
            )
            normalized.extend(input_items[index:call_end])
            normalized.extend(input_items[assistant_end:output_end])
            index = output_end
            continue

        normalized.append(input_items[index])
        index += 1
    data["input"] = normalized


def _normalize_chat_tool_batches(data):
    messages = data.get("messages")
    if not isinstance(messages, list) or len(messages) < 3:
        return

    normalized = []
    index = 0
    while index < len(messages):
        tool_call = messages[index]
        if not _is_chat_tool_call(tool_call):
            normalized.append(tool_call)
            index += 1
            continue

        assistant_end = index + 1
        while assistant_end < len(messages) and _is_plain_chat_assistant_message(
            messages[assistant_end]
        ):
            assistant_end += 1
        output_end = assistant_end
        while output_end < len(messages) and _is_chat_tool_output(
            messages[output_end]
        ):
            output_end += 1

        call_ids = [
            item.get("id")
            for item in tool_call.get("tool_calls", [])
            if isinstance(item, dict)
        ]
        output_ids = [
            item.get("tool_call_id") for item in messages[assistant_end:output_end]
        ]
        if assistant_end > index + 1 and _same_nonempty_unique_ids(
            call_ids, output_ids
        ):
            normalized.extend(
                message
                for message in messages[index + 1 : assistant_end]
                if not _is_empty_chat_assistant_message(message)
            )
            normalized.append(tool_call)
            normalized.extend(messages[assistant_end:output_end])
            index = output_end
            continue

        normalized.append(tool_call)
        index += 1
    data["messages"] = normalized


def _is_responses_tool_call(item):
    return isinstance(item, dict) and item.get("type") in (
        "function_call",
        "custom_tool_call",
    )


def _responses_tool_call_id(item):
    return item.get("call_id") or item.get("id")


def _is_responses_tool_output(item):
    return isinstance(item, dict) and item.get("type") in (
        "function_call_output",
        "custom_tool_call_output",
    )


def _is_responses_assistant_message(item):
    return (
        isinstance(item, dict)
        and item.get("type") == "message"
        and item.get("role") == "assistant"
    )


def _is_empty_assistant_message(item):
    if not isinstance(item, dict):
        return False
    if item.get("type") != "message" or item.get("role") != "assistant":
        return False
    content = item.get("content")
    if content == "" or content == []:
        return True
    if not isinstance(content, list):
        return False
    for part in content:
        if not isinstance(part, dict):
            return False
        if part.get("type") not in ("output_text", "text") or part.get("text") != "":
            return False
    return True


def _is_empty_chat_assistant_message(message):
    if not isinstance(message, dict) or message.get("role") != "assistant":
        return False
    if message.get("tool_calls"):
        return False
    for key, value in message.items():
        if key in ("role", "content", "tool_calls"):
            continue
        if value not in (None, "", [], {}):
            return False
    return message.get("content") in (None, "", [])


def _is_chat_tool_call(message):
    return (
        isinstance(message, dict)
        and message.get("role") == "assistant"
        and isinstance(message.get("tool_calls"), list)
        and bool(message.get("tool_calls"))
    )


def _is_plain_chat_assistant_message(message):
    return (
        isinstance(message, dict)
        and message.get("role") == "assistant"
        and not message.get("tool_calls")
    )


def _is_chat_tool_output(message):
    return isinstance(message, dict) and message.get("role") == "tool"


def _same_nonempty_unique_ids(left, right):
    values = (*left, *right)
    if (
        not left
        or not right
        or any(not isinstance(value, str) or not value for value in values)
    ):
        return False
    return (
        len(left) == len(set(left))
        and len(right) == len(set(right))
        and set(left) == set(right)
    )


def _post_observation(observation):
    _post_json(_SINK_URL, observation)


def _post_binding(call_id):
    if not os.environ.get(_SINK_TOKEN_ENV, ""):
        raise RuntimeError("model observation sink token is unavailable")
    _post_json(_BIND_URL, {"litellm_call_id": call_id})


def _post_json(url, payload):
    token = os.environ.get(_SINK_TOKEN_ENV, "")
    if not token:
        return
    body = json.dumps(
        payload,
        ensure_ascii=True,
        separators=(",", ":"),
    ).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        headers={
            "Content-Type": "application/json",
            "X-Switchback-Model-Sink-Token": token,
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=_HTTP_TIMEOUT_SECONDS) as response:
        response.read(4096)


def _mapping(value):
    if isinstance(value, dict):
        try:
            return dict(value)
        except Exception:
            return {}
    if value is None:
        return {}
    model_dump = _safe_attribute(value, "model_dump")
    if callable(model_dump):
        try:
            dumped = model_dump()
            if isinstance(dumped, dict):
                return dict(dumped)
        except Exception:
            pass
    legacy_dict = _safe_attribute(value, "dict")
    if callable(legacy_dict):
        try:
            dumped = legacy_dict()
            if isinstance(dumped, dict):
                return dict(dumped)
        except Exception:
            pass
    attributes = _safe_attribute(value, "__dict__")
    if isinstance(attributes, dict):
        try:
            return dict(attributes)
        except Exception:
            return {}
    return {}


def _safe_attribute(value, name):
    try:
        return getattr(value, name, None)
    except Exception:
        return None


def _normalized_times(start_value, end_value):
    epoch = datetime(1970, 1, 1, tzinfo=timezone.utc)
    start, start_valid = _datetime_value(start_value)
    end, end_valid = _datetime_value(end_value)
    if not start_valid and not end_valid:
        start = epoch
        end = epoch
    elif not start_valid:
        start = end
    elif not end_valid:
        end = start
    if end < start:
        end = start
    return (
        start.isoformat(),
        end.isoformat(),
        max(0.0, (end - start).total_seconds() * 1000.0),
    )


def _datetime_value(value):
    try:
        if isinstance(value, datetime):
            if value.tzinfo is None:
                return value.replace(tzinfo=timezone.utc), True
            return value.astimezone(timezone.utc), True
        if type(value) in (int, float):
            numeric = float(value)
            if math.isfinite(numeric):
                return datetime.fromtimestamp(numeric, tz=timezone.utc), True
    except Exception:
        pass
    return datetime(1970, 1, 1, tzinfo=timezone.utc), False


def _failure_error(kwargs, standard):
    exception = kwargs.get("exception")
    error_type = "model_call_failed"
    if isinstance(exception, BaseException):
        candidate = _safe_atom(type(exception).__name__, 128)
        if candidate:
            error_type = candidate
    else:
        candidate = _safe_atom(standard.get("error_type"), 128)
        if candidate:
            error_type = candidate

    code = _first_atom(
        (
            _safe_attribute(exception, "code"),
            kwargs.get("error_code"),
            standard.get("error_code"),
            standard.get("code"),
        ),
        256,
    )
    status = _first_atom(
        (
            _safe_attribute(exception, "status_code"),
            _safe_attribute(exception, "status"),
            kwargs.get("status_code"),
            standard.get("status_code"),
            standard.get("status"),
        ),
        32,
    )
    parts = ["type=" + error_type]
    if code:
        parts.append("code=" + code)
    if status:
        parts.append("status=" + status)
    return ";".join(parts)[:_MAX_TEXT_LENGTH]


def _first_atom(values, limit):
    for value in values:
        atom = _safe_atom(value, limit)
        if atom:
            return atom
    return ""


def _safe_atom(value, limit):
    if type(value) is str:
        raw = value
    elif type(value) is int:
        raw = str(value)
    elif type(value) is float and math.isfinite(value):
        raw = str(value)
    else:
        return ""
    allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"
    return "".join(character for character in raw if character in allowed)[:limit]


def _first_text(values, default, limit):
    for value in values:
        text = _text(value, limit, "")
        if text:
            return text
    return default


def _text(value, limit, default=""):
    if type(value) is str:
        return value[:limit]
    if type(value) is int:
        return str(value)[:limit]
    if type(value) is float and math.isfinite(value):
        return str(value)[:limit]
    return default


def _number(value):
    if type(value) is bool:
        return 0.0
    try:
        number = float(value)
    except Exception:
        return 0.0
    if not math.isfinite(number):
        return 0.0
    return max(0.0, number)


def _integer(value):
    if type(value) is bool:
        return 0
    try:
        number = int(value)
    except Exception:
        return 0
    return max(0, number)


switchback_callback = SwitchbackModelObservationCallback()
