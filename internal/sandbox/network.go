package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

func applyLimitedNetwork(
	ctx context.Context,
	box Sandbox,
	allowedHosts []string,
) error {
	controller, ok := box.(LimitedNetworkSandbox)
	if !ok {
		return Permanent(fmt.Errorf(
			"sandbox: %T does not expose limited network enforcement",
			box,
		))
	}
	if err := controller.ApplyLimitedNetwork(
		ctx,
		append([]string(nil), allowedHosts...),
	); err != nil {
		return fmt.Errorf("sandbox: apply limited network policy: %w", err)
	}
	return nil
}

// bindingSpecHash preserves the complete provisioning hash for diagnostics and
// appends a stable package-only proof. The suffix lets a limited network policy
// change for the same Session without making completed package setup ambiguous.
func bindingSpecHash(spec Spec) string {
	return specHash(spec) + "|packages=" + packageSetupHash(spec.Packages)
}

func bindingProvesPackageSetup(bindingHash string, spec Spec) bool {
	if spec.Packages.Empty() {
		return true
	}
	const marker = "|packages="
	if index := strings.LastIndex(bindingHash, marker); index >= 0 {
		return bindingHash[index+len(marker):] == packageSetupHash(spec.Packages)
	}
	// PR #89 bindings predate the package-only suffix. They remain valid while
	// the complete requested Spec is unchanged.
	return bindingHash == specHash(spec)
}

func packageSetupHash(packages PackageSet) string {
	return hashJSON(packages)
}

func limitedNetworkHash(spec Spec) string {
	return hashJSON(spec.NetworkAllowedHosts)
}

func hashJSON(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum[:])
}
