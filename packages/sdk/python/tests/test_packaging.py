from pathlib import Path
import tomllib


def test_distribution_metadata_is_publishable_and_typed() -> None:
    package_root = Path(__file__).resolve().parents[1]
    metadata = tomllib.loads((package_root / "pyproject.toml").read_text())

    assert metadata["build-system"] == {
        "requires": ["setuptools==80.9.0"],
        "build-backend": "setuptools.build_meta",
    }
    assert metadata["project"]["name"] == "caveman-sdk"
    assert metadata["project"]["dependencies"] == []
    assert metadata["tool"]["setuptools"]["packages"] == ["caveman_cloud"]
    assert metadata["tool"]["setuptools"]["package-data"]["caveman_cloud"] == ["py.typed"]
    assert (package_root / "caveman_cloud" / "py.typed").is_file()
