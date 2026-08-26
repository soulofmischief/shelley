import importlib.util
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("generate-version-metadata.py")
SPEC = importlib.util.spec_from_file_location("generate_version_metadata", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MODULE)


class GenerateVersionMetadataTest(unittest.TestCase):
    def test_latest_release_comes_from_published_github_release(self):
        response = io.BytesIO(
            json.dumps(
                {
                    "tag_name": "v0.42.9001",
                    "published_at": "2026-08-25T20:00:00Z",
                }
            ).encode()
        )
        with mock.patch.object(
            MODULE.urllib.request, "urlopen", return_value=response
        ) as urlopen:
            self.assertEqual(
                MODULE.get_latest_release("soulofmischief/shelley"),
                ("v0.42.9001", "2026-08-25T20:00:00Z"),
            )
        request = urlopen.call_args.args[0]
        self.assertEqual(
            request.full_url,
            "https://api.github.com/repos/soulofmischief/shelley/releases/latest",
        )

    def test_latest_release_rejects_incomplete_metadata(self):
        response = io.BytesIO(json.dumps({"tag_name": "v0.42.9001"}).encode())
        with mock.patch.object(MODULE.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "missing its tag or publication time"):
                MODULE.get_latest_release("soulofmischief/shelley")

    def test_commits_are_anchored_to_release_tag(self):
        with tempfile.TemporaryDirectory() as directory:
            output_dir = Path(directory)
            with mock.patch.object(
                MODULE.subprocess,
                "check_output",
                return_value="abc1234\x00Released commit\n",
            ) as check_output:
                MODULE.generate_commits_json(output_dir, "v0.42.9001", count=12)
            command = check_output.call_args.args[0]
            self.assertEqual(command[-2:], ["-12", "v0.42.9001"])
            commits = json.loads((output_dir / "commits.json").read_text())
            self.assertEqual(
                commits, [{"sha": "abc1234", "subject": "Released commit"}]
            )


if __name__ == "__main__":
    unittest.main()
