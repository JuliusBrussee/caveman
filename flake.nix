# Nix flake for JuliusBrussee/caveman
#
# Compatibility status (contributions welcome for untested agents):
#
# | Component                    | Status      | Tested on                  |
# |------------------------------|-------------|----------------------------|
# | caveman-compress binary      | ✅ works    | NixOS 24.11+, Python 3.13  |
# | Claude Code skills (SKILL.md)| ✅ works    | Claude Code 2.x, NixOS     |
# | Claude Code hooks (JS + sh)  | ✅ works    | Claude Code 2.x, NixOS     |
# | home-manager module          | ✅ works    | home-manager 24.11, NixOS  |
# | Cursor rules                 | 🚧 untested | needs home.file wiring     |
# | Windsurf rules               | 🚧 untested | needs home.file wiring     |
# | Gemini CLI extension         | 🚧 untested | needs Gemini CLI packaging  |
# | Codex plugin                 | 🚧 untested | needs Codex packaging       |
# | npx skills (non-Nix)         | ✅ unaffected| handled by upstream        |
#
# Only Claude Code on NixOS has been tested. Other agent integrations
# are structurally sound but exercise the same file-install pattern —
# if you use Cursor/Windsurf/Gemini on NixOS, please open a PR.

{
  description = "Caveman skills for Claude Code — terse communication + token compression";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        # ── Packages ──────────────────────────────────────────────────────────
        packages = {

          # caveman-compress: compresses natural language files to caveman prose.
          # Uses ANTHROPIC_API_KEY if set; falls back to `claude --print` CLI.
          # CAVEMAN_MODEL overrides model (default: claude-sonnet-4-5).
          caveman-compress = pkgs.python3Packages.buildPythonApplication {
            pname = "caveman-compress";
            version = "1.0.0";
            pyproject = true;
            src = ./caveman-compress;
            build-system = [ pkgs.python3Packages.setuptools ];
            # anthropic and tiktoken are optional — handled gracefully
          };

          # skills: all 4 SKILL.md files in a clean derivation.
          # The source repo uses symlinks for skills/compress/ which the nix
          # store cannot resolve — this derivation copies from canonical sources.
          skills = pkgs.runCommand "caveman-skills" { } ''
            mkdir -p $out/skills/caveman \
                     $out/skills/caveman-commit \
                     $out/skills/caveman-review \
                     $out/skills/compress

            cp ${./skills/caveman/SKILL.md}        $out/skills/caveman/SKILL.md
            cp ${./skills/caveman-commit/SKILL.md} $out/skills/caveman-commit/SKILL.md
            cp ${./skills/caveman-review/SKILL.md} $out/skills/caveman-review/SKILL.md
            cp ${./caveman-compress/SKILL.md}       $out/skills/compress/SKILL.md
          '';

          # hooks: Claude Code hook scripts (tested on NixOS).
          # Install into ~/.claude/hooks/ — the home-manager module does this.
          # All hooks silent-fail on filesystem errors per upstream spec.
          hooks = pkgs.runCommand "caveman-hooks" { } ''
            mkdir -p $out/hooks
            install -m755 ${./hooks/caveman-activate.js}     $out/hooks/caveman-activate.js
            install -m755 ${./hooks/caveman-mode-tracker.js} $out/hooks/caveman-mode-tracker.js
            install -m755 ${./hooks/caveman-statusline.sh}   $out/hooks/caveman-statusline.sh
          '';

          default = self.packages.${system}.caveman-compress;
        };

        # ── Dev shell ─────────────────────────────────────────────────────────
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            python3
            python3Packages.anthropic
            python3Packages.tiktoken
            self.packages.${system}.caveman-compress
          ];
          shellHook = ''
            echo "caveman-compress ready ($(python3 --version))"
          '';
        };

        # ── Checks ────────────────────────────────────────────────────────────
        # Smoke test: verify binary is present and invocable (no network/Claude needed).
        # Full compression requires ANTHROPIC_API_KEY or `claude` CLI — tested manually.
        # Run pytest tests for hook install/uninstall/activate behaviour.
        # Uses temp HOME dirs — no network, no real Claude needed.
        checks.hooks-pytest = pkgs.runCommand "caveman-hooks-pytest" {
          nativeBuildInputs = [ pkgs.bash pkgs.nodejs pkgs.python3 pkgs.python3Packages.pytest ];
          src = ./.;
        } ''
          cp -r $src/. .
          chmod -R u+w .
          python3 -m pytest tests/test_hooks.py -v
          touch $out
        '';

        checks.compress-smoke = pkgs.runCommand "caveman-compress-smoke" {
          buildInputs = [ self.packages.${system}.caveman-compress ];
        } ''
          # No-arg invocation prints usage and exits 1 — capture before set -e kills us.
          output=$(caveman-compress 2>&1 || true)
          echo "$output" | grep -qi "usage\|filepath\|caveman" \
            && echo "OK" > $out \
            || (echo "unexpected output: $output"; exit 1)
        '';
      }
    )

    //

    # ── home-manager module (system-independent) ───────────────────────────
    # Tested: NixOS 24.11+, Claude Code 2.x
    # Usage:  home-manager.sharedModules = [ inputs.caveman.homeManagerModules.caveman ];
    #         Then: programs.caveman.enable = true;
    {
      homeManagerModules.caveman = import ./nix/hm-module.nix self;
    };
}
