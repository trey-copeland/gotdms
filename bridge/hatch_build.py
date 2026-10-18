"""Hatchling hook for gotdms wheels and source distributions.

A wheel embeds one gotdms-bridge binary and is tagged for that platform.
The tag is not inferred from a C extension, because the binary is package
data. Cross-compiles set GOTDMS_PLAT_TAG (for example manylinux_2_17_x86_64)
plus GOOS and GOARCH. A source install with those unset tags the wheel for
the host (linux/amd64 -> manylinux_2_17_x86_64, and the four other pairs
the release workflow builds). CGO_ENABLED=0.

An sdist vendors the core module under third_party/gotdms when it can see
that module: the parent checkout, or a local replace such as ./_gotdms.
A source install rewrites ``replace => ../`` onto that directory when the
parent checkout is not the core module, then runs go build.
"""

from __future__ import annotations

import os
import platform
import shutil
import stat
import subprocess
from pathlib import Path

from hatchling.builders.hooks.plugin.interface import BuildHookInterface

# Static binary, so the manylinux tag is a platform label, not a glibc build.
_HOST_TAGS = {
    ("linux", "amd64"): "manylinux_2_17_x86_64",
    ("linux", "arm64"): "manylinux_2_17_aarch64",
    ("darwin", "amd64"): "macosx_11_0_x86_64",
    ("darwin", "arm64"): "macosx_11_0_arm64",
    ("windows", "amd64"): "win_amd64",
}


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version, build_data):
        root = Path(self.root)
        if self.target_name == "sdist":
            self._vendor_core(root, build_data)
            return
        if self.target_name != "wheel":
            return
        binary = self._build_binary(root)
        name = binary.name
        build_data.setdefault("force_include", {})
        build_data["force_include"][str(binary)] = f"gotdms/_bin/{name}"
        plat = os.environ.get("GOTDMS_PLAT_TAG", "").strip() or _default_plat_tag()
        build_data["infer_tag"] = False
        build_data["pure_python"] = False
        build_data["tag"] = f"py3-none-{plat}"

    def _build_binary(self, root: Path) -> Path:
        goos = os.environ.get("GOOS") or _host_goos()
        name = "gotdms-bridge.exe" if goos == "windows" else "gotdms-bridge"
        dest_dir = root / "python" / "src" / "gotdms" / "_bin"
        dest_dir.mkdir(parents=True, exist_ok=True)
        for stale in dest_dir.iterdir():
            if stale.is_file() and stale.name != ".gitignore":
                stale.unlink()
        dest = dest_dir / name
        self._ensure_core_replace(root)
        env = os.environ.copy()
        env["CGO_ENABLED"] = "0"
        subprocess.check_call(
            ["go", "build", "-trimpath", "-o", str(dest), "./cmd/gotdms-bridge"],
            cwd=root,
            env=env,
        )
        dest.chmod(dest.stat().st_mode | stat.S_IEXEC)
        return dest

    def _ensure_core_replace(self, root: Path) -> None:
        """Point a source tree at the vendored core when ../ is not the module.

        A monorepo checkout keeps ``replace => ../``. An extracted checkout
        whose go.mod already replaces a local path (the CI clone) is left
        alone. An sdist unpack still has ``=> ../`` and the vendored copy.
        """
        parent = root.parent / "go.mod"
        if parent.is_file() and _is_core_mod(parent):
            return
        text = (root / "go.mod").read_text(encoding="utf-8")
        if "replace github.com/trey-copeland/gotdms =>" in text and "=> ../" not in text:
            return
        vendor = root / "third_party" / "gotdms" / "go.mod"
        if not vendor.is_file():
            raise RuntimeError(
                "source build needs the gotdms module. The sdist vendors it at "
                "third_party/gotdms. A git checkout uses the parent replace."
            )
        subprocess.check_call(
            ["go", "mod", "edit", "-replace", "github.com/trey-copeland/gotdms=./third_party/gotdms"],
            cwd=root,
        )

    def _vendor_core(self, root: Path, build_data: dict) -> None:
        source = _core_source(root)
        if source is None:
            return
        staging = root / ".packaging" / "gotdms"
        if staging.exists():
            shutil.rmtree(staging)
        staging.mkdir(parents=True)
        _copy_core(source, staging)
        force = build_data.setdefault("force_include", {})
        for path in staging.rglob("*"):
            if path.is_file():
                rel = path.relative_to(staging).as_posix()
                force[str(path)] = f"third_party/gotdms/{rel}"


def _is_core_mod(path: Path) -> bool:
    text = path.read_text(encoding="utf-8")
    return text.startswith("module github.com/trey-copeland/gotdms\n")


def _core_source(root: Path) -> Path | None:
    """Directory of the core module to vendor, if this tree can see one."""
    parent = root.parent
    parent_mod = parent / "go.mod"
    if parent_mod.is_file() and _is_core_mod(parent_mod):
        return parent
    mod = root / "go.mod"
    if not mod.is_file():
        return None
    for line in mod.read_text(encoding="utf-8").splitlines():
        stripped = line.strip()
        if not stripped.startswith("replace github.com/trey-copeland/gotdms"):
            continue
        if "=>" not in stripped:
            continue
        dest = stripped.split("=>", 1)[1].strip()
        if dest in {"../", ".."}:
            continue
        path = (root / dest).resolve()
        if (path / "go.mod").is_file() and _is_core_mod(path / "go.mod"):
            return path
    return None


def _default_plat_tag() -> str:
    goos = os.environ.get("GOOS") or _host_goos()
    goarch = os.environ.get("GOARCH") or _host_goarch()
    tag = _HOST_TAGS.get((goos, goarch))
    if tag:
        return tag
    raise RuntimeError(
        f"set GOTDMS_PLAT_TAG; {goos}/{goarch} has no default wheel tag"
    )


def _host_goarch() -> str:
    machine = platform.machine().lower()
    if machine in {"x86_64", "amd64"}:
        return "amd64"
    if machine in {"aarch64", "arm64"}:
        return "arm64"
    raise RuntimeError(f"set GOTDMS_PLAT_TAG; unknown machine {machine}")


def _host_goos() -> str:
    if os.name == "nt":
        return "windows"
    return "linux" if os.uname().sysname != "Darwin" else "darwin"


def _copy_core(src: Path, dest: Path) -> None:
    skip_dirs = {".git", "bridge", "dist", ".github", "python", ".packaging"}
    for dirpath, dirnames, filenames in os.walk(src):
        dirnames[:] = [name for name in dirnames if name not in skip_dirs and not name.startswith(".")]
        rel_dir = Path(dirpath).relative_to(src)
        if rel_dir.parts[:1] == ("bridge",):
            continue
        target_dir = dest / rel_dir
        target_dir.mkdir(parents=True, exist_ok=True)
        for name in filenames:
            if name.endswith("_test.go"):
                continue
            if rel_dir.parts[:1] == ("examples",):
                continue
            source = Path(dirpath) / name
            if source.suffix not in {".go", ".md"} and name not in {"go.mod", "go.sum", "LICENSE"}:
                continue
            shutil.copy2(source, target_dir / name)
