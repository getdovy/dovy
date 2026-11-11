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
repository = "mozilla"
packages = ["firefox"]
EOF
mkdir -p "$tmp/repo/repositories"
cat >"$tmp/repo/repositories/mozilla.toml" <<'EOF'
name = "mozilla"
uri = "https://packages.mozilla.org/apt"
suite = "mozilla"
components = ["main"]
key-url = "https://packages.mozilla.org/apt/repo-signing-key.gpg"
fingerprints = ["35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3"]
maintainer = "test"
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
buildsh() { cat >"$tmp/repo/recipes/$1/build.sh"; }
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
# Source builds (milestone 7): yad upstream, btop from testing.
recipe yad <<'EOF'
name = "yad"
description = "Dialogs for shell scripts"
kind = "upstream-build"
version = "14.2"
maintainer = "test"
debian = [13]
replaces = ["yad"]
build-depends = ["autoconf", "automake", "autopoint", "gettext", "pkgconf", "libgtk-3-dev"]
packages = ["libgtk-3-0t64"]

[[source]]
url = "https://github.com/v1cont/yad/archive/refs/tags/v14.2.tar.gz"
sha256 = "6748c0ecb917043326cc70646f143890151dea2fc50db5fe54439de6dc04b1e3"
EOF
buildsh yad <<'EOF'
cd yad-14.2
autoreconf -ivf
./configure --prefix=/usr --disable-schemas-compile
make
make install
EOF
recipe btop <<'EOF'
name = "btop"
description = "Resource monitor"
kind = "testing-rebuild"
version = "1.4.7"
maintainer = "test"
debian = [13]
replaces = ["btop"]
source-package = "btop"

[[source]]
url = "https://deb.debian.org/debian/pool/main/b/btop/btop_1.4.7-1.dsc"
sha256 = "652ba17beb2e92f83d66dd49b00fbc1adc66f08b2bea07c0b93a5c890d913cb0"
EOF
# offline: a build that reaches for the network without saying why.
recipe offline <<'EOF'
name = "offline"
description = "Fails: its build needs the network"
kind = "upstream-build"
version = "14.2"
maintainer = "test"
debian = [13]

[[source]]
url = "https://github.com/v1cont/yad/archive/refs/tags/v14.2.tar.gz"
sha256 = "6748c0ecb917043326cc70646f143890151dea2fc50db5fe54439de6dc04b1e3"
EOF
buildsh offline <<'EOF'
getent hosts deb.debian.org
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
recipe steam <<'EOF'
name = "steam"
description = "Valve's Steam client, from Debian contrib"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
requires = ["components"]
packages = ["steam-installer", "mesa-vulkan-drivers", "libgl1-mesa-dri:i386", "libglx-mesa0:i386", "mesa-vulkan-drivers:i386"]
EOF
recipe discord <<'EOF'
name = "discord"
description = "Discord chat, from Discord's .deb"
kind = "upstream-binary"
version = "1.0.168"
maintainer = "test"
debian = [13]

[[source]]
url = "https://dl.discordapp.net/apps/linux/1.0.168/discord-1.0.168.deb"
sha256 = "93d43ad090825b9edcacfb3841506ea51e7040c60d93a9e377d0caca547bea28"

[scripts]
postinst = """
#!/bin/sh
set -e
if [ "$1" = configure ]; then
	systemctl reload apparmor 2>/dev/null || true
	update-desktop-database -q /usr/share/applications 2>/dev/null || true
fi
"""
EOF
fw='packages = ["firmware-misc-nonfree", "firmware-amd-graphics", "firmware-intel-graphics", "firmware-iwlwifi", "firmware-realtek", "firmware-atheros", "firmware-mediatek", "firmware-brcm80211", "firmware-intel-sound"]'
recipe firmware <<EOF
name = "firmware"
description = "Common hardware firmware from Debian stable"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
requires = ["components"]
conflicts = ["firmware-backports"]
$fw
EOF
recipe firmware-backports <<EOF
name = "firmware-backports"
description = "Common hardware firmware from Debian backports"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
requires = ["components"]
conflicts = ["firmware"]
backports = true
$fw
EOF
repository() { mkdir -p "$tmp/repo/repositories" && cat >"$tmp/repo/repositories/$1.toml"; }
vendor() { # name repository package
  recipe "$1" <<EOF
name = "$1"
description = "$1, from a vendor repository"
kind = "vendor-repository"
version = "1"
maintainer = "test"
debian = [13]
repository = "$2"
packages = ["$3"]
EOF
}
repository mozilla <<'EOF'
name = "mozilla"
uri = "https://packages.mozilla.org/apt"
suite = "mozilla"
components = ["main"]
key-url = "https://packages.mozilla.org/apt/repo-signing-key.gpg"
fingerprints = ["35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3"]
maintainer = "test"
EOF
repository google-chrome <<'EOF'
name = "google-chrome"
uri = "https://dl.google.com/linux/chrome/deb/"
suite = "stable"
components = ["main"]
key-url = "https://dl.google.com/linux/linux_signing_key.pub"
fingerprints = ["EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796"]
maintainer = "test"

