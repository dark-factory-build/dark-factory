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

    def test_only_app_footer_linked_pr_is_woken_once_after_lost_response(self):
        prs = [{'number': 9, 'headRefOid': SHA, 'body': 'text\nRefs #7\n'}]
        self.observe.return_value = 'allow'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review, 'verify_existing'):
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
        self.assertEqual(7, review.linked_issue("Summary\r\nRefs #7\r\n", journal, 'o/r'))

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
        self.assertEqual(7, review.linked_issue('Refs #8\nRefs #7', json.loads(Path(self.config['journal']).read_text()), 'o/r'))

    def test_managed_earlier_footer_is_ignored_when_unmanaged_footer_is_terminal(self):
        body = 'Refs #7\n\nRelated context: Refs #8\n'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': body}]), \
             patch.object(review, 'ready') as ready:
            self.assertEqual([], review.run_once(self.config))
        ready.assert_not_called()
        self.assertIsNone(review.linked_issue(body, json.loads(Path(self.config['journal']).read_text()), 'o/r'))

    def test_multiple_managed_footers_fail_closed(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        journal['issues']['o/r#8'] = {'number': 8, 'managed': True}
        with self.assertRaisesRegex(review.ReviewError, 'multiple tracked'):
            review.linked_issue('Refs #7\nCloses #8', journal, 'o/r')

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
        def bridge_call(name, arguments):
            if name == 'observe_operation':
                state = states.pop(0)
                value = {'operation_id': arguments['operation_id'], 'state': state, 'kind': None, 'result': None, 'request_digest': None}
                if state == 'completed':
                    digest = review.enqueue_request_digest(self.config, {'enqueue_operation': arguments['operation_id'], 'pr': 9, 'head': SHA, 'enqueue_base': 'main'})
                    value.update(kind='enqueue_pull_request', request_digest=digest,
                                 result={'pull_number': 9, 'head_sha': SHA, 'entry_id': 'e', 'state_when_recorded': 'QUEUED'})
                return {'structuredContent': value, 'isError': False}
            self.assertEqual('enqueue_pull_request', name)
            # Durable before the write: the id and attempt marker are already on disk.
            receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
            self.assertEqual(arguments['operation_id'], receipt['enqueue_operation'])
            self.assertTrue(receipt['enqueue_attempted'])
            writes.append(arguments)
            if isinstance(write, Exception):
                raise write
            return write
        return bridge_call, writes

    def run_allow(self, bridge_call, task_state=None):
        self.observe.return_value = 'allow'
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=dict(self.operation)), patch.object(review, 'verify_existing'), patch.object(review, 'bridge_call', side_effect=bridge_call), \
             patch.object(review.intake, 'task_state', side_effect=task_state or [None, {'status': 'queued'}]), patch.object(review.intake, 'enqueue') as enqueue:
            first = review.run_once(self.config)
            second = review.run_once(self.config)
        return first, second, enqueue, json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]

    def test_allow_enqueues_exact_head_once_and_rerun_observes_without_second_write(self):
        queued = {'structuredContent': {'pull_number': 9, 'head_sha': SHA, 'entry_id': 'e', 'state_when_recorded': 'QUEUED'}, 'isError': False}
        bridge_call, writes = self.enqueue_bridge(['missing', 'completed'], queued)
        first, second, task, receipt = self.run_allow(bridge_call)
        expected = str(review.uuid.uuid5(review.uuid.NAMESPACE_URL, 'dark-factory:host-enqueue:o/r:9:' + SHA))
        self.assertEqual([{'repository': 'o/r', 'operation_id': expected, 'pull_number': 9, 'head_sha': SHA, 'base': 'main'}], writes)
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

    def test_enqueue_receipt_for_another_head_is_refused(self):
        operation = dict(self.operation, enqueue_operation='11111111-1111-4111-8111-111111111111', enqueue_base='main')
        value = {'operation_id': operation['enqueue_operation'], 'state': 'completed', 'kind': 'enqueue_pull_request',
                 'request_digest': review.enqueue_request_digest(self.config, operation), 'result': {'pull_number': 9, 'head_sha': 'c' * 40}}
        with patch.object(review, 'bridge_call', return_value={'structuredContent': value, 'isError': False}):
            with self.assertRaisesRegex(review.ReviewError, 'exact head'):
                review.observe_enqueue(self.config, operation)
        # An operator-recorded enqueue id is preserved, never replaced by a derived one.
        bridge_call, writes = self.enqueue_bridge(['completed', 'completed'])
        self.operation['enqueue_operation'] = operation['enqueue_operation']
        _first, _second, _task, receipt = self.run_allow(bridge_call)
        self.assertEqual((operation['enqueue_operation'], 'queued', []), (receipt['enqueue_operation'], receipt['enqueue_state'], writes))

    def test_uppercase_persisted_enqueue_operation_id_still_matches_digest(self):
        # The App canonicalizes operation_id to lowercase before hashing
        # (control-plane/src/github_app.rs canonical_operation_id); an
        # operator-recorded id persisted uppercase must still be recognized
        # as the same completed request, not falsely reported unresolved.
        operation = dict(self.operation, enqueue_operation='ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB', enqueue_base='main')
        app_digest = review.enqueue_request_digest(self.config, dict(operation, enqueue_operation=operation['enqueue_operation'].lower()))
        value = {'operation_id': operation['enqueue_operation'], 'state': 'completed', 'kind': 'enqueue_pull_request',
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



if __name__ == '__main__':
    unittest.main()
