"""Frame codec and Arrow IPC decode for gotdms-bridge.

The interactive frame is little-endian:

    magic "GMDS" | version uint16 | flags uint16 | header_len uint32 | payload_len uint32
    header JSON | payload

A multi-channel payload is ``uint32 count`` then repeated ``uint32 len`` plus
one Arrow IPC stream. Sample columns are not JSON and not base64.
"""

from __future__ import annotations

import io
import json
import struct

import numpy as np

from .errors import AlignmentError, ArgumentError, GotdmsError, ProtocolError, UntimedError

MAGIC = b"GMDS"
VERSION = 1
_PREFIX = struct.Struct("<4sHHII")


def write_frame(stream, header: dict) -> None:
    body = json.dumps(header, separators=(",", ":")).encode()
    stream.write(_PREFIX.pack(MAGIC, VERSION, 0, len(body), 0))
    stream.write(body)
    stream.flush()


def read_exact(stream, n: int) -> bytes:
    buf = bytearray()
    while len(buf) < n:
        chunk = stream.read(n - len(buf))
        if not chunk:
            raise ProtocolError("bridge closed the stream")
        buf += chunk
    return bytes(buf)


def read_frame(stream):
    magic, version, _flags, hlen, plen = _PREFIX.unpack(read_exact(stream, _PREFIX.size))
    if magic != MAGIC:
        raise ProtocolError(f"bad magic {magic!r}")
    header = read_exact(stream, hlen) if hlen else b""
    payload = read_exact(stream, plen) if plen else b""
    if version != VERSION:
        raise ProtocolError(f"protocol version {version}, want {VERSION}")
    if not header:
        return {}, payload
    return json.loads(header), payload


def split_payload(payload: bytes) -> list[bytes]:
    if not payload:
        return []
    if len(payload) < 4:
        raise ProtocolError("short payload")
    (count,) = struct.unpack_from("<I", payload, 0)
    off = 4
    parts = []
    for _ in range(count):
        if off + 4 > len(payload):
            raise ProtocolError("short payload length")
        (ln,) = struct.unpack_from("<I", payload, off)
        off += 4
        end = off + ln
        if end > len(payload):
            raise ProtocolError("short payload body")
        parts.append(payload[off:end])
        off = end
    if off != len(payload):
        raise ProtocolError("payload trailing bytes")
    return parts


class Series:
    """One channel read. ``y`` is raw unless this read passed ``scaled=True``."""

    def __init__(self, meta: dict, y: np.ndarray, time_ns, relative_s):
        self.meta = meta
        self.name = meta.get("name", "")
        self.group = meta.get("group", "")
        self.y = y
        self.time_ns = time_ns
        self.relative_s = relative_s
        self.timed = bool(meta.get("timed"))
        self.scaled = bool(meta.get("scaled"))
        self.basis = meta.get("basis") or None
        self.samples = int(meta.get("samples", len(y)))

    def time_track(self, absolute_time: bool = True) -> np.ndarray:
        """Return the time axis for this series.

        ``absolute_time=True`` is ``numpy.datetime64[ns]`` UTC timestamps.
        ``absolute_time=False`` is seconds since the basis used for the read
        (negative before the basis). That is not npTDMS
        ``time_track(absolute_time=False)``, which is ``wf_start_offset`` plus
        the sample index times ``wf_increment`` and is zero at the first
        sample when the offset is zero. An untimed series returns sample
        indexes. gotdms does not build a DAQmx time track.
        """
        if absolute_time:
            if self.time_ns is None:
                raise UntimedError(f"{self.group}/{self.name} has no absolute time")
            return self.time_ns.astype("datetime64[ns]")
        if self.relative_s is None:
            raise GotdmsError("relative time was not in this response")
        return self.relative_s


