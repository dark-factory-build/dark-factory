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
        self.observe = patch.object(review, 'observe_review', return_value='block').start()
        # No test may reach a live bridge; tests that need one substitute a fake.
        patch.object(review, 'bridge_call', side_effect=review.ReviewError('maintainer bridge is unavailable')).start()
        self.addCleanup(patch.stopall)
        self.operation = {'pr': 9, 'head': SHA, 'base': 'b' * 40, 'source_marker': 'FACTORY_SOURCE o/r#7',
                          'task_id': 'c' * 32, 'incarnation_id': 'd' * 32, 'priority': 0,
                          'title': 'resume', 'body': 'mirror'}

    def tearDown(self):
        self.temp.cleanup()

    def test_discovery_processes_one_bounded_overflow_pr_instead_of_starving_it(self):
        prs = [{'number': number, 'head': {'sha': ('%040d' % number)}, 'body': 'Refs #7'} for number in range(1, 12)]
        with patch.object(review.intake, 'command', return_value=json.dumps(prs)) as command:
            discovered = review.list_prs(dict(self.config, max_issues=10))
        self.assertEqual([{'number': number, 'headRefOid': ('%040d' % number), 'body': 'Refs #7'} for number in range(1, 12)], discovered)
        self.assertEqual(['--field', 'page=1'], command.call_args.args[0][-2:])

    def test_discovery_cursor_reaches_prs_beyond_first_bounded_page(self):
        page_one = [{'number': number, 'head': {'sha': ('%040d' % number)}, 'body': 'Refs #7'} for number in range(1, 12)]
        page_two = [{'number': 12, 'head': {'sha': '%040d' % 12}, 'body': 'Refs #7'}]
        with patch.object(review.intake, 'command', side_effect=[json.dumps(page_one), json.dumps(page_two)]) as command:
            self.assertEqual(11, len(review.list_prs(dict(self.config, max_issues=10), 1)))
            self.assertEqual([{'number': 12, 'headRefOid': '%040d' % 12, 'body': 'Refs #7'}], review.list_prs(dict(self.config, max_issues=10), 2))
        self.assertEqual('page=1', command.call_args_list[0].args[0][-1])
        self.assertEqual('page=2', command.call_args_list[1].args[0][-1])

    def test_only_app_footer_linked_pr_is_woken_once_after_lost_response(self):
        prs = [{'number': 9, 'headRefOid': SHA, 'body': 'text\nRefs #7\n'}]
        self.observe.return_value = 'allow'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review, 'verify_existing'), patch.object(review, 'verify_review_body'):
            with self.assertRaisesRegex(review.ReviewError, 'bridge is unavailable'):
                review.run_once(self.config)
        enqueue.assert_not_called()
        self.observe.return_value = 'block'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review, 'verify_existing'):
            self.assertEqual(['woke PR #9 review block'], review.run_once(self.config))
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(1, enqueue.call_count)
        self.assertIn("state block", enqueue.call_args.args[1]["body"])
        receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())
        self.assertEqual("block", receipt['pulls']['9:' + SHA]["review_state"])
        self.assertNotIn("enqueue_attempted", receipt['pulls']['9:' + SHA])

    def test_crlf_terminal_footer_links_managed_pr(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        self.assertEqual(7, review.linked_issue(self.config, {'number': 9, 'body': "Summary\r\nRefs #7\r\n"}, journal))

    def test_unlinked_pr_is_not_woken(self):
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'untrusted Refs #8'}]), \
             patch.object(review, 'ready') as ready:
            self.assertEqual([], review.run_once(self.config))
        ready.assert_not_called()

    def test_managed_footer_is_not_omitted_because_body_mentions_unmanaged_issue(self):
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #8\nRefs #7'}]), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review, 'verify_existing'), \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(7, review.linked_issue(self.config, {'number': 9, 'body': 'Refs #8\nRefs #7'}, json.loads(Path(self.config['journal']).read_text())))

    def test_managed_earlier_footer_is_ignored_when_unmanaged_footer_is_terminal(self):
        body = 'Refs #7\n\nRelated context: Refs #8\n'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': body}]), \
             patch.object(review, 'ready') as ready:
            self.assertEqual([], review.run_once(self.config))
        ready.assert_not_called()
        self.assertIsNone(review.linked_issue(self.config, {'number': 9, 'body': body}, json.loads(Path(self.config['journal']).read_text())))

    def test_multiple_managed_footers_fail_closed(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        journal['issues']['o/r#8'] = {'number': 8, 'managed': True}
        with self.assertRaisesRegex(review.ReviewError, 'multiple tracked'):
            review.linked_issue(self.config, {'number': 9, 'body': 'Refs #7\nCloses #8'}, journal)

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

    def test_legacy_version_two_receipt_without_review_provider_is_still_accepted(self):
        # Pre-review_provider receipts fingerprinted config without that
        # field (review.legacy_config_fingerprint). Upgrading this script
        # must not invalidate every journal already on disk.
        legacy_fingerprint = review.legacy_config_fingerprint(self.config)
        self.assertNotEqual(legacy_fingerprint, review.config_fingerprint(self.config))
        receipt = {'version': 2, 'config_fingerprint': legacy_fingerprint, 'pulls': {'9:' + SHA: self.operation}}
        Path(self.config['journal'] + '.reviews.json').write_text(json.dumps(receipt))
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', side_effect=AssertionError('must reuse the pre-change receipt')), patch.object(review, 'verify_existing') as verify, \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(1, verify.call_count)

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

    def test_verified_body_update_starts_one_independent_correction(self):
        old_body = 'stale body\nRefs #7\n'
        new_body = '111+ / 3- production delta\n' + ('x' * 80) + '\nRefs #7\n'
        operation = dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111')
        receipt = {'version': 2, 'config_fingerprint': review.config_fingerprint(self.config),
                   'pulls': {'9:' + SHA: operation}}
        Path(self.config['journal'] + '.reviews.json').write_text(json.dumps(receipt))
        snapshot = Path(self.config['journal']).parent / ('review-9-' + SHA)
        snapshot.mkdir()
        (snapshot / 'body.md').write_text(old_body)
        self.observe.side_effect = ['block', 'missing', 'block', 'missing']
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': new_body}]), \
             patch.object(review, 'verify_existing'), patch.object(review, 'app_update_receipt', return_value=True), \
             patch.object(review, 'launch_review', return_value=1) as launch, \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            review.run_once(self.config)
            review.run_once(self.config)
        self.assertEqual(1, launch.call_count)
        corrected = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
        self.assertEqual(operation['review_operation'], corrected['prior_review_operation'])
        self.assertEqual('block', corrected['prior_review_state'])
        self.assertNotEqual(operation['review_operation'], corrected['review_operation'])
        self.assertTrue(corrected['review_attempted'])
        self.assertEqual(new_body, (snapshot / 'body.md').read_text())

    def test_direct_body_edit_with_old_creation_marker_cannot_prove_update(self):
        operation_id = '11111111-1111-4111-8111-111111111111'
        body = 'direct author edit\n\n' + self.MARK % (operation_id, 'a' * 64)
        value = {'state': 'completed', 'kind': 'create_pull_request', 'request_digest': 'a' * 64,
                 'result': {'number': 9, 'url': 'https://github.com/o/r/pull/9'}}
        with patch.object(review, 'observe_operation', return_value=value):
            self.assertFalse(review.app_update_receipt(body, 9, 'o/r'))

    def test_app_update_receipt_binds_digest_to_exact_current_body(self):
        operation_id = '22222222-2222-4222-8222-222222222222'
        updated = 'verified metadata\nRefs #7\n'
        request = {'repository': 'o/r', 'operation_id': operation_id, 'pull_number': 9, 'body': updated}
        digest = review.hashlib.sha256(json.dumps(request, separators=(',', ':')).encode()).hexdigest()
        body = updated + '\n\n' + '<!-- dark-factory-operation:%s:%s -->' % (operation_id, digest)
        value = {'state': 'completed', 'kind': 'update_pull_request_body', 'request_digest': digest,
                 'result': {'number': 9, 'url': 'https://github.com/o/r/pull/9'}}
        with patch.object(review, 'observe_operation', return_value=value):
            self.assertTrue(review.app_update_receipt(body, 9, 'o/r'))
            self.assertFalse(review.app_update_receipt('changed\n\n' + body, 9, 'o/r'))

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
                 'result': {'head_sha': SHA, 'verdict': 'allow', 'url': 'https://github.com/o/r/pull/9#pullrequestreview-1'}}
        with patch.dict(real.os.environ, {'DARK_FACTORY_MAINTAINER_BRIDGE': str(bridge)}), \
             patch.object(real.subprocess, 'run') as run:
            run.return_value = subprocess.CompletedProcess([], 0, json.dumps({'id': 1, 'result': {'structuredContent': value}}), '')
            self.assertEqual('allow', real.observe_review(self.config, operation))
            request = json.loads(run.call_args.kwargs['input'])
            self.assertEqual('observe_operation', request['params']['name'])
            value['result']['head_sha'] = 'c' * 40
            run.return_value.stdout = json.dumps({'id': 1, 'result': {'structuredContent': value}})
            with self.assertRaisesRegex(real.ReviewError, 'exact head'):
                real.observe_review(self.config, operation)

    def test_correction_allow_must_name_prior_block_in_app_rendered_review(self):
        spec = importlib.util.spec_from_file_location('real_review', Path(review.__file__))
        real = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(real)
        operation = dict(self.operation, review_operation='22222222-2222-4222-8222-222222222222',
                         prior_review_operation='11111111-1111-4111-8111-111111111111')
        value = {'operation_id': operation['review_operation'], 'state': 'completed', 'kind': 'submit_pull_request_review',
                 'result': {'review_id': 77, 'head_sha': SHA, 'verdict': 'allow', 'url': 'https://github.com/o/r/pull/9#pullrequestreview-77'}}
        body = ('findings\n\nDark-Factory-Review: allow ' + SHA + '\n' +
                'Dark-Factory-Review-Correction: ' + operation['prior_review_operation'] + '\n' +
                '<!-- dark-factory-operation:' + operation['review_operation'] + ':digest -->')
        with patch.object(real, 'bridge_call', return_value={'structuredContent': value, 'isError': False}), \
             patch.object(real.intake, 'command', return_value=json.dumps([{'id': 77, 'commit_id': SHA, 'body': body}])):
            self.assertEqual('allow', real.observe_review(self.config, operation))
        bad = body.replace(operation['prior_review_operation'], '33333333-3333-4333-8333-333333333333')
        with patch.object(real, 'bridge_call', return_value={'structuredContent': value, 'isError': False}), \
             patch.object(real.intake, 'command', return_value=json.dumps([{'id': 77, 'commit_id': SHA, 'body': bad}])):
            with self.assertRaisesRegex(real.ReviewError, 'does not explicitly correct'):
                real.observe_review(self.config, operation)

    def test_review_for_another_pr_sharing_the_head_is_refused(self):
        # The App review result carries no PR number, so a completed ALLOW on
        # PR #10 at the same head must not be read as PR #9's verdict.
        spec = importlib.util.spec_from_file_location('real_review', Path(review.__file__))
        real = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(real)
        operation = dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111')
        def receipt(url):
            value = {'operation_id': operation['review_operation'], 'state': 'completed', 'kind': 'submit_pull_request_review',
                     'result': {'head_sha': SHA, 'verdict': 'allow', 'url': url}}
            return patch.object(real, 'bridge_call', return_value={'structuredContent': value, 'isError': False})
        with receipt('https://github.com/o/r/pull/9'):
            self.assertEqual('allow', real.observe_review(self.config, operation))
        with receipt('https://github.com/O/R/pull/9#pullrequestreview-77'):
            self.assertEqual('allow', real.observe_review(self.config, operation))
        for url in ('https://github.com/o/r/pull/10#pullrequestreview-1', 'https://github.com/o/r/pull/10?x=/pull/9', 'https://github.com/o/r/pull/10#/pull/9',
                    'https://github.com/o/r/pull/9#comment-1', 'https://github.com/o/r/pull/9?x=1', 'https://github.com/o/other/pull/9',
                    'http://github.com/o/r/pull/9', 'https://github.com@evil.example/o/r/pull/9', 'https://github.com/o/r/pull/90', 'https://github.com/o/r/pull/9/', None):
            with receipt(url), self.assertRaisesRegex(real.ReviewError, 'exact head and pull request', msg=str(url)):
                real.observe_review(self.config, operation)

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

    def enqueue_bridge(self, observe_states, write=None):
        """Fake bridge: observe_operation answers the given states in order, enqueue_pull_request answers write."""
        writes, states = [], list(observe_states)
        reviewed_body_digest = self.operation.get('reviewed_body_digest')
        def bridge_call(name, arguments):
            nonlocal reviewed_body_digest
            if name == 'observe_operation':
                state = states.pop(0)
                value = {'operation_id': arguments['operation_id'], 'state': state, 'kind': None, 'result': None, 'request_digest': None}
                if state == 'completed':
                    digest = review.enqueue_request_digest(self.config, {'enqueue_operation': arguments['operation_id'], 'pr': 9, 'head': SHA, 'enqueue_base': 'main', 'reviewed_body_digest': arguments.get('reviewed_body_digest') or reviewed_body_digest})
                    value.update(kind='enqueue_pull_request', request_digest=digest,
                                 result={'pull_number': 9, 'head_sha': SHA, 'entry_id': 'e', 'state_when_recorded': 'QUEUED'})
                return {'structuredContent': value, 'isError': False}
            self.assertEqual('enqueue_pull_request', name)
            # Durable before the write: the id and attempt marker are already on disk.
            receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
            self.assertEqual(arguments['operation_id'], receipt['enqueue_operation'])
            self.assertTrue(receipt['enqueue_attempted'])
            writes.append(arguments)
            reviewed_body_digest = arguments.get('reviewed_body_digest')
            if isinstance(write, Exception):
                raise write
            return write
        return bridge_call, writes

    def run_allow(self, bridge_call, task_state=None):
        self.observe.return_value = 'allow'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=dict(self.operation)), patch.object(review, 'verify_existing'), patch.object(review, 'bridge_call', side_effect=bridge_call), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, operation: operation.update(reviewed_body_digest='sha256:' + ('0' * 64))), patch.object(review, 'observe_merge', return_value={'state': 'ACTIVE_QUEUE', 'pull_state': 'open'}), patch.object(review.intake, 'task_state', side_effect=task_state or [None, {'status': 'queued'}]), patch.object(review.intake, 'enqueue') as enqueue:
            first = review.run_once(self.config)
            second = review.run_once(self.config)
        return first, second, enqueue, json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]

    def test_changed_body_between_allow_and_enqueue_fails_closed(self):
        reviewed = 'Refs #7'
        live_body = [reviewed]
        self.observe.return_value = 'allow'
        writes = []
        def bridge_call(name, arguments):
            if name == 'observe_operation':
                return {'structuredContent': {'operation_id': arguments['operation_id'], 'state': 'missing'}, 'isError': False}
            self.assertEqual('enqueue_pull_request', name)
            if live_body[0] != reviewed:
                return {'content': [{'type': 'text', 'text': 'conflict: pull request body changed after review'}], 'isError': True}
            writes.append(arguments)
            return {'structuredContent': {'pull_number': 9, 'head_sha': SHA, 'entry_id': 'e', 'state_when_recorded': 'QUEUED'}, 'isError': False}
        def verify(_config, _pr, operation):
            operation['reviewed_body_digest'] = 'sha256:' + ('0' * 64)
            live_body[0] = 'changed body\nRefs #7'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': reviewed}]), \
             patch.object(review, 'ready', return_value=dict(self.operation)), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=verify), patch.object(review, 'bridge_call', side_effect=bridge_call), \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            review.run_once(self.config)
        self.assertEqual([], writes)
        receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
        self.assertEqual('refused', receipt['enqueue_state'])
        self.assertIn('body changed after review', receipt['enqueue_refusal'])

    def test_not_queued_merge_observation_wakes_one_causal_failure_task(self):
        self.observe.return_value = 'allow'
        queued_operation = dict(self.operation)
        def mark_queued(_config, operation, _journal_path, _receipts):
            operation['enqueue_operation'] = '44444444-4444-4444-8444-444444444444'
            operation['enqueue_base'] = 'main'
            operation['enqueue_state'] = 'queued'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=queued_operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, operation: operation.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), patch.object(review, 'observe_merge', return_value={'state': 'NOT_QUEUED', 'pull_state': 'open'}), \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}, {'status': 'queued'}, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue:
            first = review.run_once(self.config)
            second = review.run_once(self.config)
        self.assertEqual(['woke PR #9 queue failure'], first)
        self.assertEqual([], second)
        self.assertEqual(1, enqueue.call_count)
        self.assertIn('NOT_QUEUED', enqueue.call_args.args[1]['body'])
        self.assertIn('44444444-4444-4444-8444-444444444444', enqueue.call_args.args[1]['body'])

    def test_closed_not_queued_merge_observation_stays_quiet(self):
        self.observe.return_value = 'allow'
        queued_operation = dict(self.operation)
        def mark_queued(_config, operation, _journal_path, _receipts):
            operation['enqueue_operation'] = '44444444-4444-4444-8444-444444444444'
            operation['enqueue_base'] = 'main'
            operation['enqueue_state'] = 'queued'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=queued_operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, operation: operation.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), patch.object(review, 'observe_merge', return_value={'state': 'NOT_QUEUED', 'pull_state': 'closed'}), \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}), patch.object(review.intake, 'enqueue') as enqueue:
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(0, enqueue.call_count)
        receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
        self.assertEqual(('NOT_QUEUED', 'closed'), (receipt['merge_state'], receipt['merge_pull_state']))

    def test_allow_enqueues_exact_head_once_and_rerun_observes_without_second_write(self):
        queued = {'structuredContent': {'pull_number': 9, 'head_sha': SHA, 'entry_id': 'e', 'state_when_recorded': 'QUEUED'}, 'isError': False}
        bridge_call, writes = self.enqueue_bridge(['missing', 'completed'], queued)
        first, second, task, receipt = self.run_allow(bridge_call)
        expected = str(review.uuid.uuid5(review.uuid.NAMESPACE_URL, 'dark-factory:host-enqueue:o/r:9:' + SHA))
        self.assertEqual([{'repository': 'o/r', 'operation_id': expected, 'pull_number': 9, 'head_sha': SHA, 'base': 'main', 'reviewed_body_digest': 'sha256:' + ('0' * 64)}], writes)
        self.assertEqual(('queued', expected, 'main', 'allow'), (receipt['enqueue_state'], receipt['enqueue_operation'], receipt['enqueue_base'], receipt['review_state']))
        self.assertEqual((['woke PR #9 review allow'], [], 1), (first, second, task.call_count))
        body = task.call_args.args[1]['body']
        self.assertIn('observe_pull_request_merge', body)
        self.assertIn(expected, body)
        self.assertIn('merge queue and required CI stay authoritative', body)
        self.assertNotIn('merged', receipt)

    def test_uncertain_enqueue_states_fail_closed_without_write(self):
        for states in (['planned'] * 2, ['executing'] * 2, ['indeterminate'] * 2):
            bridge_call, writes = self.enqueue_bridge(states)
            _first, _second, task, receipt = self.run_allow(bridge_call)
            self.assertEqual(([], 'unresolved', False), (writes, receipt['enqueue_state'], receipt.get('enqueue_attempted', False)))
            self.assertIn('never replay the write', task.call_args.args[1]['body'])
            Path(self.config['journal'] + '.reviews.json').unlink()
        # A write whose transport died is observed, never replayed.
        bridge_call, writes = self.enqueue_bridge(['missing'] * 3, review.ReviewError('enqueue_pull_request unavailable'))
        with self.assertRaisesRegex(review.ReviewError, 'unavailable'):
            self.run_allow(bridge_call)
        _first, _second, _task, receipt = self.run_allow(bridge_call)
        self.assertEqual((1, 'unresolved', True), (len(writes), receipt['enqueue_state'], receipt['enqueue_attempted']))

    def test_block_and_unresolved_review_do_not_enqueue(self):
        for verdict in ('block', 'missing'):
            self.observe.return_value = verdict
            with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
                 patch.object(review, 'ready', return_value=dict(self.operation)), patch.object(review, 'launch_review', return_value=1), \
                 patch.object(review, 'bridge_call') as bridge, patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
                review.run_once(self.config)
            bridge.assert_not_called()
            receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
            self.assertNotIn('enqueue_operation', receipt)
            Path(self.config['journal'] + '.reviews.json').unlink()

    def test_refused_and_conflicting_enqueue_are_recorded_not_retried(self):
        for text in ('refused: The request was refused: the pull request was already queued before this operation claimed it.',
                     'conflict: The exact-head, operation, or observed-object binding did not match.'):
            bridge_call, writes = self.enqueue_bridge(['missing', 'missing'], {'content': [{'type': 'text', 'text': text}], 'isError': True})
            _first, _second, task, receipt = self.run_allow(bridge_call)
            self.assertEqual((1, 'refused', text), (len(writes), receipt['enqueue_state'], receipt['enqueue_refusal']))
            self.assertEqual(1, task.call_count)
            self.assertIn(text, task.call_args.args[1]['body'])
            self.assertIn('Do not retry blindly', task.call_args.args[1]['body'])
            Path(self.config['journal'] + '.reviews.json').unlink()

    def test_refused_enqueue_state_survives_apps_planned_observation_on_replay(self):
        # The App persists a released refusal claim as journal state
        # "planned", not "missing" (control-plane journal.rs release_claim on
        # refusal). A later intake pass observing that "planned" state must
        # keep the concrete refusal already recorded, not degrade it to the
        # generic "unresolved" guidance.
        text = 'refused: The request was refused: the pull request was already queued before this operation claimed it.'
        bridge_call, writes = self.enqueue_bridge(['missing', 'planned'], {'content': [{'type': 'text', 'text': text}], 'isError': True})
        _first, _second, task, receipt = self.run_allow(bridge_call, task_state=[None, None])
        self.assertEqual((1, 'refused', text), (len(writes), receipt['enqueue_state'], receipt['enqueue_refusal']))
        self.assertEqual(2, task.call_count)
        body = task.call_args.args[1]['body']
        self.assertIn(text, body)
        self.assertIn('Do not retry blindly', body)
        self.assertNotIn('Its outcome is unresolved', body)

    def test_enqueue_receipt_for_another_head_is_refused(self):
        operation = dict(self.operation, enqueue_operation='11111111-1111-4111-8111-111111111111', enqueue_base='main')
        value = {'operation_id': operation['enqueue_operation'], 'state': 'completed', 'kind': 'enqueue_pull_request',
                 'request_digest': review.enqueue_request_digest(self.config, operation), 'result': {'pull_number': 9, 'head_sha': 'c' * 40}}
        with patch.object(review, 'bridge_call', return_value={'structuredContent': value, 'isError': False}):
            with self.assertRaisesRegex(review.ReviewError, 'exact head'):
                review.observe_enqueue(self.config, operation)
        # An operator-recorded enqueue id is preserved, never replaced by a derived one.
        self.operation['enqueue_operation'] = operation['enqueue_operation']
        self.operation['reviewed_body_digest'] = 'sha256:' + ('0' * 64)
        bridge_call, writes = self.enqueue_bridge(['completed', 'completed'])
        _first, _second, _task, receipt = self.run_allow(bridge_call)
        self.assertEqual((operation['enqueue_operation'], 'queued', []), (receipt['enqueue_operation'], receipt['enqueue_state'], writes))

    def test_uppercase_persisted_enqueue_operation_id_still_matches_digest(self):
        # The App canonicalizes operation_id to lowercase before journal
        # lookup and echoes that canonical (lowercase) id back in its reply
        # (control-plane/src/mcp.rs observe_operation, github_app.rs
        # canonical_operation_id): the fake bridge below must do the same, or
        # this test would pass even if observe_operation demanded a byte-exact
        # match against the uppercase id we sent. An operator-recorded id
        # persisted uppercase must still be recognized as the same completed
        # request, not falsely reported unresolved.
        operation = dict(self.operation, enqueue_operation='ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB', enqueue_base='main')
        canonical_id = operation['enqueue_operation'].lower()
        app_digest = review.enqueue_request_digest(self.config, dict(operation, enqueue_operation=canonical_id))
        value = {'operation_id': canonical_id, 'state': 'completed', 'kind': 'enqueue_pull_request',
                 'request_digest': app_digest, 'result': {'pull_number': 9, 'head_sha': SHA}}
        with patch.object(review, 'bridge_call', return_value={'structuredContent': value, 'isError': False}):
            self.assertEqual('queued', review.observe_enqueue(self.config, operation))

    def test_enqueue_receipt_for_another_request_digest_is_refused(self):
        # Same PR, same head, and a well-formed completed observation, but the
        # journaled request was for a different base (release instead of the
        # persisted expected main). Matching kind/pull_number/head_sha alone
        # must not be trusted: the digest binds the whole request.
        operation = dict(self.operation, enqueue_operation='11111111-1111-4111-8111-111111111111', enqueue_base='main')
        wrong_digest = review.enqueue_request_digest(self.config, dict(operation, enqueue_base='release'))
        value = {'operation_id': operation['enqueue_operation'], 'state': 'completed', 'kind': 'enqueue_pull_request',
                 'request_digest': wrong_digest, 'result': {'pull_number': 9, 'head_sha': SHA}}
        with patch.object(review, 'bridge_call', return_value={'structuredContent': value, 'isError': False}):
            with self.assertRaisesRegex(review.ReviewError, 'exact head'):
                review.observe_enqueue(self.config, operation)


    PR_OP, ISSUE_OP = '22222222-2222-5222-8222-222222222222', '33333333-3333-5333-8333-333333333333'
    MARK = '\n\n<!-- dark-factory-operation:%s:%s -->\n'

    def app_bridge(self, receipts, enqueue_states):
        """Fake bridge: App publication/issue receipts by operation id, then the enqueue fake for everything else."""
        enqueue_call, writes = self.enqueue_bridge(enqueue_states, {'structuredContent': {'pull_number': 9, 'head_sha': SHA, 'entry_id': 'e', 'state_when_recorded': 'QUEUED'}, 'isError': False})
        observed = []
        def bridge_call(name, arguments):
            receipt = receipts.get(arguments.get('operation_id')) if name == 'observe_operation' else None
            if receipt is None:
                return enqueue_call(name, arguments)
            observed.append(arguments['operation_id'])
            return {'structuredContent': dict(receipt, operation_id=arguments['operation_id']), 'isError': False}
        return bridge_call, writes, observed

    def test_app_published_pr_on_app_created_issue_reaches_review_then_enqueue(self):
        # Issue #20 was created by the App at the overseer's request and never
        # labelled, so intake never journaled it; PR #9 is the App's own
        # publication for it. Provenance is the two completed receipts.
        body = 'Change\n\nRefs #20' + self.MARK % (self.PR_OP, 'a' * 64)
        issue_body = 'Track it' + self.MARK % (self.ISSUE_OP, 'b' * 64)
        receipts = {self.PR_OP: {'state': 'completed', 'kind': 'create_pull_request', 'request_digest': 'a' * 64, 'result': {'number': 9, 'head_sha': SHA, 'url': 'https://github.com/o/r/pull/9'}},
                    self.ISSUE_OP: {'state': 'completed', 'kind': 'create_issue', 'request_digest': 'b' * 64, 'result': {'number': 20, 'url': 'https://github.com/o/r/issues/20'}}}
        bridge_call, writes, observed = self.app_bridge(receipts, ['missing', 'completed'])
        self.observe.side_effect = ['missing', 'allow', 'allow']
        operation = dict(self.operation, source_marker='FACTORY_SOURCE o/r#20')
        def ready(_config, _path, _pr, issue):
            self.assertEqual(20, issue)
            return dict(operation)
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': body}]), \
             patch.object(review, 'ready', side_effect=ready), patch.object(review, 'verify_existing'), patch.object(review, 'bridge_call', side_effect=bridge_call), \
             patch.object(review.intake, 'exact_issue', return_value={'number': 20, 'body': issue_body}) as exact, \
             patch.object(review, 'launch_review', return_value=0) as launch, patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, operation: operation.update(reviewed_body_digest='sha256:' + ('0' * 64))), patch.object(review, 'observe_merge', return_value={'state': 'ACTIVE_QUEUE', 'pull_state': 'open'}), \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), patch.object(review.intake, 'enqueue') as task:
            self.assertEqual(['woke PR #9 review allow'], review.run_once(self.config))
            self.assertEqual([], review.run_once(self.config))
        self.assertEqual(1, launch.call_count)
        self.assertEqual(str(review.uuid.uuid5(review.uuid.NAMESPACE_URL, 'dark-factory:host-review:o/r:9:' + SHA)), launch.call_args.args[3]['review_operation'])
        self.assertEqual([str(review.uuid.uuid5(review.uuid.NAMESPACE_URL, 'dark-factory:host-enqueue:o/r:9:' + SHA))], [write['operation_id'] for write in writes])
        self.assertIn('FACTORY_SOURCE o/r#20', task.call_args.args[1]['body'])
        # Provenance is proven once per exact head; the receipt carries it afterwards.
        self.assertEqual(([self.PR_OP, self.ISSUE_OP], 1), (observed, exact.call_count))
        receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
        self.assertEqual(('allow', 'queued'), (receipt['review_state'], receipt['enqueue_state']))

    def test_unproven_footers_are_refused_with_reason_and_never_reviewed(self):
        pr_receipt = {'state': 'completed', 'kind': 'create_pull_request', 'request_digest': 'a' * 64, 'result': {'number': 9, 'head_sha': SHA, 'url': 'https://github.com/o/r/pull/9'}}
        issue_receipt = {'state': 'completed', 'kind': 'create_issue', 'request_digest': 'b' * 64, 'result': {'number': 20, 'url': 'https://github.com/o/r/issues/20'}}
        marked = 'Refs #20' + self.MARK % (self.PR_OP, 'a' * 64)
        tracked = 'Track it' + self.MARK % (self.ISSUE_OP, 'b' * 64)
        cases = [
            ('Refs #20', {}, tracked, 'no completed App publication receipt'),                                   # not App-published
            (marked, {self.PR_OP: dict(pr_receipt, result={'number': 10, 'head_sha': SHA})}, tracked, 'no completed App publication receipt'),  # receipt for another PR
            (marked, {self.PR_OP: dict(pr_receipt, request_digest='c' * 64)}, tracked, 'no completed App publication receipt'),  # marker digest differs
            (marked, {self.PR_OP: pr_receipt}, 'plain issue', 'no completed create_issue receipt'),            # human issue
            (marked, {self.PR_OP: pr_receipt, self.ISSUE_OP: dict(issue_receipt, result={'number': 21})}, tracked, 'no completed create_issue receipt'),
            (marked, {self.PR_OP: pr_receipt, self.ISSUE_OP: dict(issue_receipt, kind='resolve_issue')}, tracked, 'no completed create_issue receipt'),
            (marked, {self.PR_OP: pr_receipt, self.ISSUE_OP: dict(issue_receipt, state='indeterminate')}, tracked, 'no completed create_issue receipt'),
            ('Refs #7\n\nRefs #20' + self.MARK % (self.PR_OP, 'a' * 64), {self.PR_OP: pr_receipt, self.ISSUE_OP: issue_receipt}, tracked, 'not the tracked source #7'),
        ]
        for body, receipts, issue_body, reason in cases:
            bridge_call, _writes, _observed = self.app_bridge(receipts, [])
            with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': body}]), \
                 patch.object(review, 'ready') as ready, patch.object(review, 'bridge_call', side_effect=bridge_call), \
                 patch.object(review.intake, 'exact_issue', return_value={'number': 20, 'body': issue_body}), patch.object(review, 'launch_review') as launch:
                messages = review.run_once(self.config)
            self.assertEqual(1, len(messages), body)
            self.assertTrue(messages[0].startswith('skipped PR #9: footer #20') or messages[0].startswith('skipped PR #9: terminal footer #20'), messages)
            self.assertIn(reason, messages[0])
            ready.assert_not_called()
            launch.assert_not_called()
            self.assertFalse(Path(self.config['journal'] + '.reviews.json').exists() and '9:' + SHA in json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls'])

    def test_foreign_or_ambiguous_app_urls_are_refused(self):
        body = 'Refs #20' + self.MARK % (self.PR_OP, 'a' * 64)
        issue_body = 'Track it' + self.MARK % (self.ISSUE_OP, 'b' * 64)
        base_pr = {'state': 'completed', 'kind': 'create_pull_request', 'request_digest': 'a' * 64,
                   'result': {'number': 9, 'head_sha': SHA, 'url': 'https://github.com/o/r/pull/9'}}
        base_issue = {'state': 'completed', 'kind': 'create_issue', 'request_digest': 'b' * 64,
                      'result': {'number': 20, 'url': 'https://github.com/o/r/issues/20'}}
        for url in ('https://github.com/other/repo/pull/9', 'http://github.com/o/r/pull/9',
                    'https://github.com/o/r/pull/9?x=1', 'https://user@github.com/o/r/pull/9'):
            receipts = {self.PR_OP: dict(base_pr, result=dict(base_pr['result'], url=url)), self.ISSUE_OP: base_issue}
            bridge_call, _writes, _observed = self.app_bridge(receipts, [])
            with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': body}]), \
                 patch.object(review, 'bridge_call', side_effect=bridge_call), \
                 patch.object(review.intake, 'exact_issue', return_value={'number': 20, 'body': issue_body}), patch.object(review, 'ready') as ready:
                messages = review.run_once(self.config)
            self.assertEqual(1, len(messages), url)
            self.assertIn('no completed App publication receipt', messages[0])
            ready.assert_not_called()


if __name__ == '__main__':
    unittest.main()
