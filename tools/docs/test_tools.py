"""Tests of the docs tooling: label scrubbing, link rewriting, navigation pruning, version selection, the values
comment parser. Run with `make docs-test`."""
from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import build_site  # noqa: E402
import common  # noqa: E402
import gen_values  # noqa: E402
import hooks  # noqa: E402


# The labels are assembled from parts so that this file does not itself contain them.
def d(n: str) -> str:
    return "D" + "-" + n


def w(n: str) -> str:
    return "W" + "O" + "-" + n


class ScrubTest(unittest.TestCase):
    def test_table(self):
        cases = {
            f"hpa-coupled ({d('011')}) is audit-only": "hpa-coupled is audit-only",
            f"title ({w('18d')})": "title",
            f"login ({d('023')}, admin area `docs/contracts/admin.md`)": "login (admin area `docs/contracts/admin.md`)",
            f"RDS ({d('028')}, verify-full): x": "RDS (verify-full): x",
            f"both ({w('18a')}, {d('028')}) here": "both here",
            "plain text": "plain text",
            f"see {d('031')} for why": "see for why",
            "": "",
        }
        for src, want in cases.items():
            with self.subTest(src=src):
                self.assertEqual(common.scrub(src), want)
        common.warnings.clear()

    def test_no_label_survives(self):
        out = common.scrub(f"a ({d('012')}) b {w('04')} c, {d('022')} d ({w('1')}, x)")
        self.assertNotRegex(out, r"\b(WO|D)-\d")
        common.warnings.clear()


class LinkTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "docs/concepts").mkdir(parents=True)
        (self.root / "docs/screenshots").mkdir()
        (self.root / "docs/page.md").write_text("x")
        (self.root / "docs/concepts/README.md").write_text("x")
        (self.root / "README.md").write_text("x")
        (self.root / "deploy/charts").mkdir(parents=True)
        (self.root / "api/v1alpha1").mkdir(parents=True)

    def tearDown(self):
        self.tmp.cleanup()

    def rewrite(self, md, page="page.md", ref="main"):
        return hooks.rewrite_links(md, page, self.root / "docs", ref)

    def test_leaves_docs_links(self):
        md = "[a](concepts/README.md) [b](page.md#x) [c](https://example.com) [d](#frag) ![i](img.png)"
        self.assertEqual(self.rewrite(md), md)

    def test_outside_docs_becomes_github(self):
        out = self.rewrite("[r](../README.md#roadmap) [c](../deploy/charts) [t](../api/v1alpha1)")
        self.assertIn("(https://github.com/kerf-dev/kerf/blob/main/README.md#roadmap)", out)
        self.assertIn("(https://github.com/kerf-dev/kerf/tree/main/deploy/charts)", out)
        self.assertIn("(https://github.com/kerf-dev/kerf/tree/main/api/v1alpha1)", out)

    def test_directory_without_readme_becomes_tree(self):
        self.assertIn("/tree/main/docs/screenshots", self.rewrite("[s](screenshots)"))

    def test_nested_page_and_release_ref(self):
        out = self.rewrite("[r](../../README.md)", page="concepts/x.md", ref="v0.1.0-rc.3")
        self.assertIn("/blob/v0.1.0-rc.3/README.md", out)

    def test_code_fences_untouched(self):
        md = "```\n[r](../README.md)\n```\n[r](../README.md)"
        out = self.rewrite(md).split("\n")
        self.assertEqual(out[1], "[r](../README.md)")
        self.assertIn("github.com", out[3])

    def test_missing_target_left_for_mkdocs_to_report(self):
        md = "[r](../nope.md)"
        self.assertEqual(self.rewrite(md), md)


class NavTest(unittest.TestCase):
    def test_prune(self):
        nav = ["a.md", {"Sec": ["b.md", "missing.md"]}, {"Empty": ["gone.md"]}, {"T": "c.md"}, {"X": "nope.md"}]
        out = hooks.prune_nav(nav, lambda p: p in {"a.md", "b.md", "c.md"})
        self.assertEqual(out, ["a.md", {"Sec": ["b.md"]}, {"T": "c.md"}])

    def test_nav_files(self):
        self.assertEqual(hooks._nav_files(["a.md", {"S": [{"T": "b.md"}, "c.md"]}]), {"a.md", "b.md", "c.md"})


