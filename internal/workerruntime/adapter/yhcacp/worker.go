package yhcacp

import (
	"io"
	"os/exec"
	"syscall"

	"github.com/abietic/switchback/internal/workerruntime/adapter/acpclient"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
)

const AgentHome = "/var/lib/switchback/agent-state/yhc-user"

// New composes the fixed YHC launcher for the isolated relay profile.
func New(trace io.Writer, token string) (*acpclient.Adapter, error) {
	return acpclient.NewPreconfigured(acpclient.Options{
		ProcessName: "yhc", WorkspaceRoot: "/workspace", Trace: trace,
		ExtensionFailureSession: FailureSession,
	}, token, func(proxyToken string) *exec.Cmd {
		cmd := exec.Command("/opt/switchback/yhc", "serve", "acp", "--provider", "deepseek", "--model", "switchback-yhc",
			"--base-url", "http://127.0.0.1:4000/v1", "--tools=", "--disable-long-session-services")
		cmd.Dir = "/workspace"
		cmd.Env = []string{
			"HOME=" + AgentHome,
			"XDG_CONFIG_HOME=" + AgentHome,
			"YHC_SIMPLE=1", "YHC_DISABLE_AUTO_MEMORY=1",
			"PATH=/usr/local/bin:/usr/bin:/bin", "PROV_API_KEY=" + proxyToken,
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: runtimesecret.AgentUID, Gid: runtimesecret.AgentUID, NoSetGroups: true,
		}}
		return cmd
	})
}
