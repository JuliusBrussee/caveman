# home-manager module for caveman (Claude Code integration)
#
# Tested: NixOS 24.11+, Claude Code 2.x, home-manager 24.11
# Untested: other agent integrations (Cursor, Windsurf, Gemini CLI, Codex)
#           — community contributions welcome.
#
# Usage in flake.nix:
#
#   inputs.caveman.url = "github:JuliusBrussee/caveman";
#   inputs.caveman.inputs.nixpkgs.follows = "nixpkgs";
#
#   home-manager.sharedModules = [ inputs.caveman.homeManagerModules.caveman ];
#
# Then in any home-manager config:
#
#   programs.caveman.enable = true;

self:
{ config, pkgs, lib, ... }:

let
  cfg = config.programs.caveman;
  cavemanPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
in
{
  options.programs.caveman = {
    enable = lib.mkEnableOption "caveman skills and tools for Claude Code";

    installHooks = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Copy Claude Code hook files into ~/.claude/hooks/:
          - caveman-activate.js    (SessionStart — injects caveman context)
          - caveman-mode-tracker.js (UserPromptSubmit — tracks /caveman mode)
          - caveman-statusline.sh   (statusline badge)

        NOTE: Copying hook files is not sufficient. Claude Code discovers
        hooks through ~/.claude/settings.json. You must also register them
        there — run hooks/install.sh from the caveman repo, or manually add
        the SessionStart/UserPromptSubmit entries and set statusLine.command.

        Disable if you manage ~/.claude/hooks/ and settings.json manually.
      '';
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      # caveman-compress CLI — compresses natural language files to save tokens.
      # Falls back to `claude --print` if ANTHROPIC_API_KEY is not set.
      home.packages = [ cavemanPkgs.caveman-compress ];

      # Skills — SKILL.md files resolved from canonical sources (no symlinks).
      home.file.".claude/skills/caveman/SKILL.md".source =
        "${cavemanPkgs.skills}/skills/caveman/SKILL.md";
      home.file.".claude/skills/caveman-commit/SKILL.md".source =
        "${cavemanPkgs.skills}/skills/caveman-commit/SKILL.md";
      home.file.".claude/skills/caveman-review/SKILL.md".source =
        "${cavemanPkgs.skills}/skills/caveman-review/SKILL.md";
      home.file.".claude/skills/compress/SKILL.md".source =
        "${cavemanPkgs.skills}/skills/compress/SKILL.md";
    }

    (lib.mkIf cfg.installHooks {
      home.file.".claude/hooks/caveman-activate.js".source =
        "${cavemanPkgs.hooks}/hooks/caveman-activate.js";
      home.file.".claude/hooks/caveman-mode-tracker.js".source =
        "${cavemanPkgs.hooks}/hooks/caveman-mode-tracker.js";
      home.file.".claude/hooks/caveman-statusline.sh".source =
        "${cavemanPkgs.hooks}/hooks/caveman-statusline.sh";
    })
  ]);
}
