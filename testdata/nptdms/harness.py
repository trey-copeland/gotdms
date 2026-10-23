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
from nptdms.test.test_daqmx import (
    daqmx_channel_metadata,
    daqmx_scaler_metadata,
    digital_scaler_metadata,
    segment_toc,
)
from nptdms.test.util import (
    GeneratedFile,
    channel_metadata,
    channel_metadata_with_repeated_structure,
    group_metadata,
    hexlify_value,
    root_metadata,
    segment_objects_metadata,
    string_hexlify,
)


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
        daqmx_oracle()
    print("npTDMS differential ok")


def str_prop(text):
    return (0x20, hexlify_value("<I", len(text)) + string_hexlify(text))


def f64_prop(value):
    # tdsTypeDoubleFloat is 10.
    return (10, hexlify_value("<d", value))


def u32_prop(value):
    # tdsTypeU32 is 7. 0xFFFFFFFF is the DAQmx raw-input sentinel.
    return (7, hexlify_value("<I", value))


def write_generated(path, segments):
    generated = GeneratedFile()
    for toc, metadata, data in segments:
        generated.add_segment(toc, metadata, data)
    with open(path, "wb") as fh:
        fh.write(generated._get_contents())
    return generated._get_contents()


def np_raw_doc(path):
    opened = TdmsFile.read(path)
    doc = {"channels": []}
    try:
        for group in opened.groups():
            for ch in group.channels():
                data = ch.raw_data
                values = [] if data is None else [float(x) for x in data]
                doc["channels"].append({"group": group.name, "name": ch.name, "values": values})
    finally:
        opened.close()
    return doc


def compare_raw(got, want, label):
    gchs = {(c["group"], c["name"]): c["values"] for c in got["channels"]}
    wchs = {(c["group"], c["name"]): c["values"] for c in want["channels"]}
    if set(gchs) != set(wchs):
        raise SystemExit(f"{label}: channels {sorted(gchs)} != {sorted(wchs)}")
    for key, wv in wchs.items():
        gv = gchs[key]
        if len(gv) != len(wv):
            raise SystemExit(f"{label} {key}: len {len(gv)} != {len(wv)}")
        for i, (a, b) in enumerate(zip(gv, wv)):
            same(a, b, f"{label} {key}[{i}]")


