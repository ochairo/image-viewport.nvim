ARG DEVELOPMENT_BASE=docker.io/nixos/nix@sha256:c7cc6c8cb5d81bed19997247629604708fda95c99c43ac362daa05b6a68e8a24
FROM ${DEVELOPMENT_BASE}
COPY flake.nix flake.lock /toolchain/
RUN nix --extra-experimental-features 'nix-command flakes' build \
      --no-update-lock-file /toolchain#default --out-link /tools \
    && mkdir -p /usr/bin \
    && for tool in /tools/bin/*; do ln -sf "$tool" "/usr/bin/$(basename "$tool")"; done
ENV PATH=/usr/bin:/bin \
    SSL_CERT_FILE=/tools/etc/ssl/certs/ca-bundle.crt \
    GIT_SSL_CAINFO=/tools/etc/ssl/certs/ca-bundle.crt \
    HOME=/tmp/home \
    XDG_CONFIG_HOME=/tmp/config \
    XDG_DATA_HOME=/tmp/data \
    XDG_STATE_HOME=/tmp/state \
    XDG_CACHE_HOME=/tmp/cache \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    NVIM=/usr/bin/nvim \
    LUA_LS=/usr/bin/lua-language-server
COPY scripts/fetch-dependencies.py /dependency-source/scripts/fetch-dependencies.py
COPY dependencies.json /dependency-source/dependencies.json
RUN python3 -I -B /dependency-source/scripts/fetch-dependencies.py /dependencies \
    && chmod -R a+rX /dependencies \
    && nvim --clean --headless -u NONE -i NONE --noplugin \
      -c "lua assert(vim.fn.has('nvim-0.12') == 1)" -c 'qa!'
ENV IMAGE_VIEWPORT_DEPENDENCIES=/dependencies
WORKDIR /workspace
USER 1000:1000
CMD ["make", "check"]