[[file]]
path = "/etc/default/google-chrome"
content = """
repo_add_once="false"
repo_reenable_on_distupgrade="false"
"""
EOF
repository brave <<'EOF'
name = "brave"
uri = "https://brave-browser-apt-release.s3.brave.com/"
suite = "stable"
components = ["main"]
key-url = "https://brave-browser-apt-release.s3.brave.com/brave-browser-archive-keyring.gpg"
# Brave's key file holds three keys (2022, 2025, 2025).
fingerprints = [
  "DBF1A116C220B8C7164F98230686B78420038257",
  "47D32A74E9A9E013A4B4926C68D513D36A73CD96",
  "B2A3DCA350E67256740DF904DE4EC67BE4B0DCA0",
]
maintainer = "test"

# Brave's postinst and daily cron add Brave's own source unless these say
# not to; dovy's repository package already provides it (2.6).
[[file]]
path = "/etc/default/brave-browser"
content = """
repo_add_once="false"
repo_reenable_on_distupgrade="false"
"""

[[file]]
path = "/etc/default/brave-origin"
content = """
repo_add_once="false"
repo_reenable_on_distupgrade="false"
"""
EOF
# badkey: Microsoft's repository with a wrong fingerprint.
repository microsoft <<'EOF'
name = "microsoft"
uri = "https://packages.microsoft.com/repos/code"
suite = "stable"
components = ["main"]
key-url = "https://packages.microsoft.com/keys/microsoft.asc"
fingerprints = ["0000000000000000000000000000000000000000"]
maintainer = "test"
EOF
repository librewolf <<'EOF'
name = "librewolf"
uri = "https://repo.librewolf.net"
suite = "librewolf"
components = ["main"]
key-url = "https://repo.librewolf.net/pubkey.gpg"
fingerprints = ["662E3CDD6FE329002D0CA5BB40339DD82B12EF16"]
maintainer = "test"
EOF
repository floorp <<'EOF'
name = "floorp"
uri = "https://ppa.floorp.app/amd64"
suite = "./"
key-url = "https://ppa.floorp.app/KEY.gpg"
fingerprints = ["A8C20FCDD57739941A813D5D8F64DCC7C806D572"]
maintainer = "test"
EOF
repository mullvad <<'EOF'
name = "mullvad"
uri = "https://repository.mullvad.net/deb/stable"
suite = "stable"
components = ["main"]
key-url = "https://repository.mullvad.net/deb/mullvad-keyring.asc"
fingerprints = ["A1198702FC3E0A09A9AE5B75D5A1D4F266DE8DDF"]
maintainer = "test"
EOF
repository opera <<'EOF'
name = "opera"
uri = "https://deb.opera.com/opera-stable/"
suite = "stable"
components = ["non-free"]
key-url = "https://deb.opera.com/archive.key"
fingerprints = ["6C86BE214648376680CA957B11EE8C00B693A745"]
maintainer = "test"
# Opera's postinst makes apt trust Opera's keys for every repository
# (trusted.gpg.d, a daily cron job): never given to apt, repacked instead.
repack = true
EOF
recipe librewolf <<'EOF'
name = "librewolf"
description = "LibreWolf browser, from LibreWolf's repository"
kind = "vendor-repository"
version = "1"
maintainer = "test"
debian = [13]
repository = "librewolf"
packages = ["librewolf"]
EOF
recipe floorp <<'EOF'
name = "floorp"
description = "Floorp browser, from Floorp's repository"
kind = "vendor-repository"
version = "1"
maintainer = "test"
debian = [13]
repository = "floorp"
packages = ["floorp"]
EOF
recipe mullvad-browser <<'EOF'
name = "mullvad-browser"
description = "Mullvad Browser, from Mullvad's repository"
kind = "vendor-repository"
version = "1"
maintainer = "test"
debian = [13]
repository = "mullvad"
packages = ["mullvad-browser"]
EOF
recipe helium-bin <<'EOF'
name = "helium-bin"
description = "Helium browser, from Helium's .deb"
kind = "upstream-binary"
version = "0.21.1.1"
maintainer = "test"
debian = [13]

[[source]]
url = "https://github.com/imputnet/helium-linux/releases/download/0.21.1.1/helium-bin_0.21.1.1-1_amd64.deb"
sha256 = "679a88ea17e969653e09504a1d01bce155f4e3d2ff366d2571d6d00b985457cc"

