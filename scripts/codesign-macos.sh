#!/bin/sh
#
# codesign-macos.sh — sign one macOS binary with the stable openctl
# code-signing identity, if that identity is set up. Invoked from the
# Makefile after each `go build` (see scripts/macos-codesign-setup.sh for
# the why and the one-time setup).
#
# Usage: codesign-macos.sh <binary-path> <bundle-id> [identity]
#
# This is a deliberate no-op (exit 0) when signing isn't applicable, so it
# never breaks a build:
#   - not macOS (CI runs on Linux; cross-compiled Linux binaries aren't
#     Mach-O and can't be codesigned anyway)
#   - codesign not on PATH
#   - the signing identity isn't installed (dev hasn't run the setup script)
#
set -eu

bin="${1:?usage: codesign-macos.sh <binary-path> <bundle-id> [identity]}"
bundle_id="${2:?missing bundle id}"
identity="${3:-${CODESIGN_IDENTITY:-openctl-dev}}"

[ "$(uname -s)" = "Darwin" ] || exit 0
command -v codesign >/dev/null 2>&1 || exit 0
[ -f "$bin" ] || exit 0

# Identity present? `find-identity` without -v lists our untrusted
# self-signed cert (setup leaves it untrusted on purpose).
if ! security find-identity -p codesigning 2>/dev/null | grep -q "\"$identity\""; then
  # Not set up — leave the ad-hoc signature go(1) produced in place.
  exit 0
fi

# --force replaces the ad-hoc linker signature. --identifier pins a stable
# bundle id (default is "a.out"), which becomes part of the designated
# requirement the firewall matches on.
if codesign --force --sign "$identity" --identifier "$bundle_id" "$bin" >/dev/null 2>&1; then
  echo "  codesign: $bin  ($identity → $bundle_id)"
else
  echo "  codesign: WARNING failed to sign $bin — continuing with ad-hoc signature" >&2
fi
