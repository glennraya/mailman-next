#!/bin/sh
# Install Mailman from the latest GitHub release.
#
# Deliberately POSIX sh, since /bin/sh is dash on Debian and Ubuntu. And
# deliberately stopping short of registering the login service: a one-liner
# piped from the internet that quietly installs a login item is the kind of
# thing people are right to be angry about. It prints the command instead.
set -eu

REPO="glennraya/mailman-next"
BIN_DIR="${MAILMAN_BIN_DIR:-}"

fail() {
	echo "install: $1" >&2
	exit 1
}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "mailman has no build for $(uname -s). Build from source with 'make build'" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "mailman has no build for $(uname -m)" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

# Verification is not optional. A download that cannot be checked is not
# installed at all, rather than installed with a warning nobody reads.
if command -v sha256sum >/dev/null 2>&1; then
	checksum="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
	checksum="shasum -a 256"
else
	fail "neither sha256sum nor shasum is available, so the download cannot be verified"
fi

version="${MAILMAN_VERSION:-}"
if [ -z "$version" ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)
fi
[ -n "$version" ] || fail "could not work out the latest release. Set MAILMAN_VERSION to pick one"

asset="mailman-$os-$arch"
base="https://github.com/$REPO/releases/download/$version"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

echo "Downloading mailman $version for $os/$arch"
curl -fsSL "$base/$asset" -o "$work/$asset" || fail "could not download $base/$asset"
curl -fsSL "$base/SHA256SUMS" -o "$work/SHA256SUMS" || fail "could not download the checksums"

# Check only the asset actually downloaded: the file lists every platform,
# and --ignore-missing is not portable across the two checksum tools.
expected=$(grep " $asset\$" "$work/SHA256SUMS" | awk '{print $1}')
[ -n "$expected" ] || fail "$asset is not listed in SHA256SUMS"
actual=$($checksum "$work/$asset" | awk '{print $1}')
[ "$expected" = "$actual" ] || fail "checksum mismatch for $asset -- refusing to install"

if [ -z "$BIN_DIR" ]; then
	if [ -w /usr/local/bin ]; then
		BIN_DIR=/usr/local/bin
	else
		BIN_DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$BIN_DIR"

chmod 0755 "$work/$asset"
mv "$work/$asset" "$BIN_DIR/mailman"

echo
echo "mailman $version installed to $BIN_DIR/mailman"

case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) echo "  $BIN_DIR is not on your PATH -- add it to your shell profile" ;;
esac

cat <<'NEXT'

To run it at login and keep it running:

    mailman service install
NEXT
