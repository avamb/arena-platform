"""Local benchmark foundation. No converter execution, SDK, network or reference feedback."""
from collections import Counter, defaultdict
from hashlib import sha256
from pathlib import Path
import argparse
import csv
import json
import sys
import time
import shutil

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from seating_poc.model_validation import validate_structure

HERE = Path(__file__).resolve().parent
OUT = HERE / 'output'
AUDIT = ROOT / 'dataset_audit/output'
SPLITS = ('development', 'validation', 'closed_test_candidate', 'quarantine')


def read(path):
    return json.loads(Path(path).read_text(encoding='utf-8-sig'))


def write(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + '\n', encoding='utf-8')


def digest(path):
    return sha256(Path(path).read_bytes()).hexdigest()


def check_partition(manifest, split):
    errors, shared = [], []
    membership = defaultdict(set)
    for group in SPLITS:
        for cid in split[group]:
            membership[cid].add(group)
    for cid, groups in membership.items():
        if len(groups) != 1:
            errors.append(f'Venue crosses splits: {cid}')
    venues = {v['venue_cluster_id']: v for v in manifest['venues']}
    if set(venues) != set(membership):
        errors.append('Split coverage differs from venue inventory')
    file_groups, hashes = defaultdict(set), defaultdict(set)
    files = {f['file_id']: f for f in manifest['files']}
    for cid, venue in venues.items():
        if membership[cid] != {venue['split']}:
            errors.append(f'Manifest/split disagreement: {cid}')
        for key in ('source_file_ids', 'reference_svg_file_ids', 'reference_visual_file_ids', 'auxiliary_file_ids'):
            for fid in venue.get(key, []):
                file_groups[fid].add(venue['split'])
                hashes[files[fid]['sha256']].add(venue['split'])
    for fid, groups in file_groups.items():
        if len(groups) > 1:
            errors.append(f'File crosses splits: {fid}')
    for value, groups in hashes.items():
        if len(groups) > 1:
            errors.append(f'Exact duplicate crosses splits: {value}')
    for alias in split['alias_groups']:
        venue = venues[alias['venue_cluster_id']]
        if not set(alias['all_reference_svg_ids']) <= set(venue['reference_svg_file_ids']):
            errors.append(f'Alias/version coverage differs: {venue["venue_cluster_id"]}')
    for candidate in manifest['perceptual_similarity_candidates']:
        groups = set().union(*(file_groups[fid] for fid in candidate['file_ids']))
        if len(groups) > 1:
            errors.append(f'Suspected image variants cross splits: {candidate["file_ids"]}')
        shared.append({'file_ids': candidate['file_ids'], 'splits': sorted(groups), 'identity': 'unknown; perceptual similarity is not proof'})
    return {'errors': errors, 'counts': {s: len(split[s]) for s in SPLITS},
        'known_alias_version_groups_checked': len(split['alias_groups']),
        'perceptual_candidates_checked': shared,
        'status': 'known_clusters_disjoint; physical_identity_not_fully_certified' if not errors else 'blocked',
        'closed_test_status': 'candidate_only_not_frozen',
        'limitations': ['Unidentified physical venues remain quarantined; unknown aliases cannot be certified automatically.',
            'Previously inspected reference files are a prospective operational holdout, not retrospectively unseen data.']}


