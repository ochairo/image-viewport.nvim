# Development and verification

Each repository owns its `Containerfile`, `flake.nix`, `flake.lock`, dependency lock,
and Compose service. No sibling checkout or dotfiles installation is required.
Neovim (at least 0.12), LuaLS, StyLua, Luacheck, Python, Make, Git and Linux test
utilities come from the immutable nixpkgs revision in `flake.lock`.

## Build once, test offline

On x86-64 Linux with rootless Podman and podman-compose:

```sh
podman-compose build dev
DEV_UID="$(id -u)" DEV_GID="$(id -g)" podman-compose run --rm dev make check
```

For ARM64 Linux, select the pinned ARM64 base before building:

```sh
export DEVELOPMENT_BASE=docker.io/nixos/nix@sha256:238dfe9a743a6e276e8e04d1db13b978c9bd91741445dec5d733c579596fea79
podman-compose build dev
DEV_UID="$(id -u)" DEV_GID="$(id -g)" podman-compose run --rm dev make check
```

The default base digest is x86-64. Do not use a different architecture's digest or
replace it with a floating tag. The base pins and nixpkgs content hash retain the
extraction's reviewed source provenance; their availability and builds still need
validation in an equipped environment. `--no-update-lock-file` rejects implicit
resolution changes. See [Nix build](https://nix.dev/manual/nix/stable/command-ref/new-cli/nix3-build.html)
and [Docker digest pinning](https://docs.docker.com/build/building/best-practices/#pin-base-image-versions).

Building explicitly uses the network to acquire locked tools and test dependencies.
`make check` never downloads tools or fetches revisions. The acquisition script only
uses public GitHub HTTPS origins with exact commit IDs, disables personal Git config,
hooks and credential prompting, and rejects an existing output directory. It retains
bare Git objects and materializes bounded regular blobs, rejecting links/submodules.
Dependencies keep their original license files; no dependency source is committed here.

The development service mounts only this repository, with private temporary state,
no network, no capabilities and no container-engine socket. Run it with your UID/GID
so formatting edits remain yours. CI uses the same image with the source mount read-only
and runs `make check`; it has no publishing or deployment step. These restrictions do
not certify host sandboxing or protect against all same-user attacks.

Use `podman-compose run --rm dev make format` for authorized formatting, then inspect
the diff and rerun `make check`. The native gate is separate; do not grant a container
privileged mode or disable sandbox checks to obtain a pass.

## Updating pins

Review the official source and integrity metadata for a proposed update, change the
owning lock, rebuild, and run all portable and native checks. Dependency revisions
must remain exact commits. Do not update branches implicitly when a pin is unavailable.
Do not treat a CI definition, source-policy pass or unexecuted test as passing runtime
evidence. Check [verification status](verification.md) for outstanding work.

The image sets `IMAGE_VIEWPORT_DEPENDENCIES=/dependencies`. With local tools, explicitly
run `python3 scripts/fetch-dependencies.py /new/canonical/destination` and set that variable
to the result. `make upstream-test` rematerializes the locked image.nvim Git objects into
private state and checks loader/setup compatibility with synthetic processor/backend effects.
`make native-test` exercises actual image formats, hostile inputs, cancellation and cache
ownership on a configured Linux host. The portable image does not supply or certify that
host's `/usr/bin/magick-im7.q16`, Ghostscript, font configuration or Bubblewrap kernel support.
