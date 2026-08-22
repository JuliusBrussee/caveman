"""Hermes plugin, config/state, hook, mode-command, and init contracts."""
from __future__ import annotations

import argparse
import copy
from concurrent.futures import ThreadPoolExecutor
import hashlib
import importlib.util
import inspect
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
MODES = {"lite", "full", "ultra", "wenyan-lite", "wenyan-full", "wenyan-ultra"}
SKILLS = {"caveman", "caveman-commit", "caveman-review", "caveman-help", "caveman-stats", "caveman-compress", "cavecrew"}
TOOLS = {"caveman_stats", "caveman_prepare_compression", "caveman_apply_compression"}
HOOKS = {"on_session_start", "pre_llm_call", "post_llm_call"}


def manifest(path: Path) -> dict:
    data, current = {}, None
    for raw in path.read_text(encoding="utf-8").splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        if raw.startswith("  - ") and current:
            data[current].append(raw[4:].strip().strip("'\""))
        elif ":" in raw and not raw.startswith(" "):
            key, value = raw.split(":", 1)
            key, value = key.strip(), value.strip()
            data[key] = (int(value) if value.isdigit() else value.strip("'\"")) if value else []
            current = None if value else key
    return data


def digest_tree(root: Path) -> dict[str, str]:
    return {
        str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
        for p in sorted(root.rglob("*")) if p.is_file() and not p.is_symlink()
    }


class FakeContext:
    def __init__(self):
        self.tools, self.hooks, self.skills = {}, {}, {}
        self.commands, self.cli = {}, {}

    def register_tool(self, name, handler, schema, *args, **kwargs):
        self.tools[name] = (handler, schema)

    def register_hook(self, name, callback):
        self.hooks[name] = callback

    def register_skill(self, path, *args, **kwargs):
        path = Path(path)
        self.skills[path.parent.name] = path

    def register_command(self, name, handler, description="", args_hint=None):
        self.commands[name] = handler

    def register_cli_command(self, name, help, setup_fn, handler_fn=None, description=""):
        self.cli[name] = (setup_fn, handler_fn)


def load_plugin():
    name = "caveman_plugin_contract"
    sys.modules.pop(name, None)
    spec = importlib.util.spec_from_file_location(name, ROOT / "__init__.py", submodule_search_locations=[str(ROOT)])
    if not spec or not spec.loader:
        raise RuntimeError("cannot import root plugin")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