def daqmx_oracle():
    """Files built by npTDMS test helpers. gotdms is compared to raw_data.

    The helpers write 0x00001269 for format-changing scalers and 0x0000126A
    for digital lines. 0x00001369 is the same format-changing record with the
    index word changed; npTDMS is not asked to parse that word. The comparison
    for it is the raw values npTDMS read from the 0x00001269 file.
    """
    i16 = daqmx_scaler_metadata(0, 3, 0)
    i16_hi = daqmx_scaler_metadata(0, 3, 2)
    meta_i16 = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 4, [2], [i16]),
    )
    data_i16 = "01 00 02 00 FF FF FE FF"
    meta_pair = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 4, [4], [i16]),
        daqmx_channel_metadata("Channel2", 4, [4], [i16_hi]),
    )
    data_pair = "01 00 11 00 02 00 12 00 03 00 13 00 04 00 14 00"
    buf = [4, 4]
    meta_buf = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 4, buf, [daqmx_scaler_metadata(0, 3, 0, 0)]),
        daqmx_channel_metadata("Channel2", 4, buf, [daqmx_scaler_metadata(0, 3, 2, 0)]),
        daqmx_channel_metadata("Channel3", 4, buf, [daqmx_scaler_metadata(0, 3, 0, 1)]),
        daqmx_channel_metadata("Channel4", 4, buf, [daqmx_scaler_metadata(0, 3, 2, 1)]),
    )
    data_buf = (
        "01 00 02 00 03 00 04 00"
        "05 00 06 00 07 00 08 00"
        "09 00 0A 00 0B 00 0C 00"
        "0D 00 0E 00 0F 00 10 00"
    )
    meta_dig = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata(
            "Channel1", 4, [4], [digital_scaler_metadata(0, 0, 0)], digital_line_scaler=True
        ),
    )
    data_dig = "00 00 00 00 01 00 00 00 00 00 00 00 01 00 00 00"
    f64 = daqmx_scaler_metadata(0, 9, 0)
    meta_f64 = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 2, [8], [f64]),
    )
    data_f64 = hexlify_value("<d", 1.5) + hexlify_value("<d", -2.25)
    scale_props = {
        "NI_Scaling_Status": str_prop("unscaled"),
        "NI_Scale[0]_Scale_Type": str_prop("Linear"),
        "NI_Scale[0]_Linear_Slope": f64_prop(2.0),
        "NI_Scale[0]_Linear_Y_Intercept": f64_prop(10.0),
        "NI_Scale[0]_Linear_Input_Source": u32_prop(0xFFFFFFFF),
    }
    meta_scaled = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 3, [2], [i16], properties=scale_props),
    )
    data_scaled = "01 00 02 00 03 00"
    meta_rep = segment_objects_metadata(
        channel_metadata_with_repeated_structure("/'Group'/'Channel1'")
    )
    meta_two = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 2, [2], [i16]),
    )

    with tempfile.TemporaryDirectory() as tmp:
        specs = {
            "i16.tdms": (meta_i16, data_i16),
            "pair.tdms": (meta_pair, data_pair),
            "buffers.tdms": (meta_buf, data_buf),
            "digital.tdms": (meta_dig, data_dig),
            "f64.tdms": (meta_f64, data_f64),
            "scaled.tdms": (meta_scaled, data_scaled),
        }
        for name, (metadata, data) in specs.items():
            write_generated(os.path.join(tmp, name), [(segment_toc(), metadata, data)])
        write_generated(
            os.path.join(tmp, "segments.tdms"),
            [
                (segment_toc(), meta_two, "01 00 02 00"),
                (segment_toc(), meta_rep, "03 00 04 00"),
            ],
        )
        i16_bytes = open(os.path.join(tmp, "i16.tdms"), "rb").read()
        needle = bytes.fromhex("69120000ffffffff")
        found = i16_bytes.count(needle)
        if found != 1:
            raise SystemExit(f"format-changing header pattern occurs {found} times")
        alt = i16_bytes.replace(needle, bytes.fromhex("69130000ffffffff"), 1)
        with open(os.path.join(tmp, "i16-1369.tdms"), "wb") as fh:
            fh.write(alt)

        # The linear scale would turn [1, 2, 3] into [12, 14, 16]. npTDMS
        # raw_data stays [1, 2, 3]. channel[:] cannot apply that scale to a
        # DAQmx raw buffer, so the oracle is raw_data, not the scaled view.
        # A gotdms read that applied the scale would disagree with raw_data.

        want = {}
        for name in list(specs) + ["segments.tdms"]:
            want[name] = np_raw_doc(os.path.join(tmp, name))
        want["i16-1369.tdms"] = want["i16.tdms"]
        got = go_read(tmp)
        for name, wdoc in want.items():
            compare_raw(got[name], wdoc, f"daqmx {name}")
        scaled_got = got["scaled.tdms"]["channels"][0]["values"]
        if scaled_got != [1.0, 2.0, 3.0]:
            raise SystemExit(f"gotdms scaled file returned {scaled_got}, want raw [1, 2, 3]")
    daqmx_layout_oracle()
    daqmx_scale_oracle()


def daqmx_layout_oracle():
    """Layouts npTDMS reads that a stricter reader could reject.

    typed: the DAQmx index data type is the scaler's type (tdsTypeI16) rather
    than 0xFFFFFFFF. stdflag: standard indexes in a segment that sets
    kTocDAQmxRawData. widen: a second segment adds a channel to the buffer,
    so the first channel's stride and the width vector change. padded: the
    raw-data offset points past the end of the object list (zero and 0xFF
    filler), as in NI DAQ captures; npTDMS seeks to the offset.
    """
    i16 = daqmx_scaler_metadata(0, 3, 0)
    typed = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 2, [2], [i16], data_type=2),
    )
    stdflag = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        channel_metadata("/'Group'/'Channel1'", 2, 2),
    )
    first = segment_objects_metadata(
        root_metadata(),
        group_metadata(),
        daqmx_channel_metadata("Channel1", 2, [2], [i16]),
    )
    wider = segment_objects_metadata(
        daqmx_channel_metadata("Channel1", 2, [4], [i16]),
        daqmx_channel_metadata("Channel2", 2, [4], [daqmx_scaler_metadata(0, 3, 2)]),
    )
    with tempfile.TemporaryDirectory() as tmp:
        write_generated(os.path.join(tmp, "typed.tdms"), [(segment_toc(), typed, "07 00 08 00")])
        write_generated(os.path.join(tmp, "stdflag.tdms"), [(segment_toc(), stdflag, "07 00 08 00")])
        write_generated(
            os.path.join(tmp, "widen.tdms"),
            [
                (segment_toc(), first, "01 00 02 00"),
                (segment_toc(), wider, "03 00 1E 00 04 00 28 00"),
            ],
        )
        write_generated(
            os.path.join(tmp, "padded.tdms"),
            [
                (segment_toc(), first + " 00" * 2621, "01 00 02 00"),
                (segment_toc(), wider + " FF" * 3607, "03 00 1E 00 04 00 28 00"),
            ],
        )
        got = go_read(tmp)
        for name in ("typed.tdms", "stdflag.tdms", "widen.tdms", "padded.tdms"):
            compare_raw(got[name], np_raw_doc(os.path.join(tmp, name)), f"daqmx layout {name}")


