"""Launch the real gotdms-bridge binary. The pipe is the product under test."""

import json
import os
import subprocess
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[2]
BRIDGE = ROOT / "bridge"

try:
    import pandas as pd
except ImportError:  # pragma: no cover - pandas is installed in this environment
    pd = None


class BridgeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.TemporaryDirectory()
        cls.bin = Path(cls.tmp.name) / "gotdms-bridge"
        subprocess.check_call(
            ["go", "build", "-o", str(cls.bin), "./cmd/gotdms-bridge"],
            cwd=BRIDGE,
        )
        os.environ["GOTDMS_BRIDGE"] = str(cls.bin)
        demo = subprocess.check_output([str(cls.bin), "demo", "--out", cls.tmp.name], text=True)
        cls.demo = json.loads(demo)
        cls.epoch = datetime(2024, 3, 1, tzinfo=timezone.utc)

    @classmethod
    def tearDownClass(cls):
        cls.tmp.cleanup()

    def open_demo(self):
        import gotdms

        return gotdms.open(
            self.demo["files"],
            timestamp_channel="Time",
            timestamp_channels={"raw": ""},
            numeric_epoch=self.epoch,
        )

    def test_arrays_times_and_errors(self):
        import gotdms

        with self.open_demo() as session:
            ai0 = session["acq"]["ai0"]
            self.assertEqual(ai0.data_type, "tdsTypeDoubleFloat")
            y = ai0.data()
            self.assertIsInstance(y, np.ndarray)
            np.testing.assert_array_equal(y, [10, 11, 12, 13, 14, 15])
            absolute = ai0.time_track(absolute_time=True)
            self.assertEqual(absolute.dtype, np.dtype("datetime64[ns]"))
            self.assertEqual(absolute[0], np.datetime64("2024-03-01T00:00:00"))
            self.assertEqual(absolute[-1], np.datetime64("2024-03-01T00:00:05"))
            relative = ai0.time_track(absolute_time=False)
            np.testing.assert_array_equal(relative, [0, 1, 2, 3, 4, 5])

            basis = self.epoch + timedelta(seconds=1)
            window = session.read(
                "ai0",
                "acq",
                start=self.epoch + timedelta(seconds=2),
                end=self.epoch + timedelta(seconds=5),
                basis=basis,
            )
            np.testing.assert_array_equal(window.y, [12, 13, 14])
            np.testing.assert_array_equal(window.relative_s, [1, 2, 3])

            many = session.read_many(
                [{"name": "ai0", "group": "acq"}, {"name": "ai1", "group": "acq"}],
                start=self.epoch + timedelta(seconds=2),
                end=self.epoch + timedelta(seconds=5),
            )
            np.testing.assert_array_equal(many[0].time_ns, many[1].time_ns)
            np.testing.assert_array_equal(many[1].y, [3, 4, 5])
            self.assertFalse(many[1].scaled)
            scaled = session.read(
                "ai1",
                "acq",
                start=self.epoch + timedelta(seconds=2),
                end=self.epoch + timedelta(seconds=5),
                scaled=True,
            )
            np.testing.assert_array_equal(scaled.y, [6.5, 8.5, 10.5])
            self.assertTrue(scaled.scaled)
            self.assertEqual(session["acq"]["ai1"].scale["slope"], 2)
            self.assertEqual(session["acq"]["ai1"].unit, "V")

            with self.assertRaises(gotdms.NoScaleError):
                session.read("ai0", "acq", scaled=True)
            with self.assertRaises(gotdms.NonlinearScaleError):
                session.read("poly", "other", scaled=True)
            with self.assertRaises(gotdms.AmbiguousChannelError) as amb:
                session.channel("ai0")
            self.assertGreaterEqual(len(amb.exception.candidates), 2)
            with self.assertRaises(gotdms.UntimedError):
                session.read(
                    "x",
                    "raw",
                    start=self.epoch,
                    end=self.epoch + timedelta(seconds=1),
                )
            raw = session["raw"]["x"]
            np.testing.assert_array_equal(raw.data(), [9, 8, 7])
            np.testing.assert_array_equal(raw.time_track(absolute_time=False), [0, 1, 2])
            with self.assertRaises(gotdms.UntimedError):
                raw.time_track(absolute_time=True)

            names = [channel.name for channel in session.group("acq")]
            self.assertIn("ai0", names)
            self.assertIn("Time", names)
            self.assertTrue(any(item["name"] == "ai1" for item in session.search("ai1")))

    @unittest.skipIf(pd is None, "pandas is not installed")
    def test_as_dataframe(self):
        import gotdms

        with self.open_demo() as session:
            frame = session.as_dataframe(
                channels=[session.channel("ai0", "acq"), session.channel("ai1", "acq")],
                time="column",
                absolute_time=True,
                align="strict",
            )
            self.assertIsInstance(frame, pd.DataFrame)
            self.assertIn("time", frame.columns)
            self.assertTrue(isinstance(frame.index, pd.RangeIndex))
            np.testing.assert_array_equal(frame["acq/ai0"].to_numpy(), [10, 11, 12, 13, 14, 15])
            np.testing.assert_array_equal(frame["acq/ai1"].to_numpy(), [1, 2, 3, 4, 5, 6])
            self.assertFalse(frame["acq/ai0"].isna().any())
            indexed = session["acq"].as_dataframe(time="index", absolute_time=True, align="strict")
            self.assertIsInstance(indexed.index, pd.DatetimeIndex)
            self.assertNotIn("Time", "".join(indexed.columns))

            with self.assertRaises(gotdms.AlignmentError):
                session.as_dataframe(
                    channels=[session.channel("ai0", "acq"), session.channel("volt", "fast")],
                    align="strict",
                )
            outer = session.as_dataframe(
                channels=[session.channel("ai0", "acq"), session.channel("volt", "fast")],
                align="nptdms",
                time="column",
            )
            self.assertTrue(outer["acq/ai0"].isna().any())
            self.assertTrue(outer["fast/volt"].isna().any())
            at_half = outer.loc[outer["time"] == pd.Timestamp("2024-03-01T00:00:00.500000000Z")]
            self.assertEqual(len(at_half), 1)
            self.assertTrue(pd.isna(at_half["acq/ai0"].iloc[0]))
            self.assertEqual(at_half["fast/volt"].iloc[0], 1)

            with self.assertRaises(gotdms.AlignmentError):
                session.channel("d", "dups").as_dataframe(align="nptdms")

            relative = session.as_dataframe(
                channels=[session.channel("ai0", "acq")],
                time="index",
                absolute_time=False,
                basis=self.epoch + timedelta(seconds=1),
            )
            self.assertEqual(list(relative.index), [float(v) for v in range(-1, 5)])

    def test_convert_bundle(self):
        import gotdms

        out = Path(self.tmp.name) / "bundle"
        start = "2024-03-01T00:00:02Z"
        end = "2024-03-01T00:00:05Z"
        subprocess.check_call(
            [
                str(self.bin),
                "convert",
                "--path",
                self.demo["files"][0],
                "--path",
                self.demo["files"][1],
                "--path",
                self.demo["files"][2],
                "--timestamp-channel",
                "Time",
                "--group-timestamp",
                "raw=",
                "--numeric-epoch",
                "2024-03-01T00:00:00Z",
                "--channel",
                "acq/ai0",
                "--channel",
                "acq/ai1",
                "--start",
                start,
                "--end",
                end,
                "--basis",
                "2024-03-01T00:00:01Z",
                "--out",
                str(out),
            ],
            stdout=subprocess.DEVNULL,
        )
        bundle = gotdms.load_bundle(out)
        self.assertEqual(bundle.format, "arrow-ipc-stream")
        self.assertEqual(len(bundle.channels), 2)
        self.assertEqual(bundle.channels[0].group, "acq")
        self.assertTrue(bundle.channels[0].meta.get("data_type"))
        self.assertTrue(bundle.basis)
        np.testing.assert_array_equal(bundle.channels[0].y, [12, 13, 14])
        np.testing.assert_array_equal(bundle.channels[0].relative_s, [1, 2, 3])
        if pd is not None:
            frame = bundle.as_dataframe(align="strict", time="column")
            self.assertIn("time", frame.columns)
            np.testing.assert_array_equal(frame["acq/ai1"].to_numpy(), [3, 4, 5])


if __name__ == "__main__":
    unittest.main()
