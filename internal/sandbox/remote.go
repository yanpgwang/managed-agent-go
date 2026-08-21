package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
)

const (
	remoteManagedKey    = "io.mango.managed"
	remoteSessionKey    = "io.mango.session_key"
	remoteManagedValue  = "true"
	remoteDefaultRoot   = "/workspace"
	remoteDefaultPeriod = 30 * time.Second
)

func validateRemoteReference(providerName, sessionKey string, ref Ref) error {
	if sessionKey == "" {
		return Permanent(errors.New("sandbox: session key is required"))
	}
	if err := ref.validate(); err != nil {
		return Permanent(err)
	}
	if ref.Provider != providerName {
		return Permanent(fmt.Errorf(
			"sandbox: %s provider cannot attach reference for %q",
			providerName,
			ref.Provider,
		))
	}
	return nil
}

func remoteMetadata(sessionKey string) map[string]string {
	return map[string]string{
		remoteManagedKey: remoteManagedValue,
		remoteSessionKey: remoteSessionIdentity(sessionKey),
	}
}

func validateRemoteOwnership(
	providerName string,
	resourceID string,
	sessionKey string,
	metadata map[string]string,
) error {
	if metadata[remoteManagedKey] != remoteManagedValue {
		return Permanent(fmt.Errorf(
			"sandbox: refusing to attach unmanaged %s sandbox %q",
			providerName,
			resourceID,
		))
	}
	if metadata[remoteSessionKey] != remoteSessionIdentity(sessionKey) {
		return Permanent(fmt.Errorf(
			"sandbox: %s sandbox %q belongs to another session",
			providerName,
			resourceID,
		))
	}
	return nil
}

func remoteSessionIdentity(sessionKey string) string {
	sum := sha256.Sum256([]byte(sessionKey))
	return fmt.Sprintf("%x", sum[:16])
}

func remoteControlHTTPClient(base *http.Client) *http.Client {
	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	if client.Timeout <= 0 {
		client.Timeout = remoteDefaultPeriod
	}
	return client
}

func deterministicRemoteName(providerName, sessionKey string) string {
	sum := sha256.Sum256([]byte(providerName + "\x00" + sessionKey))
	return fmt.Sprintf("mango-%x", sum[:16])
}

// containedRemotePath confines a relative tool path to a POSIX workspace.
// Remote services receive the resulting absolute path; no host filesystem is
// consulted.
func containedRemotePath(root, value string) (string, error) {
	if root == "" {
		root = remoteDefaultRoot
	}
	clean := path.Clean(path.Join(root, value))
	if clean != root && !strings.HasPrefix(clean+"/", root+"/") {
		return "", fmt.Errorf("sandbox: path %q escapes root", value)
	}
	return clean, nil
}

// remoteToolPath resolves provider file-tool requests without confusing an
// absolute Session resource path with a workspace-relative path. Additional
// roots are opt-in per sandbox capability; every other absolute path remains
// outside the file-tool boundary.
func remoteToolPath(
	root string,
	value string,
	additionalRoots ...string,
) (string, error) {
	if root == "" {
		root = remoteDefaultRoot
	}
	if !path.IsAbs(value) {
		return containedRemotePath(root, value)
	}
	clean := path.Clean(value)
	if pathWithinRemoteRoot(root, clean) {
		return clean, nil
	}
	for _, additionalRoot := range additionalRoots {
		if !pathWithinRemoteRoot(additionalRoot, clean) || clean == additionalRoot {
			continue
		}
		return clean, nil
	}
	return "", fmt.Errorf("sandbox: path %q is outside the authorized runtime roots", value)
}

// remoteWritableToolPath preserves normal workspace writes while reserving
// the custom Skill tree for the bundle reconciler. This explicit check matters
// for /workspace providers, where the Skill root is otherwise a child of the
// ordinary workspace root.
func remoteWritableToolPath(
	root string,
	value string,
	additionalRoots ...string,
) (string, error) {
	full, err := remoteToolPath(root, value, additionalRoots...)
	if err != nil {
		return "", err
	}
	if pathWithinRemoteRoot(SessionSkillsRoot, full) {
		return "", fmt.Errorf("sandbox: path %q is read-only", value)
	}
	return full, nil
}

func pathWithinRemoteRoot(root string, value string) bool {
	root = path.Clean(root)
	value = path.Clean(value)
	return value == root || strings.HasPrefix(value, root+"/")
}

func commandTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// remoteOperationCommandTimeout lets provider maintenance operations use the
// caller's wider deadline instead of inheriting the shorter per-tool timeout.
// Callers without a deadline retain the sandbox timeout as a safe fallback.
func remoteOperationCommandTimeout(
	ctx context.Context,
	fallback time.Duration,
) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}

func remoteCommandLine(cmd Command) string {
	parts := make([]string, 0, 1+len(cmd.Args))
	parts = append(parts, shellQuote(cmd.Path))
	for _, arg := range cmd.Args {
		parts = append(parts, shellQuote(arg))
	}
	command := "exec " + strings.Join(parts, " ")
	if len(cmd.Stdin) == 0 {
		return command
	}
	encoded := base64.StdEncoding.EncodeToString(cmd.Stdin)
	return "printf %s " + shellQuote(encoded) + " | base64 -d | " + command
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func capRemoteOutput(value string) []byte {
	raw := []byte(value)
	if len(raw) > maxOutput {
		raw = raw[:maxOutput]
	}
	return append([]byte(nil), raw...)
}

func remoteResult(
	ctx context.Context,
	timeout time.Duration,
	stdout string,
	stderr string,
	exitCode int,
) (*Result, error) {
	result := &Result{
		Stdout:   capRemoteOutput(stdout),
		Stderr:   capRemoteOutput(stderr),
		ExitCode: exitCode,
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && timeout > 0 {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return result, nil
}
