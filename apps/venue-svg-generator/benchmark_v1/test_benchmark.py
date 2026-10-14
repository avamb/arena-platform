"""Infrastructure tests only: no evaluator calls and no converter execution."""
import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
from benchmark import AUDIT, HERE, ROOT, check_partition, read, validate_submission


class BenchmarkTests(unittest.TestCase):
    def setUp(self):
        self.submission = read(HERE/'control-submission.json')
        self.model = read(ROOT/self.submission['seating_model_path'])

    def test_provider_neutral_contract(self):
        for provider in (None, 'anthropic', 'openai', 'other'):
            self.submission['converter']['provider'] = provider
            self.assertEqual(validate_submission(self.submission, self.model), [])

    def test_unknown_time_is_not_zero(self):
        self.assertIsNone(self.submission['processing']['duration_seconds'])
        del self.submission['processing']['duration_seconds']
        self.assertTrue(validate_submission(self.submission, self.model))

    def test_invalid_cost_rejected(self):
        for value in (-1, float('nan'), float('inf'), True):
            self.submission['processing']['api_cost_usd'] = value
            self.assertTrue(validate_submission(self.submission, self.model))

    def test_duplicate_key_rejected(self):
        self.model['seats'][1]['seat_key'] = self.model['seats'][0]['seat_key']
        self.assertTrue(validate_submission(self.submission, self.model))

    def test_nonfinite_position_rejected(self):
        self.model['seats'][0]['position_pt']['x'] = float('nan')
        self.assertTrue(validate_submission(self.submission, self.model))

    def test_pixels_require_explicit_normalization(self):
        self.model['coordinates']['units'] = 'pixels'
        self.assertTrue(validate_submission(self.submission, self.model))

    def test_split_and_shared_alias_failure(self):
        manifest = read(AUDIT/'dataset-manifest.json')
        split = read(AUDIT/'split-proposal.json')
        self.assertEqual(check_partition(manifest, split)['errors'], [])
        split['validation'].append('akropolis')
        self.assertTrue(check_partition(manifest, split)['errors'])

    def test_version_hash_leak_rejected(self):
        manifest = read(AUDIT/'dataset-manifest.json')
        split = read(AUDIT/'split-proposal.json')
        dev = next(v for v in manifest['venues'] if v['venue_cluster_id'] == 'aribau')
        val = next(v for v in manifest['venues'] if v['venue_cluster_id'] == 'atrium')
        val['reference_svg_file_ids'].append(dev['reference_svg_file_ids'][0])
        self.assertTrue(check_partition(manifest, split)['errors'])

    def test_source_manifest_has_no_reference_metadata(self):
        text = (HERE/'converter_inputs/sources.json').read_text(encoding='utf-8').lower()
        for marker in ('.svg', 'reference_svg', 'candidate_seat_circles'):
            self.assertNotIn(marker, text)
        bundle = HERE/'converter_inputs/akropolis-260'
        self.assertEqual({p.suffix for p in bundle.iterdir()}, {'.json', '.pdf'})


if __name__ == '__main__':
    unittest.main(verbosity=2)
