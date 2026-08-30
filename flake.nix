{
  description = "fn - a small terminal-based LLM agent with shell access";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {inherit system;};
    in {
      packages = rec {
        fn-agent = pkgs.buildGoModule {
          pname = "fn-agent";
          version = self.shortRev or "dirty";

          src = ./.;

          modRoot = "src";

          vendorHash = "sha256-ZP04jw/tefXfbsvQgPoXreugDOLBshcYjoNBccIwa5U=";

          subPackages = ["cmd/fn"];

          nativeBuildInputs = [pkgs.makeWrapper pkgs.python3];

          postInstall = ''
            wrapProgram $out/bin/fn --prefix PATH : ${pkgs.lib.makeBinPath [pkgs.python3]}
          '';

          meta = with pkgs.lib; {
            description = "A small terminal-based LLM agent with shell access";
            homepage = "https://github.com/Baitinq/fn-agent";
            license = licenses.bsd2;
            maintainers = [];
            platforms = platforms.unix;
            mainProgram = "fn";
          };
        };

        default = fn-agent;
      };

      devShells.default = pkgs.mkShell {
        packages = [pkgs.go];
      };
    });
}
