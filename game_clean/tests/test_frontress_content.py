import importlib.util
import json
from pathlib import Path, PurePath
import shutil
import tempfile
import unittest

import vpk  # Independent format reader; pinned only in the test job.


SCRIPT = Path(__file__).resolve().parents[1] / "frontress_content.py"
spec = importlib.util.spec_from_file_location("frontress_content", SCRIPT)
content = importlib.util.module_from_spec(spec)
spec.loader.exec_module(content)


class ResourcePackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.dest = Path(self.temp.name)
        for name in ("gameinfo.txt", "gameinfo_server.txt"):
            shutil.copyfile(content.ROOT / "game/tc2" / name, self.dest / name)

    def test_every_checkout_resource_readable_by_independent_reader(self):
        content.build(self.dest)
        archive = vpk.open(str(self.dest / content.ARCHIVE))
        expected = content.source_files()
        self.assertEqual(set(archive), set(expected))
        for name, data in expected.items():
            with archive[name] as entry:
                self.assertEqual(entry.read(), data, name)
        first = (self.dest / content.ARCHIVE).read_bytes()
        content.build(self.dest)
        self.assertEqual((self.dest / content.ARCHIVE).read_bytes(), first)

    def test_empty_extensionless_and_binary_entries(self):
        files = {"readme": b"", "cfg/nested/test.cfg": b"value 1\n",
                 "resource/locale.txt": "Русский".encode("utf-16"),
                 "sound/test.wav": bytes(range(256)) * 512}
        path = self.dest / content.ARCHIVE
        path.write_bytes(content.make_vpk(files))
        archive = vpk.open(str(path))
        for name, data in files.items():
            # vpk 1.4.0 exposes the format's blank-extension sentinel as '. '.
            key = name if PurePath(name).suffix else name + '. '
            with archive[key] as entry:
                self.assertEqual(entry.read(), data)
        self.assertEqual(content.read_vpk(path.read_bytes()), files)

    def test_corrupt_package_rejected_before_publication(self):
        content.build(self.dest)
        path = self.dest / content.ARCHIVE
        data = bytearray(path.read_bytes())
        data[-1] ^= 1
        path.write_bytes(data)
        with self.assertRaisesRegex(ValueError, "SHA256"):
            content.verify(self.dest)

    def test_old_commit_rejected(self):
        content.build(self.dest)
        path = self.dest / content.MANIFEST
        manifest = json.loads(path.read_text())
        manifest["commit"] = "old-build"
        path.write_text(json.dumps(manifest))
        with self.assertRaisesRegex(ValueError, "different commit"):
            content.verify(self.dest)

    def test_wrong_mount_order_rejected(self):
        content.build(self.dest)
        path = self.dest / "gameinfo.txt"
        data = path.read_text().replace("frontress.vpk", "TEMP.vpk")
        data = data.replace("pak1.vpk", "frontress.vpk").replace("TEMP.vpk", "pak1.vpk")
        path.write_text(data)
        with self.assertRaisesRegex(ValueError, "mount frontress"):
            content.verify(self.dest)

    def test_cached_pack_rebuilt_after_resource_change(self):
        content.build(self.dest)
        original = content.source_files
        def changed_sources():
            files = original()
            files["resource/ui/mainmenuoverride.res"] += b"\n// changed for release\n"
            return files
        content.source_files = changed_sources
        self.addCleanup(setattr, content, "source_files", original)
        with self.assertRaisesRegex(ValueError, "stale.*mainmenuoverride"):
            content.verify(self.dest)
        content.build(self.dest)
        archive = vpk.open(str(self.dest / content.ARCHIVE))
        with archive["resource/ui/mainmenuoverride.res"] as entry:
            self.assertTrue(entry.read().endswith(b"// changed for release\n"))


if __name__ == "__main__":
    unittest.main()
