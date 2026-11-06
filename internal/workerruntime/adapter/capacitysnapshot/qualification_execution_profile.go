package capacitysnapshot

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

const qualificationExecutionProfileSchema = "switchback.qualification.execution_profile.v1"
const qualificationExecutionProfileLeaf = "execution-profile.json"
const qualificationKimiCredentialPath = "/var/lib/switchback/provider-credentials/kimi.key"
const qualificationDeepSeekCredentialPath = "/var/lib/switchback/provider-credentials/deepseek.key"

// This producer-private selector freezes existing role environment, not any
// Task/Turn authority, provider choice, image input, or public protocol.
type qualificationExecutionProfile struct {
	Schema string `json:"schema"`
	Mode   string `json:"mode"`
}

// Only the initial nofollow lookup may establish absence. ENOENT after a
// successful lookup is instability, never a request to disable execution.
func qualificationExecutionProfileAt(ctx context.Context, dir *os.File, stat func(context.Context, int) (unix.Stat_t, error)) (qualificationExecutionProfile, error) {
	if qualificationContext(ctx) != nil || dir == nil || stat == nil {
		return qualificationExecutionProfile{}, errQualification
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), qualificationExecutionProfileLeaf, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) && qualificationContext(ctx) == nil {
			return qualificationExecutionProfile{Schema: qualificationExecutionProfileSchema, Mode: "disabled"}, nil
		}
		return qualificationExecutionProfile{}, err
	}
	leaf, err := readQualificationLeafWithStat(ctx, dir, qualificationExecutionProfileLeaf, func(ctx context.Context, fd int) (unix.Stat_t, error) {
		value, err := stat(ctx, fd)
		if err == nil {
			err = qualificationEngineCheckLeaf(uint64(value.Mode), uint64(value.Nlink), uint64(value.Uid), uint64(value.Gid), value.Size, 0400, 4096)
		}
		return value, err
	})
	if err != nil {
		return qualificationExecutionProfile{}, err
	}
	return qualificationDecodeExecutionProfile(leaf.data)
}

func qualificationDecodeExecutionProfile(raw []byte) (qualificationExecutionProfile, error) {
	var profile qualificationExecutionProfile
	if strictJSON(raw, &profile) != nil || profile.Schema != qualificationExecutionProfileSchema ||
		(profile.Mode != "disabled" && profile.Mode != "worker_delivery") {
		return qualificationExecutionProfile{}, errQualification
	}
	return profile, nil
}

func qualificationExecutionBackendEnvironment(profile qualificationExecutionProfile, workerImage string) ([]qualificationEnv, error) {
	if profile.Schema != qualificationExecutionProfileSchema || (profile.Mode != "disabled" && profile.Mode != "worker_delivery") {
		return nil, errQualification
	}
	env := qualificationBackendEnvironment()
	if profile.Mode == "disabled" {
		return env, nil
	}
	if !digestPattern.MatchString(workerImage) {
		return nil, errQualification
	}
	for i := range env {
		if env[i].Name == "SWITCHBACK_M1_WORKER_DELIVERY_ENABLED" {
			env[i].Value = "1"
		}
	}
	return append(env,
		qualificationEnv{"SWITCHBACK_WORKER_IMAGE", workerImage},
		qualificationEnv{"SWITCHBACK_KIMI_CREDENTIAL_FILE", qualificationKimiCredentialPath},
		qualificationEnv{"SWITCHBACK_DEEPSEEK_CREDENTIAL_FILE", qualificationDeepSeekCredentialPath},
	), nil
}
