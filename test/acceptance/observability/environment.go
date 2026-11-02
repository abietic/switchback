package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// freshEnvironment never reads deploy.env, ambient provider keys or prior claims.
func freshEnvironment(project, root string) error {
	if strings.ContainsAny(root, " \n\r\t#$'\"") {
		return fmt.Errorf("env output path contains unsupported characters")
	}
	for _, dir := range []string{"data", "keys"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			return err
		}
	}
	env := map[string]string{"COMPOSE_PROJECT_NAME": project, "SWITCHBACK_HOST_DATA_ROOT": filepath.Join(root, "data"), "SWITCHBACK_WORKER_NETWORK": project + "-worker-control", "SWITCHBACK_WORKER_IMAGE": project + "-worker:controlled", "OBSERVABILITY_BACKEND_IMAGE": project + "-backend:acceptance", "OBSERVABILITY_DATABASE_NAME": strings.ReplaceAll(project, "-", "_"), "SWITCHBACK_M2_ASSISTANT_ENABLED": "0", "SWITCHBACK_SINGLE_RUNTIME_RECOVERY_ENABLED": "0"}
	for _, key := range []string{"SWITCHBACK_GRAFANA_PASSWORD", "SWITCHBACK_POSTGRES_PASSWORD", "LANGFUSE_NEXTAUTH_SECRET", "LANGFUSE_SALT", "LANGFUSE_ENCRYPTION_KEY", "LANGFUSE_CLICKHOUSE_PASSWORD", "LANGFUSE_MINIO_PASSWORD", "LANGFUSE_REDIS_PASSWORD", "LANGFUSE_INIT_PROJECT_PUBLIC_KEY", "LANGFUSE_INIT_PROJECT_SECRET_KEY", "LANGFUSE_INIT_USER_PASSWORD", "SWITCHBACK_ASSISTANT_LITELLM_PROXY_KEY"} {
		value := make([]byte, 32)
		if _, err := rand.Read(value); err != nil {
			return err
		}
		env[key] = hex.EncodeToString(value)
	}
	env["LANGFUSE_INIT_PROJECT_PUBLIC_KEY"] = "pk-lf-" + env["LANGFUSE_INIT_PROJECT_PUBLIC_KEY"]
	env["LANGFUSE_INIT_PROJECT_SECRET_KEY"] = "sk-lf-" + env["LANGFUSE_INIT_PROJECT_SECRET_KEY"]
	for _, provider := range []string{"KIMI", "DEEPSEEK"} {
		path := filepath.Join(root, "keys", strings.ToLower(provider)+".key")
		value := make([]byte, 32)
		if _, err := rand.Read(value); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("synthetic-only-"+hex.EncodeToString(value)+"\n"), 0600); err != nil {
			return err
		}
		env["SWITCHBACK_"+provider+"_KEY_FILE"] = path
	}
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, key := range []string{"SWITCHBACK_GRAFANA_PORT", "SWITCHBACK_PROMETHEUS_PORT", "SWITCHBACK_BACKEND_PORT", "SWITCHBACK_WEB_UI_PORT", "SWITCHBACK_LANGFUSE_PORT", "SWITCHBACK_LANGFUSE_MINIO_PORT", "OBSERVABILITY_POSTGRES_PORT"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		listeners = append(listeners, listener)
		env[key] = fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var contents strings.Builder
	contents.WriteString("# Fresh synthetic acceptance credentials. Loopback ports selected at claim time; operator must recheck before start.\n")
	for _, key := range keys {
		fmt.Fprintf(&contents, "%s=%s\n", key, env[key])
	}
	return os.WriteFile(filepath.Join(root, "acceptance.env"), []byte(contents.String()), 0600)
}