def decode_ipc(blob: bytes):
    try:
        import pyarrow.ipc as ipc
    except ImportError as exc:
        raise GotdmsError(
            "pyarrow is required to decode Arrow IPC; install gotdms[arrow]"
        ) from exc
    reader = ipc.open_stream(io.BytesIO(blob))
    table = reader.read_all()
    names = set(table.column_names)
    y = np.asarray(table.column("y").to_numpy(zero_copy_only=False), dtype=np.float64)
    time_ns = None
    if "time_ns" in names:
        time_ns = np.asarray(table.column("time_ns").to_numpy(zero_copy_only=False), dtype=np.int64)
    relative = None
    if "relative_s" in names:
        relative = np.asarray(table.column("relative_s").to_numpy(zero_copy_only=False), dtype=np.float64)
    return y, time_ns, relative


def series_from_parts(metas: list, parts: list[bytes]) -> list[Series]:
    if len(metas) != len(parts):
        raise ProtocolError(f"{len(metas)} channel headers and {len(parts)} payloads")
    out = []
    for meta, blob in zip(metas, parts):
        y, time_ns, relative = decode_ipc(blob)
        out.append(Series(meta, y, time_ns, relative))
    return out


def as_dataframe(series: list[Series], *, time: str = "column", absolute_time: bool = True, align: str = "strict"):
    """Build one DataFrame from series that were returned together.

    ``time`` is ``"column"`` (default), ``"index"``, or ``"none"``.
    ``absolute_time=True`` uses absolute timestamps. ``False`` uses
    basis-relative seconds, not npTDMS's zero-at-first-sample axis.

    ``align="strict"`` requires identical time vectors and does not insert
    NaNs. ``align="nptdms"`` outer-joins on time and inserts NaNs where a
    channel has no sample, which is the npTDMS ``as_dataframe(time_index=True)``
    behavior. Duplicate timestamps in one channel raise AlignmentError
    instead of repeating rows.

    Columns are ``group/name``. The caller chooses scaling before the read;
    this function does not scale.
    """
    try:
        import pandas as pd
    except ImportError as exc:
        raise GotdmsError("pandas is required for as_dataframe; install gotdms[pandas]") from exc
    if time not in ("column", "index", "none"):
        raise ArgumentError("time must be column, index, or none")
    if align not in ("strict", "nptdms"):
        raise ArgumentError("align must be strict or nptdms")
    if not series:
        raise ArgumentError("no channels")
    keys = []
    for item in series:
        if absolute_time:
            if item.time_ns is None:
                raise UntimedError(f"{item.group}/{item.name} has no absolute time")
            keys.append(np.asarray(item.time_ns))
        else:
            if item.relative_s is None:
                raise GotdmsError(f"{item.group}/{item.name} has no relative time")
            keys.append(np.asarray(item.relative_s, dtype=np.float64))
    names = [f"{item.group}/{item.name}" for item in series]
    if align == "strict":
        for key in keys[1:]:
            if not np.array_equal(keys[0], key):
                raise AlignmentError(
                    "channels do not share a time vector; pass align='nptdms' to insert NaNs"
                )
        frame = pd.DataFrame({names[i]: np.asarray(series[i].y) for i in range(len(series))})
        return _attach_time(frame, keys[0], time, absolute_time)
    frames = []
    for item, key, name in zip(series, keys, names):
        if len(np.unique(key)) != len(key):
            raise AlignmentError(f"{name} has duplicate timestamps")
        frames.append(pd.DataFrame({name: np.asarray(item.y), "__t": key}))
    merged = frames[0]
    for frame in frames[1:]:
        merged = merged.merge(frame, on="__t", how="outer")
    merged = merged.sort_values("__t", kind="mergesort").reset_index(drop=True)
    key = merged.pop("__t").to_numpy()
    return _attach_time(merged, key, time, absolute_time)


def _attach_time(frame, key, time: str, absolute: bool):
    import pandas as pd

    if time == "none":
        return frame
    if absolute:
        stamps = pd.to_datetime(np.asarray(key), unit="ns", utc=True)
        if time == "index":
            frame.index = pd.DatetimeIndex(stamps, name="time")
            return frame
        frame.insert(0, "time", stamps)
        return frame
    if time == "index":
        frame.index = pd.Index(np.asarray(key, dtype=np.float64), name="relative_s")
        return frame
    frame.insert(0, "relative_s", np.asarray(key, dtype=np.float64))
    return frame
