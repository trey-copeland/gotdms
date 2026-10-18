"""Python client for gotdms, the MIT TDMS reader.

This package is the npTDMS-decoupling path. Analysis code stays in Python
with numpy and pandas. It launches the ``gotdms-bridge`` binary instead of
importing npTDMS (LGPL-3.0-only). Nothing in this package imports npTDMS.

The bridge speaks Arrow IPC. Install ``gotdms[arrow]`` to decode reads and
``gotdms[pandas]`` for :func:`as_dataframe`. numpy is required.

See ``docs/nptdms-migration.md`` for the npTDMS mapping, the license note,
and the batch and interactive modes.
"""

from .bundle import Bundle, load_bundle
from .client import Channel, Group, Session, locate_binary, open
from .codec import Series, as_dataframe
from .errors import (
    AlignmentError,
    AmbiguousChannelError,
    ArgumentError,
    GotdmsError,
    MonotonicError,
    NoFilesError,
    NoScaleError,
    NonlinearScaleError,
    NotFoundError,
    OverlapError,
    ProtocolError,
    ScaleMismatchError,
    UntimedError,
)

__all__ = [
    "AlignmentError",
    "AmbiguousChannelError",
    "ArgumentError",
    "Bundle",
    "Channel",
    "GotdmsError",
    "Group",
    "MonotonicError",
    "NoFilesError",
    "NoScaleError",
    "NonlinearScaleError",
    "NotFoundError",
    "OverlapError",
    "ProtocolError",
    "ScaleMismatchError",
    "Series",
    "Session",
    "UntimedError",
    "as_dataframe",
    "load_bundle",
    "locate_binary",
    "open",
]
