import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('stage_cogletnet', Path(__file__).with_name('stage_cogletnet.py'))
stage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stage)


class StagingTests(unittest.TestCase):
    def test_snapshot_is_read_only_and_excludes_private_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / 'upstream'
            source.mkdir()
            files = {'Cargo.toml': '[workspace]\nmembers = ["crates/core"]\n',
                     'Cargo.lock': 'version = 4\n',
                     'rust-toolchain.toml': '[toolchain]\nchannel = "1.97.1"\n',
                     'crates/core/src/lib.rs': '// unchanged upstream\n',
                     '.env': 'PRIVATE_KEY=never-stage-this\n',
                     'data/state.sqlite3': 'private state',
                     'target/debug/binary': 'host build output'}
            for relative, content in files.items():
                p = source / relative
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(content)
            with contextlib.redirect_stdout(io.StringIO()):
                stage.stage(source, root / 'staged')
            for relative, content in files.items():
                self.assertEqual((source / relative).read_text(), content)
            project = root / 'staged/cogletnet'
            self.assertFalse(list(project.rglob('.env')))
            self.assertFalse((project / 'source/data').exists())
            self.assertFalse((project / 'source/target').exists())
            self.assertEqual((project / 'source/crates/core/src/lib.rs').read_text(), files['crates/core/src/lib.rs'])
            self.assertIn('memeval-host', (project / 'source/Cargo.toml').read_text())
            self.assertIn('cogletnet-memeval', (project / 'source/Cargo.lock').read_text())
            m = json.loads((project / 'manifest.json').read_text())
            self.assertEqual(len(m['bridge']['config']['source_snapshot_sha256']), 64)
            leaf = json.loads((project / 'manifest-leaf-memory.json').read_text())
            self.assertEqual(leaf['bridge']['config']['source_snapshot_sha256'], m['bridge']['config']['source_snapshot_sha256'])
            self.assertEqual(leaf['bridge']['config']['retrieval_mode'], 'leaf_memory')
            self.assertEqual(leaf['process']['env']['INDEX_REPRESENTATIONS'], 'leaf_memory')
            leaf_embed = json.loads((project / 'manifest-leaf-memory-leaf-embed.json').read_text())
            self.assertEqual(leaf_embed['bridge']['config']['source_snapshot_sha256'], m['bridge']['config']['source_snapshot_sha256'])
            self.assertEqual(leaf_embed['bridge']['config']['retrieval_mode'], 'leaf_memory')
            self.assertEqual(leaf_embed['process']['env']['LEAF_MEMORY_EMBED_INPUT'], 'leaf')
            abstractive = json.loads((project / 'manifest-leaf-memory-abstractive.json').read_text())
            self.assertEqual(abstractive['bridge']['config']['source_snapshot_sha256'], m['bridge']['config']['source_snapshot_sha256'])
            self.assertEqual(abstractive['process']['env']['SUMMARY_PROMPT_PROFILE'], 'abstractive-ratio-v3')
            lv1 = json.loads((project / 'manifest-leaf-memory-abstractive-lv1.json').read_text())
            self.assertEqual(lv1['bridge']['config']['source_snapshot_sha256'], m['bridge']['config']['source_snapshot_sha256'])
            self.assertEqual(lv1['variant'], 'native-leaf-memory-abstractive-lv1-jev3-openai-embedding')
            self.assertEqual(lv1['process']['env']['MAX_LEVEL'], '1')
            self.assertEqual(lv1['process']['env']['SUMMARY_PROMPT_PROFILE'], 'abstractive-ratio-v3')
            tree = json.loads((project / 'manifest-tree.json').read_text())
            self.assertEqual(tree['bridge']['config']['source_snapshot_sha256'], m['bridge']['config']['source_snapshot_sha256'])
            self.assertEqual(tree['bridge']['config']['retrieval_mode'], 'tree')
            self.assertEqual(tree['process']['env']['INDEX_REPRESENTATIONS'], 'body')
            self.assertEqual(tree['process']['env']['SUMMARY_PROMPT_PROFILE'], 'abstractive-ratio-v4')
            self.assertEqual(tree['process']['env']['SUMMARY_RECORD_TIME'], 'recorded-v1')
            self.assertEqual(m['llm']['env']['HOST_GENERATION_MODEL'], '{llm.model}')
            self.assertEqual(m['llm']['env']['GENERATION_REASONING_EFFORT'], '{llm.reasoning_effort}')
            with self.assertRaisesRegex(ValueError, 'outside'):
                stage.stage(source, source / 'must-not-write')
            self.assertFalse((source / 'must-not-write').exists())

    def test_all_manifest_variants_stamped_without_preserving_file_mtime(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, repo = root / 'upstream', root / 'repo'
            source.mkdir()
            for name, data in {'Cargo.toml': '[workspace]\nmembers = []\n',
                               'Cargo.lock': 'version = 4\n',
                               'rust-toolchain.toml': '[toolchain]\nchannel = "stable"\n'}.items():
                (source / name).write_text(data)
            project = repo / 'suts/cogletnet'
            files = {'adapter.py': '# adapter\n', 'prepare_native.py': '#!/usr/bin/env python3\n',
                     'native/Cargo.toml': '[package]\nname = "cogletnet-memeval"\n',
                     'native/src/main.rs': 'fn main() {}\n'}
            for name in ('manifest.json', 'manifest-leaf-memory.json', 'manifest-leaf-memory-leaf-embed.json',
                         'manifest-leaf-memory-abstractive.json', 'manifest-leaf-memory-abstractive-lv1.json',
                         'manifest-tree.json', 'manifest-another-variant.json'):
                files[name] = json.dumps({'bridge': {'config': {'variant_marker': name}}}) + '\n'
            originals = {}
            for relative, data in files.items():
                p = project / relative
                p.parent.mkdir(parents=True, exist_ok=True)
                p.write_text(data)
                p.chmod(0o755 if relative == 'prepare_native.py' else 0o644)
                os.utime(p, (100, 100))
                originals[p] = (p.read_bytes(), p.stat().st_mtime_ns, p.stat().st_mode)
            bridge = repo / 'suts/_bridge/python/memeval_bridge/__init__.py'
            bridge.parent.mkdir(parents=True)
            bridge.write_text('# bridge\n')
            os.utime(bridge, (100, 100))
            with patch.object(stage, '__file__', str(repo / 'scripts/stage_cogletnet.py')):
                with contextlib.redirect_stdout(io.StringIO()) as stdout:
                    stage.stage(source, root / 'staged')
            snapshot = json.loads(stdout.getvalue())['source_snapshot_sha256']
            for name in files:
                dest = root / 'staged/cogletnet' / name
                self.assertGreater(dest.stat().st_mtime_ns, 100_000_000_000)
                if name.startswith('manifest'):
                    config = json.loads(dest.read_text())['bridge']['config']
                    self.assertEqual(config['variant_marker'], name)
                    self.assertEqual(config['source_snapshot_sha256'], snapshot)
                else:
                    self.assertEqual(dest.read_text(), files[name])
            self.assertGreater((root / 'staged/_bridge/python/memeval_bridge/__init__.py').stat().st_mtime_ns, 100_000_000_000)
            self.assertGreater((root / 'staged/cogletnet/source/memeval-host/src/main.rs').stat().st_mtime_ns, 100_000_000_000)
            self.assertEqual((root / 'staged/cogletnet/prepare_native.py').stat().st_mode & 0o777, 0o755)
            for path, expected in originals.items():
                self.assertEqual((path.read_bytes(), path.stat().st_mtime_ns, path.stat().st_mode), expected)


if __name__ == '__main__':
    unittest.main()
