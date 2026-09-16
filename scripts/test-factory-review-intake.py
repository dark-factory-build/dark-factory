#!/usr/bin/env python3
import importlib.util
import json
import tempfile
import subprocess
import shutil
import os
import unittest
from pathlib import Path
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location('review_intake', Path(__file__).with_name('factory-review-intake.py'))
review = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(review)
SHA = 'a' * 40


class ReviewIntakeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.config = {'repository': 'o/r', 'project_id': '1' * 32, 'overseer_agent_id': '3' * 32,
                       'label': 'factory:ready', 'allowed_authors': ['maintainer'], 'factory_home': str(root / 'home'),
                       'journal': str(root / 'intake.json'), 'review_mirror_root': str(root / 'mirrors')}
        review.intake.atomic_json(Path(self.config['journal']), {'version': 2, 'updated_at': 0, 'config_fingerprint': review.intake.config_fingerprint(self.config),
            'issues': {'o/r#7': {'number': 7, 'managed': True}}})
        self.observe = patch.object(review, 'observe_review', return_value='allow').start()
        self.addCleanup(patch.stopall)
        self.operation = {'pr': 9, 'head': SHA, 'base': 'b' * 40, 'source_marker': 'FACTORY_SOURCE o/r#7',
                          'task_id': 'c' * 32, 'incarnation_id': 'd' * 32, 'priority': 0,
                          'title': 'resume', 'body': 'mirror'}

    def tearDown(self):
        self.temp.cleanup()

    def test_only_app_footer_linked_pr_is_woken_once_after_lost_response(self):
        prs = [{'number': 9, 'headRefOid': SHA, 'body': 'text\nRefs #7\n'}]
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review, 'verify_existing'):
            self.assertEqual(['woke PR #9 review allow'], review.run_once(self.config))
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(1, enqueue.call_count)
        self.assertIn("state allow", enqueue.call_args.args[1]["body"])
        receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())
        self.assertEqual("allow", receipt['pulls']['9:' + SHA]["review_state"])

    def test_unlinked_pr_is_not_woken(self):
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'untrusted Refs #8'}]), \
             patch.object(review, 'ready') as ready:
            self.assertEqual([], review.run_once(self.config))
        ready.assert_not_called()

    def test_mirror_must_match_app_reported_head(self):
        def command(argv, **_kwargs):
            if argv[-1].startswith('refs/pull'):
                return 'b' * 40 + '\n'
            return SHA + '\n'
        with patch.object(review.intake, 'command', side_effect=command):
            with self.assertRaisesRegex(review.ReviewError, 'exact head'):
                review.ready(self.config, Path('/mirror'), {'number': 9, 'headRefOid': SHA}, 7)

    def test_existing_head_keeps_its_recorded_base_when_main_advances(self):
        receipt = {'version': 2, 'config_fingerprint': review.config_fingerprint(self.config), 'pulls': {'9:' + SHA: self.operation}}
        Path(self.config['journal'] + '.reviews.json').write_text(json.dumps(receipt))
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', side_effect=AssertionError('must reuse persisted base')), patch.object(review, 'verify_existing') as verify, \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(1, verify.call_count)
        self.assertEqual('b' * 40, verify.call_args.args[2]['base'])

    def test_receipt_rejects_mirror_config_change(self):
        Path(self.config['journal'] + '.reviews.json').write_text(json.dumps({'version': 2, 'config_fingerprint': review.config_fingerprint(self.config), 'pulls': {}}))
        changed = dict(self.config, review_mirror_root='/other')
        with patch.object(review, 'mirror', return_value=Path('/mirror')):
            with self.assertRaisesRegex(review.ReviewError, 'receipt is invalid'):
                review.run_once(changed)

    def test_nonbare_mirror_is_refused(self):
        path = Path(self.config['review_mirror_root']) / 'o' / 'r'
        path.mkdir(parents=True)
        (path / 'HEAD').write_text('ref: refs/heads/main\n')
        with patch.object(review.intake, 'command', return_value='false\n'):
            with self.assertRaisesRegex(review.ReviewError, 'must be bare'):
                review.mirror(self.config)

    def test_one_launch_per_pass_and_uncertain_result_is_observed_not_replayed(self):
        prs = [{'number': n, 'headRefOid': SHA, 'body': 'Refs #7'} for n in (9, 10)]
        def ready(_config, _path, pr, _issue):
            return dict(self.operation, pr=pr['number'])
        self.observe.return_value = 'missing'
        with patch.object(review, 'mirror', return_value=Path('/mirror/o/r')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'ready', side_effect=ready), patch.object(review, 'verify_existing'), \
             patch.object(review, 'launch_review', return_value=124) as launch, \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            review.run_once(self.config)
            self.assertEqual(1, launch.call_count)
            review.run_once(self.config)
            self.assertEqual(2, launch.call_count)
            self.assertEqual([9, 10], [call.args[2]['number'] for call in launch.call_args_list])
            review.run_once(self.config)
            self.assertEqual(2, launch.call_count)
        receipts = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']
        self.assertTrue(all(v['review_attempted'] and v['review_state'] == 'unresolved' for v in receipts.values()))

    def test_block_is_completed_review_and_lost_wakeup_does_not_repeat_launch(self):
        self.observe.side_effect = ['missing', 'block', 'block']
        with patch.object(review, 'mirror', return_value=Path('/mirror/o/r')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'launch_review', return_value=1) as launch, \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue', side_effect=review.intake.IntakeError('lost response')) as enqueue:
            with self.assertRaises(review.intake.IntakeError):
                review.run_once(self.config)
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(1, launch.call_count)
        self.assertEqual(1, enqueue.call_count)
        self.assertIn('state block', enqueue.call_args.args[1]['body'])

    def test_launch_uses_host_boundary_exact_receipt_and_owned_group(self):
        operation = dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111')
        with patch.object(review.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0)) as run:
            self.assertEqual(0, review.launch_review(self.config, Path('/mirror/o/r'), {'number': 9, 'body': 'exact body'}, operation))
        argv = run.call_args.args[0]
        self.assertIn('go_gate_run_bounded', argv[2])
        self.assertEqual(['o/r', '9', SHA, 'b' * 40], argv[-5:-1])
        self.assertEqual('exact body', Path(argv[-1]).read_text())
        self.assertEqual('file:///mirror', run.call_args.kwargs['env']['DARK_FACTORY_REVIEW_REMOTE'])
        self.assertEqual(operation['review_operation'], run.call_args.kwargs['env']['DARK_FACTORY_REVIEW_OPERATION_ID'])
        self.assertNotIn('timeout', run.call_args.kwargs)

    def test_app_receipt_is_exact_and_observation_does_not_write(self):
        # Resolve the original function from a separate module, not the fixture mock.
        spec = importlib.util.spec_from_file_location('real_review', Path(review.__file__))
        real = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(real)
        bridge = Path(self.temp.name) / 'bridge'
        bridge.write_text('#!/bin/sh\nexit 0\n')
        bridge.chmod(0o700)
        operation = dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111')
        value = {'operation_id': operation['review_operation'], 'state': 'completed', 'kind': 'submit_pull_request_review',
                 'result': {'head_sha': SHA, 'verdict': 'allow'}}
        with patch.dict(real.os.environ, {'DARK_FACTORY_MAINTAINER_BRIDGE': str(bridge)}), \
             patch.object(real.subprocess, 'run') as run:
            run.return_value = subprocess.CompletedProcess([], 0, json.dumps({'id': 1, 'result': {'structuredContent': value}}), '')
            self.assertEqual('allow', real.observe_review(operation))
            request = json.loads(run.call_args.kwargs['input'])
            self.assertEqual('observe_operation', request['params']['name'])
            value['result']['head_sha'] = 'c' * 40
            run.return_value.stdout = json.dumps({'id': 1, 'result': {'structuredContent': value}})
            with self.assertRaisesRegex(real.ReviewError, 'exact head'):
                real.observe_review(operation)

    def test_timed_out_reviewer_group_is_gone_before_return(self):
        scripts = Path(self.temp.name) / 'scripts'
        scripts.mkdir()
        shutil.copy(Path(review.__file__).with_name('go-gate-environment.sh'), scripts)
        reviewer = scripts / 'cold-review.sh'
        reviewer.write_text('#!/bin/sh\nsleep 30 &\nchild=$!\necho $child > child.pid\ntrap \'wait "$child"; exit 130\' TERM\nwait "$child"\n')
        reviewer.chmod(0o700)
        real_run = subprocess.run
        def short_deadline(argv, **kwargs):
            argv[5] = '1'
            return real_run(argv, **kwargs)
        with patch.object(review, 'HERE', scripts), patch.object(review.subprocess, 'run', side_effect=short_deadline):
            status = review.launch_review(self.config, Path('/mirror/o/r'), {'number': 9, 'body': 'review'},
                                          dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111'))
        self.assertNotEqual(0, status)
        directory = Path(self.config['journal']).parent / ('review-9-' + SHA)
        child = int((directory / 'child.pid').read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(child, 0)


if __name__ == '__main__':
    unittest.main()