# Helium's scripts chown files already root's, refresh caches Debian's
# triggers refresh, and install an AppArmor profile Debian does not need.
[scripts]
postinst = ""
postrm = ""
EOF
recipe opera-stable <<'EOF'
name = "opera-stable"
description = "Opera browser, repacked from Opera's repository"
kind = "vendor-repository"
version = "139.0.6053.38"
maintainer = "test"
debian = [13]
repository = "opera"
packages = ["opera-stable"]

# Opera's postinst, without the cron job, keys and source (ADR 0014).
# Opera's prerm (alternatives) and postrm are kept; its debconf question
# about adding a source goes with config and templates.
[scripts]
postinst = """
#!/bin/sh
set -e
if [ "$1" = configure ]; then
	chown root:root /usr/lib/x86_64-linux-gnu/opera-stable/opera_sandbox
	chmod 4755 /usr/lib/x86_64-linux-gnu/opera-stable/opera_sandbox
	update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/bin/opera 120 \\
		--slave /usr/share/man/man1/x-www-browser.1.gz x-www-browser.1.gz /usr/share/man/man1/opera.1.gz
	update-alternatives --install /usr/bin/gnome-www-browser gnome-www-browser /usr/bin/opera 120 \\
		--slave /usr/share/man/man1/gnome-www-browser.1.gz gnome-www-browser.1.gz /usr/share/man/man1/opera.1.gz
fi
"""
config = ""
templates = ""
EOF
recipe opera-gx-stable <<'EOF'
name = "opera-gx-stable"
description = "Opera GX browser, repacked from Opera's repository"
kind = "vendor-repository"
version = "139.0.6053.42"
maintainer = "test"
debian = [13]
repository = "opera"
packages = ["opera-gx-stable"]

# Opera's postinst, without the cron job, keys and source (ADR 0014).
# Opera's prerm (alternatives) and postrm are kept; its debconf question
# about adding a source goes with config and templates.
[scripts]
postinst = """
#!/bin/sh
set -e
if [ "$1" = configure ]; then
	chown root:root /usr/lib/x86_64-linux-gnu/opera-gx-stable/opera_sandbox
	chmod 4755 /usr/lib/x86_64-linux-gnu/opera-gx-stable/opera_sandbox
	update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/bin/opera-gx 120 \\
		--slave /usr/share/man/man1/x-www-browser.1.gz x-www-browser.1.gz /usr/share/man/man1/opera-gx.1.gz
	update-alternatives --install /usr/bin/gnome-www-browser gnome-www-browser /usr/bin/opera-gx 120 \\
		--slave /usr/share/man/man1/gnome-www-browser.1.gz gnome-www-browser.1.gz /usr/share/man/man1/opera-gx.1.gz
fi
"""
config = ""
templates = ""
EOF
repository nvidia <<'EOF'
name = "nvidia"
uri = "https://developer.download.nvidia.com/compute/cuda/repos/debian13/x86_64"
suite = "/"
key-url = "https://developer.download.nvidia.com/compute/cuda/repos/debian13/x86_64/8793F200.pub"
fingerprints = ["02182E60104FCDC26EAE1B8597A5D4CB8793F200"]
maintainer = "test"
pin = false
EOF
recipe nvidia <<'EOF'
name = "nvidia"
description = "NVIDIA's open driver, from NVIDIA's repository"
kind = "vendor-repository"
version = "1"
maintainer = "test"
debian = [13]
hardware = "nvidia-turing"
secure-boot = "dkms"
repository = "nvidia"
packages = ["nvidia-open", "nvidia-driver-libs:i386", "linux-headers-amd64"]
EOF
recipe nvflux <<'EOF'
name = "nvflux"
description = "Needs nvidia; proves inherited hardware checks"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
depends = ["nvidia"]

[[file]]
path = "/etc/modprobe.d/dovy-nvflux.conf"
content = """
# test
"""
EOF
vendor firefox mozilla firefox
echo 'replaces = ["firefox-esr"]' >>"$tmp/repo/recipes/firefox/recipe.toml"
# clash: Mozilla's firefox without replaces, refused where firefox-esr is.
vendor clash mozilla firefox
vendor google-chrome google-chrome google-chrome-stable
vendor brave-browser brave brave-browser
vendor brave-origin brave brave-origin
vendor badkey microsoft code
recipe config-pipewire <<'EOF'
name = "config-pipewire"
description = "PipeWire audio, playing common sample rates without resampling"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["pipewire-audio"]