def prepare():
    manifest, split = read(AUDIT/'dataset-manifest.json'), read(AUDIT/'split-proposal.json')
    partition = check_partition(manifest, split)
    with (AUDIT/'source-svg-pairs.csv').open(encoding='utf-8-sig', newline='') as handle:
        csv_pairs = list(csv.DictReader(handle, delimiter=';'))
    files_by_id = {f['file_id']: f for f in manifest['files']}
    names_by_id = {v['venue_cluster_id']: v['name'] for v in manifest['venues']}
    expected_csv = Counter((names_by_id[p['venue_cluster_id']], files_by_id[p['source_file_id']]['path'].replace('ChatExport_2026-10-11/', '').replace('ChatExport_2026-10-11\\', ''), p['confidence']) for p in manifest['pairs'])
    observed_csv = Counter((p['Площадка/группа'], p['Исходник'], p['Статус связи']) for p in csv_pairs)
    if observed_csv != expected_csv:
        partition['errors'].append('CSV/JSON pair inventory differs')
    source_manifest = read(AUDIT/'generator-input-manifest.json')
    refs = read(AUDIT/'evaluator-reference-manifest.json')
    # Keep full references on the evaluator side. No converter receives this data.
    write(HERE/'evaluator_only/references.json', refs)
    source_bytes = json.dumps(source_manifest, ensure_ascii=False).lower()
    if '.svg' in source_bytes or 'reference_svg' in source_bytes or 'candidate_seat_circles' in source_bytes:
        partition['errors'].append('Reference metadata in source-only manifest')
    write(HERE/'converter_inputs/sources.json', source_manifest)
    # A real source-only payload for the one control, not a link to the archive.
    control_source = next(s for s in source_manifest['sources'] if s['venue_cluster_id'] == 'akropolis')
    source_path = ROOT/control_source['source_path']
    if digest(source_path) != control_source['sha256']:
        partition['errors'].append('Control source hash differs from inventory')
    bundle = HERE/'converter_inputs/akropolis-260'
    bundle.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source_path, bundle/source_path.name)
    write(bundle/'input.json', {'case_id': 'akropolis-260', 'files': [{'path': source_path.name, 'sha256': control_source['sha256']}],
        'access_policy': 'Only this bundle and an empty output directory may be mounted in a future converter sandbox.'})
    pairs = []
    for pair in manifest['pairs']:
        confidence = pair['confidence']
        level = ('geometrically_verified' if confidence == 'confirmed_geometric_poc' else
                 'contextual' if confidence == 'confirmed_archive_association' else 'hypothetical')
        pairs.append({**pair, 'benchmark_evidence_level': level,
            'automatic_scoring_ready': level == 'geometrically_verified',
            'required_curation': [] if level == 'geometrically_verified' else
                ['confirm exact source/target configuration and version', 'approve contextual group mapping and alignment anchors',
                 'mark absent official semantics unknown; separate unreadable content from nonexistent content']})
    write(HERE/'evaluator_only/pair-readiness.json', pairs)
    shortlist_ids = ['aribau', 'hybernia', 'kino-mkp', 'ladvi', 'merkin', 'scandic', 'nova-spirala']
    shortlist = []
    file_map = {f['file_id']: f for f in manifest['files']}
    for venue in manifest['venues']:
        if venue['venue_cluster_id'] not in shortlist_ids:
            continue
        assert venue['split'] == 'development'
        shortlist.append({k: venue[k] for k in ('venue_cluster_id', 'name', 'complexity', 'processing_without_ai', 'uncertainties')} |
            {'source_files': [{'file_id': fid, 'path': file_map[fid]['path'], 'format': file_map[fid]['extension']}
                              for fid in venue['source_file_ids']], 'automatic_scoring_ready': False})
    write(OUT/'development-shortlist.json', shortlist)
    partition['pair_readiness_counts'] = dict(Counter(p['benchmark_evidence_level'] for p in pairs))
    partition['automatic_scoring_ready_pairs'] = sum(p['automatic_scoring_ready'] for p in pairs)
    partition['dataset_inputs_sha256'] = {p.name: digest(p) for p in (AUDIT/'dataset-manifest.json', AUDIT/'split-proposal.json', AUDIT/'source-svg-pairs.csv')}
    write(OUT/'split-and-readiness.json', partition)
    if partition['errors']:
        raise ValueError(partition['errors'])
    print(json.dumps(partition, ensure_ascii=False))


def validate_submission(submission, model):
    errors = validate_structure(model)
    for key in ('schema_version', 'case_id', 'converter', 'seating_model_path', 'processing'):
        if key not in submission:
            errors.append(f'Submission missing {key}')
    if submission.get('schema_version') != 'benchmark-submission-1':
        errors.append('Unsupported submission version')
    if model.get('schema_version') != 'phase3b-1':
        errors.append('Unsupported seating model version')
    if model.get('coordinates', {}).get('units') != 'PDF pt (1/72 inch)':
        errors.append('v1 requires explicit normalization to PDF points; never silently treat pixels as points')
    for seat in model.get('seats', []):
        for key in ('original_number_text', 'official_row_label', 'official_sector_label', 'confidence', 'evidence'):
            if key not in seat:
                errors.append(f"{seat.get('seat_key')}: missing {key}; unknown must be explicit null")
    for key in ('duration_seconds', 'api_cost_usd', 'input_tokens', 'output_tokens'):
        value = submission.get('processing', {}).get(key, 'missing')
        if value == 'missing' or (value is not None and (isinstance(value, bool) or not isinstance(value, (int, float)) or not (0 <= value < float('inf')))):
            errors.append(f'Invalid/missing processing.{key}')
    return errors


