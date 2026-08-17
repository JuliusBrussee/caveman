import importlib.util
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("caveman_benchmark", ROOT / "benchmarks" / "run.py")
BENCHMARK = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(BENCHMARK)


class BenchmarkContractTests(unittest.TestCase):
    def test_readme_has_one_replaceable_benchmark_region(self):
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertEqual(readme.count(BENCHMARK.BENCHMARK_START), 1)
        self.assertEqual(readme.count(BENCHMARK.BENCHMARK_END), 1)
        self.assertLess(readme.index(BENCHMARK.BENCHMARK_START), readme.index(BENCHMARK.BENCHMARK_END))

    def test_result_files_are_committable(self):
        ignored = (ROOT / ".gitignore").read_text(encoding="utf-8").splitlines()
        self.assertNotIn("benchmarks/results/*.json", ignored)

    def test_readme_publishes_no_uncommitted_output_percentage_or_accuracy(self):
        readme = (ROOT / "README.md").read_text(encoding="utf-8").lower()
        self.assertNotIn("65% average output", readme)
        self.assertNotIn("technical accuracy    ", readme)

    def test_update_readme_replaces_only_marker_body(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "README.md"
            path.write_text(f"before\n{BENCHMARK.BENCHMARK_START}\nold\n{BENCHMARK.BENCHMARK_END}\nafter\n", encoding="utf-8")
            original = BENCHMARK.README_PATH
            try:
                BENCHMARK.README_PATH = path
                BENCHMARK.update_readme("new table")
            finally:
                BENCHMARK.README_PATH = original
            self.assertEqual(
                path.read_text(encoding="utf-8"),
                f"before\n{BENCHMARK.BENCHMARK_START}\nnew table\n{BENCHMARK.BENCHMARK_END}\nafter\n",
            )


if __name__ == "__main__":
    unittest.main()