class VersionTest(unittest.TestCase):
    def tags(self, *names):
        return [build_site.parse_tag(n) for n in names]

    def test_parse(self):
        self.assertIsNone(build_site.parse_tag("v1.2"))
        self.assertIsNone(build_site.parse_tag("latest"))
        t = build_site.parse_tag("v0.1.0-rc.3")
        self.assertEqual((t.minor_id, t.stable), ("0.1", False))

    def test_only_release_candidates(self):
        chosen, latest = build_site.pick_versions(self.tags("v0.1.0-rc.1", "v0.1.0-rc.3", "v0.1.0-rc.2"), 3)
        self.assertEqual([t.name for t in chosen], ["v0.1.0-rc.3"])
        self.assertEqual(latest.name, "v0.1.0-rc.3")

    def test_rc_numbering_is_numeric(self):
        chosen, _ = build_site.pick_versions(self.tags("v0.1.0-rc.9", "v0.1.0-rc.10"), 3)
        self.assertEqual(chosen[0].name, "v0.1.0-rc.10")

    def test_final_beats_rc_and_latest_is_stable(self):
        chosen, latest = build_site.pick_versions(
            self.tags("v0.1.0-rc.3", "v0.1.0", "v0.1.1", "v0.2.0-rc.1"), 3)
        self.assertEqual([t.name for t in chosen], ["v0.2.0-rc.1", "v0.1.1"])
        self.assertEqual(latest.name, "v0.1.1")

    def test_max_minors(self):
        chosen, _ = build_site.pick_versions(self.tags("v0.1.0", "v0.2.0", "v0.3.0", "v0.4.0"), 2)
        self.assertEqual([t.minor_id for t in chosen], ["0.4", "0.3"])

    def test_none(self):
        self.assertEqual(build_site.pick_versions([], 3), ([], None))


class UnpublishedTest(unittest.TestCase):
    def test_page_and_links_dropped(self):
        with tempfile.TemporaryDirectory() as tmp:
            docs = Path(tmp)
            (docs / "branding").mkdir()
            (docs / "branding" / "brand.md").write_text("# Brand\n")
            (docs / "branding" / "comparison.md").write_text("# Comparison\n")
            (docs / "README.md").write_text("| a | b |\n| [Brand](branding/brand.md) | x |\n| [C](branding/comparison.md) | y |\n")
            (docs / "guides").mkdir()
            (docs / "guides" / "g.md").write_text("See [voice](../branding/brand.md#voice).\nKeep me.\n")
            build_site.drop_unpublished(docs)
            self.assertFalse((docs / "branding" / "brand.md").exists())
            self.assertTrue((docs / "branding" / "comparison.md").exists())
            self.assertEqual((docs / "README.md").read_text(), "| a | b |\n| [C](branding/comparison.md) | y |\n")
            self.assertEqual((docs / "guides" / "g.md").read_text(), "Keep me.\n")


class ValuesCommentsTest(unittest.TestCase):
    TEXT = """\
# Banner of the file

# ------------------------------
# hub: the hub
hub:
  # Replicas of the hub.
  # Two by default.
  replicas: 2   # inline note
  image:
    tag: ""
  list:
    - a: 1

# A banner, then a blank line: not a comment of the next key

other: 1
"""

    def test_parse(self):
        docs = gen_values.parse_comments(self.TEXT)
        self.assertEqual(docs["hub"], "hub: the hub")
        self.assertEqual(docs["hub.replicas"], "Replicas of the hub.\nTwo by default.\ninline note")
        self.assertNotIn("hub.image.tag", docs)
        self.assertNotIn("other", docs)
        self.assertNotIn("hub.list.a", docs)

    def test_example_lines_become_code(self):
        cell = gen_values.comment_cell("Production:\n  secretStoreRef: {kind: X}\ncaBundle: {a: b}", "t")
        self.assertIn("`secretStoreRef: {kind: X}`", cell)
        self.assertIn("`caBundle: {a: b}`", cell)


if __name__ == "__main__":
    unittest.main()