def run(submission_path):
    started = time.perf_counter()
    submission = read(submission_path)
    model_path = (ROOT/submission['seating_model_path']).resolve()
    if not model_path.is_relative_to(ROOT):
        raise ValueError('Submitted model must be inside project')
    model = read(model_path)
    before = digest(model_path)
    errors = validate_submission(submission, model)
    result = {'schema_version': 'benchmark-result-1', 'case_id': submission['case_id'],
        'converter': submission['converter'], 'model_sha256': before, 'errors': errors,
        'metrics': {'seat_count': {'observed': len(model.get('seats', [])), 'status': 'unknown'},
                    **{k: {'status': 'unknown', 'reason': 'No approved scoring adapter/reference mapping'}
                       for k in ('numbering', 'geometric_rows', 'official_rows', 'official_sectors', 'geometry', 'intentional_gaps', 'overlaps')}},
        'processing': submission['processing'], 'claims': {'universality_confirmed': False, 'production_ready': False}}
    if errors:
        result['status'] = 'invalid_submission'
    elif submission['case_id'] != 'akropolis-260':
        result['status'] = 'accepted_unscored'
    else:
        baseline = read(ROOT/'poc_registry/geometry-poc-001.json')
        changed = [a['path'] for a in baseline['artifacts'] if digest(ROOT/a['path']) != a['sha256']]
        if changed:
            raise ValueError(f'Accepted PoC artifacts changed: {changed}')
        # Import reference-bearing code only in the independent scoring process.
        from independent_evaluation.evaluate_reference import evaluate
        from seating_poc.build_seating_model import validate
        source_check = validate(model, read(ROOT/'pdf_diagnostics/results/source-manifest.json'))
        result['source_evidence_validation'] = source_check
        result['errors'].extend(source_check['errors'])
        try:
            ref_path = ROOT/'ChatExport_2026-10-11/files/Palac_Akropolis.svg'
            report = evaluate(model_path, ref_path, OUT/'control')
            result['reference_sha256'] = digest(ref_path)
            metrics = result['metrics']
            expected = report['reference_seats']
            metrics['seat_count'] = {'status': 'match' if len(model['seats']) == expected else 'mismatch',
                'observed': len(model['seats']), 'expected': expected,
                'absolute_error': abs(len(model['seats'])-expected), 'page_counts': report['model_page_counts'],
                'reference_sector_counts': report['reference_sector_counts'],
                'page_to_sector_mapping': 'evaluation hypothesis; not official labels'}
            metrics['numbering'] = {'status': 'match_under_evaluation_mapping' if not report['numbering_mismatches'] else 'mismatch',
                'mismatches': report['numbering_mismatches'], 'scope': 'actual parent-group context; global duplicate numbers allowed'}
            metrics['geometric_rows'] = {'status': 'match_under_evaluation_projection',
                'model_band_count': len(model['geometric_rows']), 'reference_group_count': len(report['reference_groups']),
                'mapping': report['group_mappings'], 'limitation': report['grouping_difference']}
            metrics['geometry'] = {'status': 'within_poc_tolerances' if all(g['alignment_accepted_under_poc_thresholds'] for g in report['geometry']) else 'unknown',
                'pages': [{k: v for k, v in g.items() if k != 'seat_residuals'} for g in report['geometry']],
                'limitation': 'Anchor seats used in alignment; diagnostic control, not unseen generalization test'}
            metrics['intentional_gaps'] = {'status': 'match_under_evaluation_mapping' if not report['numbering_mismatches'] else 'mismatch',
                'reference_groups': report['reference_groups'], 'model_gaps': [{k: r[k] for k in ('geometric_row_id', 'gaps_within_observed_number_range')} for r in model['geometric_rows']],
                'intent_authority': 'unknown; observed omissions preserved, intent is not inferred'}
            metrics['overlaps'] = {'status': 'source_validator_reported', 'details': source_check.get('overlaps', source_check.get('suspected_overlaps', 'unknown'))}
            result['status'] = 'control_completed' if not result['errors'] else 'control_failed'
        except ValueError as exc:
            result['status'] = 'architecture_decision_required'
            result['errors'].append(str(exc))
    result['evaluation_duration_seconds'] = time.perf_counter()-started
    result['model_unchanged'] = digest(model_path) == before
    write(OUT/'benchmark-result.json', result)
    print(json.dumps({'status': result['status'], 'errors': result['errors'], 'model_unchanged': result['model_unchanged']}, ensure_ascii=False))
    return result


if __name__ == '__main__':
    sys.stdout.reconfigure(encoding='utf-8')
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['prepare', 'run'])
    parser.add_argument('--submission', type=Path, default=HERE/'control-submission.json')
    args = parser.parse_args()
    if args.action == 'prepare':
        prepare()
    else:
        result = run(args.submission)
        if result['errors'] or result['status'] == 'architecture_decision_required':
            sys.exit(1)