[[file]]
path = "/etc/pipewire/pipewire.conf.d/00-dovy.conf"
content = """
# Rates the device may switch to, so 44.1 kHz and 96 kHz audio play
# untouched instead of being resampled to 48 kHz.
context.properties = {
    default.clock.allowed-rates = [ 44100 48000 88200 96000 ]
}
"""
EOF
recipe power-profiles <<'EOF'
name = "power-profiles"
description = "Power profiles (performance, balanced, power saver) for the desktop"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
conflicts = ["tuned-profiles"]
packages = ["power-profiles-daemon"]
EOF
recipe tuned-profiles <<'EOF'
name = "tuned-profiles"
description = "Power profiles from tuned, with a desktop performance profile"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
conflicts = ["power-profiles"]
replaces = ["power-profiles-daemon"]
packages = ["tuned-ppd"]

# The desktop's Performance profile maps to tuned's throughput-performance,
# a server profile. This one replaces it with CPU and firmware settings only.
[[file]]
path = "/etc/tuned/profiles/throughput-performance/tuned.conf"
content = """
[main]
summary=Desktop performance (dovy): CPU, firmware and SATA links at performance; memory, disk and network as Debian sets them

# Debian's server profile also sets min_perf_pct=100 (every core held at
# full clock: heat and fan noise), vm.swappiness=10 (works against zram),
# dirty-page ratios, readahead and somaxconn. They are left out here.
[cpu]
governor=performance
energy_perf_bias=performance
energy_performance_preference=performance
boost=1

[acpi]
platform_profile=performance

[video]
panel_power_savings=0

# SATA links at full power: no wake-up delay on SATA disks (NVMe is not
# affected). Balanced and power saver keep Debian's link power saving.
[scsi_host]
alpm=max_performance
"""
EOF
recipe archivers <<'EOF'
name = "archivers"
description = "Archive tools: 7z, RAR, zip and zstd"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
requires = ["components"]
packages = ["7zip", "7zip-rar", "zip", "unzip", "zstd"]
EOF
recipe config-bash <<'EOF'
name = "config-bash"
description = "Bash defaults: larger history, completion"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["bash-completion"]
user-notes = "grep -qxF '[ -r /etc/dovy/bashrc.d/00-dovy.sh ] && . /etc/dovy/bashrc.d/00-dovy.sh' ~/.bashrc || echo '[ -r /etc/dovy/bashrc.d/00-dovy.sh ] && . /etc/dovy/bashrc.d/00-dovy.sh' >> ~/.bashrc"

[[file]]
path = "/etc/dovy/bashrc.d/00-dovy.sh"
content = """
# History: larger, without duplicates, appended rather than overwritten,
# and written after every command so no terminal loses it.
HISTSIZE=10000
HISTFILESIZE=20000
HISTCONTROL=ignoreboth:erasedups
shopt -s histappend
PROMPT_COMMAND="history -a${PROMPT_COMMAND:+; $PROMPT_COMMAND}"
# Line wrapping follows window resizes.
shopt -s checkwinsize
"""
EOF
recipe dovy-defaults <<'EOF'
name = "dovy-defaults"
description = "System baseline: kernel, firmware and Mesa from backports, zram, audio, power, tools"
kind = "bundle"
version = "1"
maintainer = "test"
debian = [13]
members = ["kernel-backports", "firmware-backports", "mesa-backports", "zram", "config-pipewire", "power-profiles", "archivers", "cli-tools", "config-bash"]
EOF
commit "install recipes"
T5=catalog-2026.04.10.1
tag trusted $T5
bundle install-old $T5

# T6, for upgrade: zram 2 (content changed), pfetch dropped (orphaned),
# cli-tools succeeded by cli-tools-next, and a new dovy-defaults member.
sed -i 's/^version = "1"$/version = "2"/; s/min(ram \/ 2, 8192)/min(ram \/ 2, 16384)/' "$tmp/repo/recipes/zram/recipe.toml"
g rm -q -r recipes/pfetch
echo 'successor = "cli-tools-next"' >>"$tmp/repo/recipes/cli-tools/recipe.toml"
recipe cli-tools-next <<'EOF'
name = "cli-tools-next"
description = "Command-line search and viewing tools, with eza"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["ripgrep", "fd-find", "bat", "eza"]
EOF
recipe emoji <<'EOF'
name = "emoji"
description = "Colour emoji font"
kind = "setup"
version = "1"
maintainer = "test"
debian = [13]
packages = ["fonts-noto-color-emoji"]
EOF
sed -i 's/^version = "1"$/version = "2"/; s/"config-bash"\]/"config-bash", "emoji"]/' "$tmp/repo/recipes/dovy-defaults/recipe.toml"
commit "upgrade test release"
T6=catalog-2026.04.11.1
tag trusted $T6
bundle install $T5 $T6

rm -f "$out/catalog.bundle"
echo "wrote $out/catalog-{v1,v2,mixed,bad,install-old,install}.bundle and $out/allowed_signers"