class PluginContractTests(unittest.TestCase):
    def test_manifest_declares_only_real_surfaces(self):
        data = manifest(ROOT / "plugin.yaml")
        self.assertEqual(data["name"], "caveman")
        self.assertEqual(data["version"], "1.0.0")
        self.assertEqual(data["kind"], "standalone")
        self.assertTrue(data["description"])
        self.assertNotIn("manifest_version", data)
        self.assertEqual(set(data["provides_tools"]), TOOLS)
        self.assertEqual(set(data["provides_hooks"]), HOOKS)
        self.assertNotIn("provides_commands", data)

    def test_root_import_registers_surfaces_and_seven_canonical_skills(self):
        ctx = FakeContext()
        load_plugin().register(ctx)
        self.assertEqual(set(ctx.tools), TOOLS)
        self.assertEqual(set(ctx.hooks), HOOKS)
        self.assertEqual(set(ctx.skills), SKILLS)
        self.assertEqual(set(ctx.commands), {"caveman", "caveman-init"})
        self.assertEqual(set(ctx.cli), {"caveman"})
        for name, path in ctx.skills.items():
            self.assertEqual(path, ROOT / "skills" / name / "SKILL.md")

    def test_tool_schemas_expose_only_real_model_arguments(self):
        ctx = FakeContext()
        load_plugin().register(ctx)

        stats = ctx.tools["caveman_stats"][1]["parameters"]
        prepare = ctx.tools["caveman_prepare_compression"][1]["parameters"]
        apply = ctx.tools["caveman_apply_compression"][1]["parameters"]

        self.assertEqual(stats, {
            "type": "object",
            "properties": {},
            "additionalProperties": False,
        })
        self.assertEqual(prepare["required"], ["path"])
        self.assertEqual(set(prepare["properties"]), {"path"})
        self.assertEqual(
            apply["required"], ["path", "revision", "proposed_content"]
        )
        self.assertEqual(
            set(apply["properties"]),
            {"path", "revision", "proposed_content"},
        )
        self.assertFalse(prepare["additionalProperties"])
        self.assertFalse(apply["additionalProperties"])

    def test_tool_handlers_return_registry_supported_strings(self):
        ctx = FakeContext()
        load_plugin().register(ctx)

        result = ctx.tools["caveman_prepare_compression"][0](
            {"path": "/missing/caveman-document.md"}, session_id="s"
        )

        self.assertIsInstance(result, str)
        self.assertFalse(json.loads(result)["ok"])

    def test_callbacks_accept_future_kwargs(self):
        ctx = FakeContext()
        load_plugin().register(ctx)
        for name, callback in ctx.hooks.items():
            self.assertTrue(any(p.kind is inspect.Parameter.VAR_KEYWORD for p in inspect.signature(callback).parameters.values()), name)

    def test_import_has_no_write_process_or_network_side_effects(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            for name in ("home", "hermes", "xdg", "work"):
                (root / name).mkdir()
            (root / "home" / "sentinel").write_text("same", encoding="utf-8")
            before = digest_tree(root)
            env = {"HOME": str(root / "home"), "HERMES_HOME": str(root / "hermes"), "XDG_CONFIG_HOME": str(root / "xdg")}
            old = Path.cwd()
            try:
                os.chdir(root / "work")
                with mock.patch.dict(os.environ, env, clear=False), mock.patch("subprocess.Popen", side_effect=AssertionError("process")), mock.patch("subprocess.run", side_effect=AssertionError("process")), mock.patch("socket.socket.connect", side_effect=AssertionError("network")):
                    load_plugin()
            finally:
                os.chdir(old)
            self.assertEqual(digest_tree(root), before)

    @unittest.skipUnless(os.environ.get("CAVEMAN_HERMES_SOURCE_ROOT"), "real Hermes source not configured")
    def test_live_disposable_hermes_discovery(self):
        source = Path(os.environ["CAVEMAN_HERMES_SOURCE_ROOT"]).resolve()
        self.assertTrue((source / "hermes_cli" / "plugins.py").is_file())
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            hh = root / "hermes"
            shutil.copytree(ROOT, hh / "plugins" / "caveman", ignore=shutil.ignore_patterns(".git", "node_modules", "__pycache__", "*.pyc"))
            (hh / "config.yaml").write_text("plugins:\n  enabled:\n    - caveman\n", encoding="utf-8")
            script = "\n".join([
                "import json",
                "from hermes_cli.plugins import PluginManager",
                "m=PluginManager(); m.discover_and_load()",
                "i=next(x for x in m.list_plugins() if x['key']=='caveman')",
                "print(json.dumps({'info':i,'tools':sorted(m._plugin_tool_names),'hooks':sorted(m._hooks),'commands':sorted(m._plugin_commands),'cli':sorted(m._cli_commands),'skills':m.list_plugin_skills('caveman')}))",
            ])
            env = os.environ.copy()
            env.update({"HOME": str(root / "home"), "HERMES_HOME": str(hh), "XDG_CONFIG_HOME": str(root / "xdg"), "PYTHONPATH": str(source), "HERMES_SAFE_MODE": "0"})
            hermes_python = os.environ.get("CAVEMAN_HERMES_PYTHON")
            if not hermes_python and (source / ".venv" / "bin" / "python").is_file():
                hermes_python = str(source / ".venv" / "bin" / "python")
            result = subprocess.run([hermes_python or sys.executable, "-c", script], cwd=root, env=env, text=True, capture_output=True, timeout=30)
            self.assertEqual(result.returncode, 0, result.stderr)
            data = json.loads(result.stdout.strip().splitlines()[-1])
            self.assertTrue(data["info"]["enabled"], data)
            self.assertIsNone(data["info"]["error"], data)
            self.assertEqual(data["info"]["version"], "1.0.0", data)
            self.assertEqual(data["info"]["kind"], "standalone", data)
            self.assertTrue(data["info"]["description"], data)
            self.assertTrue(TOOLS.issubset(data["tools"]), data)
            self.assertTrue(HOOKS.issubset(data["hooks"]), data)
            self.assertEqual(set(data["skills"]), SKILLS)
            self.assertEqual(set(data["commands"]), {"caveman", "caveman-init"})
            self.assertIn("caveman", data["cli"])


class IsolatedCase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.home, self.hh, self.xdg = self.root / "home", self.root / "hermes", self.root / "xdg"
        self.project = self.root / "project with spaces"
        self.home.mkdir(); self.project.mkdir()
        self.env = mock.patch.dict(os.environ, {"HOME": str(self.home), "HERMES_HOME": str(self.hh), "XDG_CONFIG_HOME": str(self.xdg)}, clear=False)
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.temp.cleanup()


class ConfigStateTests(IsolatedCase):
    def setUp(self):
        super().setUp()
        from hermes_caveman import config, state
        self.config, self.state = config, state

    def test_aliases_paths_and_precedence(self):
        self.assertEqual(self.config.normalize_mode("WENYAN"), "wenyan-full")
        self.assertIsNone(self.config.normalize_mode("commit"))
        user = self.xdg / "caveman" / "config.json"; user.parent.mkdir(parents=True); user.write_text('{"defaultMode":"lite"}', encoding="utf-8")
        repo = self.project / ".caveman" / "config.json"; repo.parent.mkdir(); repo.write_text('{"defaultMode":"ultra"}', encoding="utf-8")
        self.assertEqual(self.config.resolve_default_mode(self.project, env={"XDG_CONFIG_HOME": str(self.xdg), "CAVEMAN_DEFAULT_MODE": "wenyan"}), "wenyan-full")
        self.assertEqual(self.config.resolve_default_mode(self.project, env={"XDG_CONFIG_HOME": str(self.xdg)}), "ultra")
        repo.unlink(); self.assertEqual(self.config.resolve_default_mode(self.project, env={"XDG_CONFIG_HOME": str(self.xdg)}), "lite")
        self.assertEqual(self.config.user_config_path(env={}, platform="darwin", home=self.home), self.home / ".config/caveman/config.json")
        self.assertEqual(self.config.user_config_path(env={"APPDATA": "C:/A"}, platform="win32", home=Path("C:/H")), Path("C:/A/caveman/config.json"))

    def test_repo_walk_bound_invalid_config_and_symlink_fallthrough(self):
        deep = self.project / "a" / "b" / "c"; deep.mkdir(parents=True)
        (self.project / ".caveman.json").write_text("{broken", encoding="utf-8")
        self.assertIsNone(self.config.find_repo_config(deep, max_levels=2))
        target = self.root / "mode.json"; target.write_text('{"defaultMode":"ultra"}', encoding="utf-8")
        (deep / ".caveman.json").symlink_to(target)
        self.assertIsNone(self.config.find_repo_config(deep, max_levels=1))
        self.assertEqual(self.config.resolve_default_mode(deep, env={"XDG_CONFIG_HOME": str(self.xdg)}), "full")

    def test_atomic_state_permissions_isolation_and_concurrency(self):
        modes = ["lite", "full", "ultra", "wenyan-full"] * 20
        with ThreadPoolExecutor(max_workers=12) as pool:
            list(pool.map(self.state.write_mode, modes))
        self.assertIn(self.state.read_mode(), set(modes))
        self.assertEqual(stat.S_IMODE(self.state.mode_path().stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.state.mode_path().parent.stat().st_mode), 0o700)
        self.assertFalse(any(self.home.rglob("*")))
        json.loads(self.state.mode_path().read_text(encoding="utf-8"))

    def test_state_parent_and_file_symlinks_rejected(self):
        outside = self.root / "outside"; outside.mkdir(); self.hh.mkdir()
        (self.hh / "caveman").symlink_to(outside, target_is_directory=True)
        with self.assertRaises(self.state.StateSecurityError): self.state.write_mode("full")
        (self.hh / "caveman").unlink(); (self.hh / "caveman").mkdir()
        victim = outside / "victim"; victim.write_text("same", encoding="utf-8")
        self.state.mode_path().symlink_to(victim)
        with self.assertRaises(self.state.StateSecurityError): self.state.write_mode("full")
        self.assertEqual(victim.read_text(encoding="utf-8"), "same")

    def test_concurrent_history_permissions_and_truncated_line(self):
        rows = [{"session": f"s{i}", "mode": "full", "output_tokens": i} for i in range(50)]
        with ThreadPoolExecutor(max_workers=12) as pool: list(pool.map(self.state.append_history, rows))
        with self.state.history_path().open("a", encoding="utf-8") as fh: fh.write('{"bad":')
        self.assertEqual({r["session"] for r in self.state.read_history()}, {r["session"] for r in rows})
        self.assertEqual(stat.S_IMODE(self.state.history_path().stat().st_mode), 0o600)


class HookCommandTests(IsolatedCase):
    def setUp(self):
        super().setUp()
        from hermes_caveman import commands, hooks, lifecycle_helper, state
        self.commands, self.hooks, self.lifecycle_helper, self.state = commands, hooks, lifecycle_helper, state

    def test_session_default_off_and_filtered_reinforcement(self):
        self.hooks.on_session_start(session_id="s1", cwd=str(self.project), future="ok")
        self.assertEqual(self.state.read_mode(), "full")
        for mode in MODES:
            text = self.hooks.render_reinforcement(mode)
            self.assertTrue(text.startswith(f"CAVEMAN MODE ACTIVE — level: {mode}"))
            self.assertIn(f"| **{mode}** |", text)
            self.assertIn("## Auto-Clarity", text)
            for other in MODES - {mode}: self.assertNotIn(f"| **{other}** |", text)
        self.state.clear_ephemeral_state()
        with mock.patch.dict(os.environ, {"CAVEMAN_DEFAULT_MODE": "off"}, clear=False): self.hooks.on_session_start(session_id="s2", cwd=str(self.project))
        self.assertEqual(self.state.read_mode(), "off")
        self.assertIsNone(self.hooks.pre_llm_call(session_id="s2", user_message="hello"))

    def test_natural_switches_and_false_positives(self):
        self.state.write_mode("off")
        with mock.patch.dict(os.environ, {"CAVEMAN_DEFAULT_MODE": "lite"}, clear=False):
            result = self.hooks.pre_llm_call(session_id="s", user_message="Please activate caveman mode", future="ok")
        self.assertEqual(self.state.read_mode(), "lite")
        self.assertIn("level: lite", result["context"])
        self.assertIsNone(self.hooks.pre_llm_call(session_id="s", user_message="turn caveman mode off"))
        self.assertEqual(self.state.read_mode(), "off")
        for text in ["What is caveman mode?", "How do I exit vim normal mode?", "Document `activate caveman mode`.", 'He said "activate caveman mode".', "```text\nactivate caveman mode\n```", "Does caveman lite drop articles?", "Be brief in the summary only."]:
            with self.subTest(text=text): self.assertIsNone(self.hooks.detect_mode_switch(text, cwd=self.project))

    def test_fail_open_concurrency_and_mode_command(self):
        with mock.patch.object(self.state, "read_mode", side_effect=OSError("boom")): self.assertIsNone(self.hooks.pre_llm_call(session_id="s", user_message="hello"))
        with mock.patch("hermes_caveman.hooks.resolve_default_mode", side_effect=OSError("boom")): self.hooks.on_session_start(session_id="s", cwd=str(self.project))
        with ThreadPoolExecutor(max_workers=8) as pool: list(pool.map(lambda p: self.hooks.pre_llm_call(session_id=f"s{p[0]}", user_message=p[1]), enumerate(["use caveman", "stop caveman"] * 10)))
        self.assertIn(self.state.read_mode(), {"full", "off"})
        self.assertEqual(self.hooks.caveman_command("wenyan"), "Caveman mode: wenyan-full")
        self.assertEqual(self.state.read_mode(), "wenyan-full")
        self.assertIn("Unknown mode", self.hooks.caveman_command("banana")); self.assertEqual(self.state.read_mode(), "wenyan-full")

    def parser(self):
        parser = argparse.ArgumentParser(prog="hermes caveman")
        self.commands.setup_caveman_cli(parser)
        return parser

    def run_cli(self, argv):
        args = self.parser().parse_args(argv); old = Path.cwd()
        try: os.chdir(self.project); return self.commands.handle_caveman_cli(args)
        finally: os.chdir(old)

    def test_slash_init_redirect_is_zero_write(self):
        before = digest_tree(self.root)
        self.assertIn("hermes caveman init", self.hooks.caveman_init_command("--force; touch x"))
        self.assertEqual(digest_tree(self.root), before)

    def test_cli_dry_run_project_only_force_and_input_validation(self):
        openclaw = self.root / "openclaw"; openclaw.mkdir(); (openclaw / "SOUL.md").write_text("owner\n", encoding="utf-8")
        with mock.patch.dict(os.environ, {"OPENCLAW_WORKSPACE": str(openclaw)}, clear=False):
            before = digest_tree(self.root); self.assertEqual(self.run_cli(["init", "--dry-run"]), 0); self.assertEqual(digest_tree(self.root), before)
            self.assertEqual(self.run_cli(["init"]), 0)
        expected = {".cursor/rules/caveman.mdc", ".windsurf/rules/caveman.md", ".clinerules/caveman.md", ".github/copilot-instructions.md", ".opencode/AGENTS.md", "AGENTS.md"}
        self.assertEqual({str(p.relative_to(self.project)) for p in self.project.rglob("*") if p.is_file()}, expected)
        self.assertEqual((openclaw / "SOUL.md").read_text(encoding="utf-8"), "owner\n")
        cursor = self.project / ".cursor/rules/caveman.mdc"; cursor.write_text("user", encoding="utf-8")
        self.run_cli(["init", "--only", "cursor"]); self.assertEqual(cursor.read_text(encoding="utf-8"), "user")
        self.run_cli(["init", "--only", "cursor", "--force"]); self.assertIn("Respond terse", cursor.read_text(encoding="utf-8"))
        for argv in (["init", "--only", "openclaw"], ["init", "--only", "cursor;touch-x"], ["init", "--unknown"]):
            with self.subTest(argv=argv), self.assertRaises(SystemExit): self.parser().parse_args(argv)

    def test_lifecycle_snapshot_parser_isolates_and_restores_profile_env(self):
        snapshot = self.root / "snapshot"
        snapshot.mkdir(mode=0o700)
        config_path = snapshot / "config.yaml"
        config_path.write_text("{}\n", encoding="utf-8")
        config_path.chmod(0o600)
        owner_home = self.root / "owner-home"
        owner_home.mkdir(mode=0o700)
        owner_config = self.root / "owner-config.yaml"
        owner_config.write_text("{}\n", encoding="utf-8")
        original = {
            "HERMES_HOME": str(owner_home),
            "HERMES_PROFILE": "owner-profile",
            "HERMES_CONFIG": str(owner_config),
        }

        def read_snapshot():
            self.assertEqual(Path(os.environ["HERMES_HOME"]).resolve(), snapshot.resolve())
            self.assertNotIn("HERMES_PROFILE", os.environ)
            self.assertNotIn("HERMES_CONFIG", os.environ)
            return {"plugins": {"entries": {"caveman": {"allow_tool_override": True}}}}

        with mock.patch.dict(os.environ, original, clear=False):
            with mock.patch("hermes_cli.config.read_raw_config", side_effect=read_snapshot):
                self.assertEqual(
                    self.run_cli([
                        "_lifecycle",
                        "inspect-plugin-override",
                        "--snapshot-home",
                        str(snapshot),
                    ]),
                    0,
                )
            self.assertEqual({key: os.environ[key] for key in original}, original)

    def test_lifecycle_helper_anchors_state_root_and_preserves_history(self):
        home = self.root / "helper-home"
        state_root = home / "caveman"
        (state_root / "sessions" / "nested").mkdir(parents=True)
        (state_root / "sessions" / "nested" / "turn.json").write_text("{}\n", encoding="utf-8")
        (state_root / "mode.json").write_text("{}\n", encoding="utf-8")
        history = state_root / "history.jsonl"
        history.write_text('{"owner":true}\n', encoding="utf-8")

        outcome = self.lifecycle_helper._cleanup_state(home.resolve(), False)
        self.assertEqual(outcome, {"history": "preserved"})
        self.assertFalse((state_root / "sessions").exists())
        self.assertFalse((state_root / "mode.json").exists())
        self.assertEqual(history.read_text(encoding="utf-8"), '{"owner":true}\n')

        victim = self.root / "helper-victim"
        victim.mkdir()
        marker = victim / "owner.txt"
        marker.write_text("owner\n", encoding="utf-8")
        state_root.rename(self.root / "saved-state")
        state_root.symlink_to(victim, target_is_directory=True)
        with self.assertRaises(OSError):
            self.lifecycle_helper._cleanup_state(home.resolve(), True)
        self.assertEqual(marker.read_text(encoding="utf-8"), "owner\n")

    def test_lifecycle_config_mutation_preserves_siblings_and_restores_forced_mcp(self):
        config = {
            "unrelated": {"keep": True},
            "plugins": {
                "enabled": ["sibling", "caveman"],
                "disabled": ["other"],
                "entries": {
                    "caveman": {"allow_tool_override": False},
                    "sibling": {"keep": True},
                },
            },
            "mcp_servers": {
                "sibling": {"command": "keep"},
                "caveman-shrink": {"command": "owner"},
            },
        }
        installed = {"command": "node", "args": ["proxy", "upstream"]}
        previous = self.commands.mutate_lifecycle_config(
            config,
            operation="set-mcp",
            mcp_name="caveman-shrink",
            mcp_entry=installed,
        )
        self.assertEqual(previous, {"command": "owner"})
        self.assertEqual(config["mcp_servers"]["caveman-shrink"], installed)

        self.commands.mutate_lifecycle_config(
            config,
            operation="remove",
            plugin="caveman",
            mcp_name="caveman-shrink",
            expected_mcp=installed,
            restore_mcp=previous,
            restore_present=True,
            expected_plugin_override=False,
            restore_plugin_override_present=True,
            restore_plugin_override=True,
        )
        self.assertEqual(config["plugins"]["enabled"], ["sibling"])
        self.assertEqual(config["plugins"]["disabled"], ["other"])
        self.assertEqual(config["mcp_servers"]["caveman-shrink"], previous)
        self.assertEqual(config["mcp_servers"]["sibling"], {"command": "keep"})
        self.assertIs(config["plugins"]["entries"]["caveman"]["allow_tool_override"], True)
        self.assertEqual(config["plugins"]["entries"]["sibling"], {"keep": True})
        self.assertEqual(config["unrelated"], {"keep": True})

    def test_lifecycle_mcp_mismatch_is_atomic(self):
        config = {
            "plugins": {"enabled": ["caveman", "sibling"], "disabled": []},
            "mcp_servers": {"caveman-shrink": {"command": "user", "args": ["modified"]}},
        }
        before = copy.deepcopy(config)
        with self.assertRaisesRegex(ValueError, "managed MCP entry.*modified"):
            self.commands.mutate_lifecycle_config(
                config,
                operation="remove",
                plugin="caveman",
                mcp_name="caveman-shrink",
                expected_mcp={"command": "node", "args": ["managed"]},
            )
        self.assertEqual(config, before)


if __name__ == "__main__":
    unittest.main()
