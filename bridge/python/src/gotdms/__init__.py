"""Python client for gotdms, the MIT TDMS reader.

This package is the npTDMS-decoupling path. Analysis code stays in Python.
It launches the ``gotdms-bridge`` binary instead of importing npTDMS
(LGPL-3.0-only). Nothing in this package imports npTDMS.

The bridge speaks Arrow IPC, which Polars reads natively. Install
``gotdms[arrow]`` (pyarrow) to decode reads and ``gotdms[polars]`` for
:func:`as_polars`. numpy is required; it is BSD-3-Clause. This package does
not depend on pandas. A caller who wants one can use Polars' ``to_pandas()``.

See the migration guide next to this package for the npTDMS mapping, the
license note, and the batch and interactive modes.
"""

from .bundle import Bundle, load_bundle
from .client import Channel, Group, Session, locate_binary, open
from .codec import Series, as_polars
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
    "as_polars",
    "load_bundle",
    "locate_binary",
    "open",
]
