#!/bin/sh
set -eu

kimi_code_key_file=${SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY_FILE:?Assistant Kimi Code key file is required}
deepseek_key_file=${SWITCHBACK_ASSISTANT_DEEPSEEK_API_KEY_FILE:?Assistant DeepSeek key file is required}
test -f "$kimi_code_key_file" && test ! -L "$kimi_code_key_file"
test -f "$deepseek_key_file" && test ! -L "$deepseek_key_file"
SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY=$(cat "$kimi_code_key_file")
SWITCHBACK_ASSISTANT_DEEPSEEK_API_KEY=$(cat "$deepseek_key_file")
test -n "$SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY"
test -n "$SWITCHBACK_ASSISTANT_DEEPSEEK_API_KEY"
export SWITCHBACK_ASSISTANT_KIMI_CODE_API_KEY SWITCHBACK_ASSISTANT_DEEPSEEK_API_KEY

exec litellm --config /opt/switchback/assistant-litellm/config.yaml --host 0.0.0.0 --port 4000
