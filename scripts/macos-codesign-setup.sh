#!/usr/bin/env bash
#
# macos-codesign-setup.sh — create a stable, self-signed code-signing
# identity in the login keychain so rebuilt openctl binaries keep a
# consistent code identity across builds.
#
# Why this exists
# ---------------
# `go build` produces an ad-hoc ("linker-signed") Mach-O whose cdhash
# changes on every build. Per-app outbound firewalls (LuLu, Little Snitch)
# identify unsigned/ad-hoc apps by that cdhash, so every `make build`
# looks like a brand-new app: the rule you approved yesterday no longer
# matches, the firewall silently blocks the new binary, and openctl gets
# `connect: no route to host` reaching Proxmox — a confusing failure that
# looks like a network problem but is really a firewall re-prompt you
# never saw.
#
# Signing every build with ONE persistent self-signed certificate fixes
# this. codesign's designated requirement for a self-signed leaf is:
#
#     identifier "io.openctl.<x>" and certificate leaf = H"<cert sha1>"
#
# That references the bundle identifier and the certificate hash — never
# the cdhash — so every rebuild signed with the same cert satisfies the
# identical requirement. A firewall rule keyed on that requirement keeps
# applying across rebuilds. The cert is untrusted (not from Apple); that
# is fine — the firewall wants a *stable* identity, not a *trusted* one.
#
# Run this ONCE per machine. Then every `make build` re-signs (see
# scripts/codesign-macos.sh, invoked from the Makefile). Re-running is
# safe and idempotent.
#
# To undo: security delete-identity -c "$IDENTITY" login.keychain-db
#
set -euo pipefail

IDENTITY="${CODESIGN_IDENTITY:-openctl-dev}"
KEYCHAIN="${HOME}/Library/Keychains/login.keychain-db"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "This script is macOS-only (code signing is for macOS firewalls); platform=$(uname -s)." >&2
  exit 1
fi

# Idempotent: if the identity already exists, we're done. `find-identity`
# without -v lists identities even when the (self-signed) cert is
# untrusted, which is the state we deliberately leave it in.
if security find-identity -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "\"$IDENTITY\""; then
  echo "Code-signing identity \"$IDENTITY\" already present in the login keychain — nothing to do."
  echo "Rebuilds will be signed automatically by 'make build'."
  exit 0
fi

echo "Creating self-signed code-signing identity \"$IDENTITY\"..."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/openssl.cnf" <<EOF
[req]
distinguished_name = dn
x509_extensions    = v3
prompt             = no
[dn]
CN = $IDENTITY
[v3]
basicConstraints     = critical,CA:false
keyUsage             = critical,digitalSignature
extendedKeyUsage     = critical,codeSigning
EOF

# 10-year self-signed cert with the codeSigning EKU codesign requires.
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$tmp/key.pem" -out "$tmp/cert.pem" \
  -days 3650 -config "$tmp/openssl.cnf" >/dev/null 2>&1

# -legacy: OpenSSL 3.x defaults to a PKCS#12 MAC (SHA-256) that macOS's
# Security framework can't verify ("MAC verification failed"); the legacy
# encoding imports cleanly.
openssl pkcs12 -export -legacy \
  -inkey "$tmp/key.pem" -in "$tmp/cert.pem" \
  -out "$tmp/bundle.p12" -passout pass:openctl -name "$IDENTITY" >/dev/null 2>&1

# -T /usr/bin/codesign adds codesign to the private key's ACL so it can
# sign without an interactive keychain prompt on every build.
security import "$tmp/bundle.p12" -k "$KEYCHAIN" -P openctl -T /usr/bin/codesign >/dev/null

# Best-effort: authorize codesign against the key's partition list too, so
# newer macOS doesn't prompt. Needs the keychain password on some systems;
# if it fails, codesign will prompt once and you click "Always Allow".
security set-key-partition-list \
  -S apple-tool:,apple:,codesign: -s -k "" "$KEYCHAIN" >/dev/null 2>&1 || true

if ! security find-identity -p codesigning "$KEYCHAIN" 2>/dev/null | grep -q "\"$IDENTITY\""; then
  echo "error: identity \"$IDENTITY\" not found after import — see above." >&2
  exit 1
fi

echo
echo "Done. Identity \"$IDENTITY\" installed in the login keychain."
echo "Every 'make build' now re-signs openctl binaries with it, so a"
echo "firewall rule you approve once keeps applying across rebuilds."
echo
echo "Next: rebuild and re-approve once in your firewall (LuLu/Little Snitch):"
echo "  make build"