def daqmx_scale_oracle():
    """gotdms DAQmxScaledFloat64s against npTDMS channel[:] (scaled).

    An NI_Scale index with no Scale_Type is the DAQmx scaler with that scale
    id, the output is index NI_Number_Of_Scales - 1, and each scale reads its
    input source.
    """
    def lin(i, slope, icept, src):
        return {
            f"NI_Scale[{i}]_Scale_Type": str_prop("Linear"),
            f"NI_Scale[{i}]_Linear_Slope": f64_prop(slope),
            f"NI_Scale[{i}]_Linear_Y_Intercept": f64_prop(icept),
            f"NI_Scale[{i}]_Linear_Input_Source": u32_prop(src),
        }

    def poly(i, coeffs, src):
        p = {
            f"NI_Scale[{i}]_Scale_Type": str_prop("Polynomial"),
            f"NI_Scale[{i}]_Polynomial_Coefficients_Size": u32_prop(len(coeffs)),
            f"NI_Scale[{i}]_Polynomial_Input_Source": u32_prop(src),
        }
        for j, c in enumerate(coeffs):
            p[f"NI_Scale[{i}]_Polynomial_Coefficients[{j}]"] = f64_prop(c)
        return p

    def count(n):
        return {"NI_Number_Of_Scales": u32_prop(n), "NI_Scaling_Status": str_prop("unscaled")}

    one = [daqmx_scaler_metadata(0, 3, 0)]
    two = [daqmx_scaler_metadata(0, 3, 0), daqmx_scaler_metadata(1, 3, 2)]
    one_data = "01 00 02 00 03 00"
    two_data = "01 00 0A 00 02 00 14 00 03 00 1E 00"
    cases = {
        "typical.tdms": (one, [2], {**count(2), **lin(1, 2.0, 1.0, 0)}, one_data),
        "chain.tdms": (one, [2], {**count(3), **poly(1, [1.0, 2.0, 3.0], 0), **lin(2, 10.0, 0.0, 1)}, one_data),
        "scaler1.tdms": (two, [4], {**count(3), **lin(2, 2.0, 0.0, 1)}, two_data),
        "order.tdms": (one, [2], {**count(3), **lin(1, 2.0, 0.0, 2), **lin(2, 3.0, 0.0, 0)}, one_data),
        "count.tdms": (two, [4], {**count(2), **lin(0, 2.0, 0.0, 0)}, two_data),
    }
    with tempfile.TemporaryDirectory() as tmp:
        for name, (scalers, widths, props, data) in cases.items():
            meta = segment_objects_metadata(
                root_metadata(),
                group_metadata(),
                daqmx_channel_metadata("Channel1", 3, widths, scalers, properties=props),
            )
            write_generated(os.path.join(tmp, name), [(segment_toc(), meta, data)])
        got = go_read(tmp)
        for name in cases:
            opened = TdmsFile.read(os.path.join(tmp, name))
            try:
                want = [float(x) for x in opened["Group"]["Channel1"][:]]
            finally:
                opened.close()
            ch = got[name]["channels"][0]
            if "scaled_error" in ch:
                raise SystemExit(f"daqmx scale {name}: gotdms error {ch['scaled_error']}")
            if ch["scaled"] != want:
                raise SystemExit(f"daqmx scale {name}: gotdms {ch['scaled']} != npTDMS {want}")
            if ch["values"] != [1.0, 2.0, 3.0]:
                raise SystemExit(f"daqmx scale {name}: Values were scaled: {ch['values']}")


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as exc:
        sys.exit(exc.returncode)
