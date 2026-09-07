{
  description = "image-viewport.nvim development tools";
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  outputs = { nixpkgs, ... }:
    let
      systems = [ "aarch64-linux" "x86_64-linux" ];
      eachSystem = nixpkgs.lib.genAttrs systems;
    in {
      packages = eachSystem (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          default = assert pkgs.lib.versionAtLeast pkgs.neovim.version "0.12";
            pkgs.buildEnv {
              name = "plugin-development-tools";
              paths = with pkgs; [
                bash coreutils diffutils findutils gawk git gnugrep gnused gnumake
                lua-language-server lua54Packages.luacheck neovim python3
                stylua util-linux cacert
              ];
              pathsToLink = [ "/bin" "/etc/ssl/certs" ];
            };
        });
    };
}
