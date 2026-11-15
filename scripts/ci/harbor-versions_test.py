#!/usr/bin/env python3
import importlib.util
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("hv", pathlib.Path(__file__).with_name("harbor-versions.py"))
hv = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hv)


class VersionsTest(unittest.TestCase):
    def write(self, text):
        handle = tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False)
        handle.write(text)
        handle.close()
        self.addCleanup(pathlib.Path(handle.name).unlink)
        return handle.name

    def test_parses_entries_and_ignores_comments(self):
        path = self.write('# c\n- minor: "2.2"\n  chart: a\n  values: ""\n\n- minor: "2.1"\n  chart: b # not a comment\n  values: v.yaml\n')
        entries = hv.load(path)
        self.assertEqual([e["minor"] for e in entries], ["2.2", "2.1"])
        self.assertEqual(entries[1]["values"], "v.yaml")
        self.assertEqual(entries[0]["values"], "")

    def test_find_defaults_to_first_and_rejects_unknown(self):
        entries = [{"minor": "2.2"}, {"minor": "2.1"}]
        self.assertEqual(hv.find(entries, "")["minor"], "2.2")
        self.assertEqual(hv.find(entries, "2.1")["minor"], "2.1")
        with self.assertRaises(SystemExit):
            hv.find(entries, "2.0")

    def test_committed_file_lists_two_distinct_minors_with_existing_values(self):
        entries = hv.load()
        self.assertEqual(len(entries), 2)
        self.assertEqual(len({e["minor"] for e in entries}), 2)
        self.assertEqual(len({e["chartVersion"] for e in entries}), 2)
        root = pathlib.Path(hv.DEFAULT).parents[2]
        for entry in entries:
            if entry["values"]:
                self.assertTrue((root / entry["values"]).is_file())


if __name__ == "__main__":
    unittest.main()
