<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/getdovy/dovy/main/.github/logo/dovy-wordmark-dark.svg">
    <img alt="dovy" src="https://raw.githubusercontent.com/getdovy/dovy/main/.github/logo/dovy-wordmark.svg" height="64">
  </picture>
</p>

<p align="center">
  Newer and missing software for Debian stable, on top of apt.<br>
  Not part of Debian.
</p>

<p align="center">
  <a href="https://github.com/getdovy/dovy/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/getdovy/dovy"></a>
  <a href="LICENSE"><img alt="Licence" src="https://img.shields.io/github/license/getdovy/dovy"></a>
</p>

## Overview

dovy installs software Debian stable does not ship, newer versions of
software it does, and system setup such as backports, firmware and drivers.

Each piece of software is a recipe in a signed catalog,
[dovy-catalog](https://github.com/getdovy/dovy-catalog). dovy builds a
Debian package from the recipe on your machine and installs it with apt.
Every change is shown as a plan and confirmed first; dpkg and apt remain
the record of what is installed, and everything dovy installs can be
removed with it.

## Requirements

- Debian 13 (trixie), amd64.
- A user with `sudo`.

## Install

From the [latest release](https://github.com/getdovy/dovy/releases/latest),
download the `.deb` and `SHA256SUMS`, then:

```sh
sha256sum -c SHA256SUMS
sudo apt install ./dovy_*.deb
```

After that, dovy updates itself with `dovy upgrade`.

## Use

```sh
dovy update                 # fetch and verify the catalog
dovy list                   # what the catalog offers
dovy search browser         # by name and description
dovy show firefox           # details of one recipe
dovy install firefox        # plan, confirm, install
dovy upgrade                # one plan for everything
dovy remove firefox         # plan, confirm, remove
```

| Command | Does |
|---|---|
| `install <recipe>...` | plan, confirm, install, with the recipes they depend on |
| `install --reinstall [--restore-config] <recipe>...` | install again what is installed; `--restore-config` also brings back dovy's config files (yours kept as `*.dpkg-old`) |
| `remove <recipe>...` | plan, confirm, remove; Debian packages a recipe replaced come back |
| `update` | fetch the newest trusted catalog release; changes nothing |
| `upgrade` | dovy first if it is outdated, then installed recipes and Debian's own upgrade, in one plan |
| `list [--installed \| --updates \| --orphaned]` | the catalog, or part of it |
| `search <word>` | recipes by name and description |
| `show <recipe> [--recipe \| --diff]` | details, the recipe itself, or what changed since install |
| `check` | report problems; changes nothing |
| `clean` | delete dovy's downloads and builds in `~/.cache/dovy`; they come back when needed |

`man dovy` has the rest.

## How it works

- The catalog is a git repository; each release is a tag signed by the
  project's key. dovy uses the newest release it trusts and can read,
  refuses anything unsigned or edited, and never moves back to an older
  one.
- Downloads are checked against the sha256 in the recipe. Vendor
  repositories are checked against the key fingerprint in the recipe and
  pinned, so they only serve the packages the recipe names.
- Packages are built from the recipe on your machine, source builds in an
  unprivileged sandbox, into a local repository at `/var/lib/dovy/repo`.
  apt installs them like any other package.
- dovy runs as your user and asks for `sudo` only when it needs root.

## Removing dovy

Remove the recipes first, then dovy:

```sh
dovy list --installed
dovy remove <recipe>...
sudo apt purge dovy
```

`purge` also removes dovy's records and logs. Your catalog copy stays in
`~/.local/share/dovy`; run `dovy clean` before the purge to delete the
downloads and builds in `~/.cache/dovy`.

## Limits

- Debian 13, amd64 only.
- Secure Boot: recipes that need it say what to do, such as enrolling
  DKMS's key; dovy itself runs nothing for it.
- Every change is confirmed: there is no `--yes`.

## Building

Needs Go (Debian 13's `golang-go`); `make deb` needs Docker.

```sh
make            # ./dovy
make test       # format, vet, tests
make deb        # dist/dovy_<version>_amd64.deb and SHA256SUMS
```

## Reporting problems

Problems with dovy: [issues](https://github.com/getdovy/dovy/issues).
Recipes, requests and broken recipes:
[dovy-catalog](https://github.com/getdovy/dovy-catalog/issues/new/choose).
Security problems, privately:
[SECURITY.md](https://github.com/getdovy/.github/blob/main/SECURITY.md).

## Licence

GPL-3.0-or-later: [LICENSE](LICENSE). Name and logo:
[TRADEMARKS.md](TRADEMARKS.md).
