ARG PYTHON_IMAGE=docker.io/library/python@sha256:56249d7a2f93306106f6d8bcdf6423afb73c1b747d874febcc778beee25cb8bb

FROM ${PYTHON_IMAGE}
RUN groupadd --gid 10001 litellm \
    && useradd --uid 10001 --gid litellm --create-home --home-dir /var/lib/switchback-litellm --shell /usr/sbin/nologin litellm \
    && pip install --no-cache-dir 'litellm[proxy]==1.93.0' \
    && python -c "import importlib.metadata as m; assert m.version('litellm') == '1.93.0'" \
    && litellm --version
COPY images/assistant-litellm/config.yaml /opt/switchback/assistant-litellm/config.yaml
COPY images/assistant-litellm/entrypoint.sh /usr/local/bin/assistant-litellm
RUN chmod 0555 /usr/local/bin/assistant-litellm \
    && chown -R litellm:litellm /var/lib/switchback-litellm
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/assistant-litellm"]
