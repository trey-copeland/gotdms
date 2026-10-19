#!/usr/bin/env python3
"""Compare gotdms and npTDMS in both directions.

npTDMS is LGPL-2.1 and is used only as a CI oracle. It is not linked or
shipped. This tree has no LabVIEW, so npTDMS is the external anchor.
Strings in these files do not end in NUL: gotdms drops one trailing NUL
on read and npTDMS does not.
"""

import json
import math
import os
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

import numpy as np
from nptdms import ChannelObject, RootObject, TdmsFile, TdmsWriter


def go(*args):
    subprocess.check_call(["go", "run", "./testdata/nptdms", *args])


def go_read(path):
    raw = subprocess.check_output(["go", "run", "./testdata/nptdms", "read", path])
    return json.loads(raw)


def same(got, want, where):
    if type(got) is not type(want) and not (isinstance(got, (int, float)) and isinstance(want, (int, float))):
        raise SystemExit(f"{where}: type {type(got).__name__} != {type(want).__name__} ({got!r} vs {want!r})")
    if isinstance(want, float):
        if not math.isclose(float(got), want, rel_tol=0, abs_tol=0):
            if float(got) != want:
                raise SystemExit(f"{where}: {got!r} != {want!r}")
    elif got != want:
        raise SystemExit(f"{where}: {got!r} != {want!r}")


def compare_docs(got, want, label):
    if set(got) != set(want):
        raise SystemExit(f"{label}: files {sorted(got)} != {sorted(want)}")
    for name, wdoc in want.items():
        gdoc = got[name]
        for key, val in wdoc.get("properties", {}).items():
            if gdoc.get("properties", {}).get(key) != val:
                raise SystemExit(f"{label} {name} property {key}: {gdoc.get('properties')!r}")
        gchs = {(c["group"], c["name"]): c for c in gdoc["channels"]}
        wchs = {(c["group"], c["name"]): c for c in wdoc["channels"]}
        if set(gchs) != set(wchs):
            raise SystemExit(f"{label} {name}: channels {sorted(gchs)} != {sorted(wchs)}")
        for key, wc in wchs.items():
            gc = gchs[key]
            if len(gc["values"]) != len(wc["values"]):
                raise SystemExit(f"{label} {name} {key}: len {len(gc['values'])} != {len(wc['values'])}")
            for i, (gv, wv) in enumerate(zip(gc["values"], wc["values"])):
                same(gv, wv, f"{label} {name} {key}[{i}]")


def read_np(path):
    f = TdmsFile.read(path)
    doc = {"properties": {}, "channels": []}
    props = f.properties
    for key, val in props.items():
        if isinstance(val, str):
            doc["properties"][key] = val
    for g in f.groups():
        for ch in g.channels():
            doc["channels"].append(channel_doc(g.name, ch))
    return doc


def channel_doc(group, ch):
    data = ch[:]
    values = []
    if data.dtype.kind == "M":
        for item in data:
            ns = int(item.astype("datetime64[ns]").astype(np.int64))
            ts = datetime.fromtimestamp(ns / 1e9, tz=timezone.utc)
            frac = ns % 1_000_000_000
            text = ts.strftime("%Y-%m-%dT%H:%M:%S")
            if frac:
                text += f".{frac:09d}".rstrip("0")
            values.append(text + "Z")
    elif data.dtype == object or data.dtype.kind in ("U", "S", "O"):
        for item in data:
            values.append("" if item is None else str(item))
    elif data.dtype == np.bool_:
        values = [bool(x) for x in data]
    else:
        values = [float(x) for x in data]
    return {"group": group, "name": ch.name, "type": "", "values": values}


def main():
    root = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
    os.chdir(root)
    with tempfile.TemporaryDirectory() as tmp:
        go_dir = os.path.join(tmp, "go")
        py_dir = os.path.join(tmp, "py")
        os.makedirs(py_dir)
        go("write", go_dir)
        with open(os.path.join(go_dir, "manifest.json"), encoding="utf-8") as fh:
            manifest = json.load(fh)
        got = {}
        for name in manifest:
            got[name] = read_np(os.path.join(go_dir, name))
        compare_docs(got, manifest, "npTDMS read of gotdms")

        when = np.datetime64("2020-01-02T03:04:05")
        written = {
            "scalars.tdms": {
                "properties": {"title": "from-np"},
                "channels": [
                    {"group": "g", "name": "i32", "values": [-2.0, 4.0, 9.0]},
                    {"group": "g", "name": "f64", "values": [1.5, -2.25]},
                    {"group": "g", "name": "s", "values": ["ab", "", "c,d"]},
                    {"group": "g", "name": "b", "values": [True, False]},
                ],
            },
            "segments.tdms": {
                "properties": {},
                "channels": [{"group": "g", "name": "c", "values": [1.0, 2.0, 3.0]}],
            },
            "time.tdms": {
                "properties": {},
                "channels": [{"group": "g", "name": "t", "values": ["2020-01-02T03:04:05Z"]}],
            },
        }
        # npTDMS 1.13 writes contiguous segments only. Interleaved files are
        # covered in the other direction: gotdms writes one and npTDMS reads it.
        def write(name, objects):
            path = os.path.join(py_dir, name)
            with TdmsWriter(path) as w:
                w.write_segment(objects)

        write("scalars.tdms", [
            RootObject(properties={"title": "from-np"}),
            ChannelObject("g", "i32", np.array([-2, 4, 9], dtype=np.int32)),
            ChannelObject("g", "f64", np.array([1.5, -2.25], dtype=np.float64)),
            ChannelObject("g", "s", np.array(["ab", "", "c,d"], dtype=object)),
            ChannelObject("g", "b", np.array([True, False])),
        ])
        path = os.path.join(py_dir, "segments.tdms")
        with TdmsWriter(path) as w:
            w.write_segment([ChannelObject("g", "c", np.array([1.0, 2.0], dtype=np.float64))])
            w.write_segment([ChannelObject("g", "c", np.array([3.0], dtype=np.float64))])
        write("time.tdms", [
            ChannelObject("g", "t", np.array([when], dtype="datetime64[s]")),
        ])
        compare_docs(go_read(py_dir), written, "gotdms read of npTDMS")
    print("npTDMS differential ok")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as exc:
        sys.exit(exc.returncode)
