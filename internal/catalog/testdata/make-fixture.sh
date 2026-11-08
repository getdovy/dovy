#!/bin/bash
# Makes the test catalogs (testdata/catalog-*.bundle) and testdata/allowed_signers.
# Run by a maintainer (it commits); tests only read the result.
# Releases are tags (ADR 0005). Throwaway keys: "trusted" signs, "stranger" is
# not in allowed_signers.
set -euo pipefail

out=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

ssh-keygen -q -t ed25519 -N '' -C trusted -f "$tmp/trusted"
ssh-keygen -q -t ed25519 -N '' -C stranger -f "$tmp/stranger"
echo "trusted@test namespaces=\"git\" $(cut -d' ' -f1,2 "$tmp/trusted.pub")" >"$out/allowed_signers"

g() {
  git -C "$tmp/repo" -c user.name=test -c user.email=test@test \
    -c gpg.format=ssh -c commit.gpgsign=false -c tag.gpgsign=false "$@"
}
commit() { g add -A && g commit -q -m "$1"; }
tag() { g -c user.signingkey="$tmp/$1.pub" tag -s -m "$2" "$2"; }
T1=catalog-2026.01.10.1 T2=catalog-2026.02.10.1 T3=catalog-2026.03.10.1 T4=catalog-2026.03.10.2

recipe() { mkdir -p "$tmp/repo/recipes/$1" && cat >"$tmp/repo/recipes/$1/recipe.toml"; }

git init -q -b main "$tmp/repo"

recipe steam <<'EOF'
name = "steam"
description = "Valve's Steam client"
kind = "upstream-binary"
version = "1.0.0.85"
maintainer = "test"
debian = [13]
requires = ["i386", "components"]

[[source]]
url = "https://repo.steampowered.com/steam/archive/stable/steam_1.0.0.85.tar.gz"
sha256 = "1111111111111111111111111111111111111111111111111111111111111111"
EOF
recipe zram <<'EOF'
name = "zram"
description = "Compressed swap in RAM"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["systemd-zram-generator"]

[[file]]
path = "/etc/systemd/zram-generator.conf.d/dovy.conf"
content = """
[zram0]
zram-size = ram / 2
"""
EOF
recipe firefox <<'EOF'
name = "firefox"
description = "Mozilla Firefox, from Mozilla's repository"
kind = "vendor-repository"
version = "145.0"
maintainer = "test"
debian = [13]

[repository]
uri = "https://packages.mozilla.org/apt"
suite = "mozilla"
components = ["main"]
key-url = "https://packages.mozilla.org/apt/repo-signing-key.gpg"
fingerprint = "35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3"
packages = ["firefox"]
EOF
g add -A
commit "three recipes"
tag trusted $T1

# v2: steam updated, discord added, zram dropped
sed -i 's/1\.0\.0\.85/1.0.0.86/g; s/1111/2222/g' "$tmp/repo/recipes/steam/recipe.toml"
g rm -q -r recipes/zram
recipe discord <<'EOF'
name = "discord"
description = "Discord chat"
kind = "upstream-binary"
version = "0.0.112"
maintainer = "test"
debian = [13]

[[source]]
url = "https://dl.discordapp.net/apps/linux/0.0.112/discord-0.0.112.deb"
sha256 = "3333333333333333333333333333333333333333333333333333333333333333"
EOF
g add -A
commit "steam 1.0.0.86, discord added, zram dropped"
tag trusted $T2

# Newer than T2 but not trusted: an unsigned tag and one signed by a stranger.
echo "# unsigned" >>"$tmp/repo/recipes/steam/recipe.toml"
commit unsigned
g tag -a -m $T3 $T3
echo "# stranger" >>"$tmp/repo/recipes/steam/recipe.toml"
commit stranger
tag stranger $T4

bundle() { name=$1; shift; g bundle create -q "$out/catalog-$name.bundle" "${@/#/refs/tags/}"; }
bundle v1 $T1
bundle v2 $T1 $T2
bundle mixed $T1 $T2 $T3 $T4
bundle bad $T3 $T4

# install: real recipes for the integration tests, plus "surprise", whose
# postfix makes apt remove an installed exim4.
g checkout -q --orphan install
g rm -q -r -f .
recipe pfetch <<'EOF'
name = "pfetch"
description = "Pretty system information tool, Rust rewrite of pfetch"
kind = "upstream-binary"
version = "3.1.1"
maintainer = "test"
debian = [13]

[[source]]
url = "https://github.com/Gobidev/pfetch-rs/releases/download/v3.1.1/pfetch-linux-musl-x86_64.tar.gz"
sha256 = "1816aa30f763856403b1a4d91f73ffd79e96d3a0a5cef6f305f025253f6a3a6c"

[install]
"/usr/bin/pfetch" = "pfetch"
EOF
recipe zram <<'EOF'
name = "zram"
description = "Compressed swap in RAM"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["systemd-zram-generator"]

[[file]]
path = "/etc/systemd/zram-generator.conf.d/00-dovy.conf"
content = """
[zram0]
zram-size = min(ram / 2, 8192)
compression-algorithm = zstd
"""
EOF
recipe cli-tools <<'EOF'
name = "cli-tools"
description = "Command-line search and viewing tools from Debian"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["ripgrep", "fd-find", "bat"]
EOF
recipe surprise <<'EOF'
name = "surprise"
description = "Pulls postfix, which conflicts with exim4"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["postfix"]
EOF
mesa='packages = ["mesa-vulkan-drivers", "libgl1-mesa-dri", "libglx-mesa0", "libegl-mesa0", "libgbm1", "mesa-va-drivers", "mesa-libgallium"]'
recipe mesa <<EOF
name = "mesa"
description = "Mesa graphics drivers from Debian stable"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
conflicts = ["mesa-backports"]
$mesa
EOF
recipe mesa-backports <<EOF
name = "mesa-backports"
description = "Mesa graphics drivers from Debian backports"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
conflicts = ["mesa"]
backports = true
$mesa
EOF
recipe kernel-backports <<'EOF'
name = "kernel-backports"
description = "Debian's signed kernel from backports"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
backports = true
packages = ["linux-image-amd64", "linux-headers-amd64"]
EOF
commit "install recipes"
T5=catalog-2026.04.10.1
tag trusted $T5
bundle install $T5

rm -f "$out/catalog.bundle"
echo "wrote $out/catalog-{v1,v2,mixed,bad,install}.bundle and $out/allowed_signers"
