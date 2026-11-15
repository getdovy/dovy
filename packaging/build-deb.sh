#!/bin/sh
# Builds OUT/dovy_<version>-1~dovy13_amd64.deb and OUT/SHA256SUMS
# (specs/package.md). Run from the repository root, in Debian 13 with its
# Go: make deb does both. Usage: packaging/build-deb.sh VERSION OUT
set -eu
version=$1 out=$2
deb=$version-1~dovy13 # what the dovy recipe stamps (ADR 0010)
root=$(mktemp -d)
chmod 755 "$root"
trap 'rm -rf "$root"' EXIT

CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$root/usr/bin/dovy" ./cmd/dovy

install -d -m 755 "$root/DEBIAN" "$root/etc/apt/sources.list.d" "$root/usr/share/keyrings" \
	"$root/var/lib/dovy" "$root/etc/dovy/keys.d" "$root/usr/share/man/man1" \
	"$root/usr/share/bash-completion/completions" "$root/usr/share/zsh/vendor-completions" \
	"$root/usr/share/fish/vendor_completions.d" "$root/usr/share/doc/dovy"
install -m 644 packaging/dovy.sources "$root/etc/apt/sources.list.d/dovy.sources"
install -m 644 /dev/null "$root/usr/share/keyrings/dovy-local.pgp" # empty (ADR 0024)
gzip -9n <packaging/dovy.1 >"$root/usr/share/man/man1/dovy.1.gz"
install -m 644 packaging/completions/dovy.bash "$root/usr/share/bash-completion/completions/dovy"
install -m 644 packaging/completions/_dovy "$root/usr/share/zsh/vendor-completions/_dovy"
install -m 644 packaging/completions/dovy.fish "$root/usr/share/fish/vendor_completions.d/dovy.fish"
install -m 644 packaging/copyright "$root/usr/share/doc/dovy/copyright"
chmod 644 "$root/usr/share/man/man1/dovy.1.gz"
install -m 755 packaging/postinst packaging/postrm "$root/DEBIAN/"
size=$(du -sk --exclude=DEBIAN "$root" | cut -f1)
sed "s/@VERSION@/$deb/; s/@SIZE@/$size/" packaging/control >"$root/DEBIAN/control"

mkdir -p "$out"
dpkg-deb --root-owner-group -Zxz --build "$root" "$out/dovy_${deb}_amd64.deb"
(cd "$out" && sha256sum "dovy_${deb}_amd64.deb" >SHA256SUMS)
