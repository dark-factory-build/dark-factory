#!/usr/bin/env python3
import importlib.util
import fcntl
import sys
import json
import tempfile
import subprocess
import shutil
import os
import unittest
from pathlib import Path
from unittest.mock import Mock, patch


SPEC = importlib.util.spec_from_file_location('review_intake', Path(__file__).with_name('factory-review-intake.py'))
review = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(review)
SHA = 'a' * 40
CUSTOMER_BRIDGE = review.bridge_call
CUSTOMER_OBSERVE = review.observe_review


class ReviewIntakeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.config = {'repository': 'o/r', 'project_id': '1' * 32, 'overseer_agent_id': '3' * 32,
                       'label': 'factory:ready', 'allowed_authors': ['maintainer'], 'factory_home': str(root / 'home'),
                       'journal': str(root / 'intake.json'), 'review_mirror_root': str(root / 'mirrors')}
        Path(self.config['factory_home']).mkdir(mode=0o700)
        review.intake.atomic_json(Path(self.config['journal']), {'version': 2, 'updated_at': 0, 'config_fingerprint': review.intake.config_fingerprint(self.config),
            'issues': {'o/r#7': {'number': 7, 'managed': True, 'desired_fingerprint': 'e' * 64,
                                  'operation': {'task_id': 'c' * 32, 'incarnation_id': 'd' * 32}}}})
        self.observe = patch.object(review, 'observe_review', return_value='block').start()
        # No test may reach a live bridge; tests that need one substitute a fake.
        patch.object(review, 'bridge_call', side_effect=review.ReviewError('maintainer bridge is unavailable')).start()
        self.addCleanup(patch.stopall)
        self.operation = {'pr': 9, 'head': SHA, 'base': 'b' * 40, 'source_marker': 'FACTORY_SOURCE o/r#7',
                          'task_id': 'c' * 32, 'incarnation_id': 'd' * 32, 'priority': 0,
                          'title': 'resume', 'body': 'mirror'}

    def tearDown(self):
        self.temp.cleanup()

    def test_customer_modes_refuse_before_gh_or_legacy_bridge(self):
        path = Path(self.config['factory_home']) / 'maintainer.json'
        for state, record in [('connected', {'id':'a'*64,'credential':'fixture'}),
                              ('disconnected', {'disabled':True}),
                              ('expired', {'id':'b'*64,'credential':'expired-fixture'})]:
            with self.subTest(state=state):
                path.write_text(json.dumps(record))
                path.chmod(0o600)
                with patch.object(review.intake,'command') as command, patch.object(review,'bridge_call') as bridge, patch.object(review,'mirror') as mirror:
                    with self.assertRaisesRegex(review.ReviewError,'owner-only.*including disconnect'):
                        review.run_once(self.config)
                    command.assert_not_called()
                    bridge.assert_not_called()
                    mirror.assert_not_called()
        path.unlink()
        with patch.object(review,'mirror',return_value=Path('/mirror')), patch.object(review,'list_prs',return_value=[]):
            self.assertEqual([],review.run_once(self.config))

    def test_customer_companion_reviews_and_enqueues_exact_target_without_legacy_auth(self):
        (Path(self.config['factory_home'])/'maintainer.json').write_text('{"id":"fixture"}')
        request = {'source_id':'a'*32,'project_id':self.config['project_id'],
                   'configuration':{'repository':'o/r','target_repository_id':'b'*32},
                   'legacy':{'plan_hash':'c'*64,'config_hash':'d'*64,'journal_hash':'e'*64}}
        controller = Mock()
        managed = controller, {'request':request}, Path('/installed/factoryctl')
        operations, calls, enqueued = {}, [], set()
        pull = {'number':9,'body':'Reviewed publication '+SHA+'\n\nRefs o/r#7','head_sha':SHA,'base_sha':'b'*40,'base_ref':'release+candidate'}
        def api(_binary, _home, _args, value):
            if value['action']=='legacy_lineage':
                return {'state':'legacy_existing_work','task_id':'d'*32}  # Daemon-proven historical work.
            self.assertEqual('b'*32,value['configuration']['target_repository_id'])
            item=value['review']; tool=item['tool']; calls.append(tool)
            if tool=='configuration':
                return {'state':'ok','review':{'repository':'delivery/target','repository_id':42}}
            if tool=='list_pull_requests': result={'pull_requests':[pull],'repository_id':42,'next_page':None}
            elif tool=='observe_operation': result=operations.get(item['operation_id'],{'operation_id':item['operation_id'],'state':'missing'})
            elif tool=='submit_pull_request_review':
                self.assertEqual(7,value['issue_number'])
                result={'url':'https://github.com/delivery/target/pull/9#pullrequestreview-71','head_sha':SHA,'verdict':'allow','review_id':71}
                operations[item['operation_id']]={'operation_id':item['operation_id'],'state':'completed','kind':tool,'result':result}
            elif tool=='enqueue_pull_request':
                self.assertEqual('release+candidate',item['base'])
                self.assertEqual(7,value['issue_number'])
                result={'pull_number':9,'head_sha':SHA}
                operation={'enqueue_operation':item['operation_id'],'pr':9,'head':SHA,'enqueue_base':item['base'],'reviewed_body_digest':item['reviewed_body_digest']}
                operations[item['operation_id']]={'operation_id':item['operation_id'],'state':'completed','kind':tool,'result':result,'request_digest':review.enqueue_request_digest({'repository':'delivery/target'},operation)}
            elif tool=='observe_pull_request_merge': result={'pull_number':9,'head_sha':SHA,'base':'release+candidate','state':'MERGED_AFTER_ENQUEUE_ATTEMPT','pull_state':'closed'}
            else: self.fail('unexpected customer tool '+tool)
            return {'state':'ok','review':{'repository':'delivery/target','repository_id':42,'response':json.dumps({'jsonrpc':'2.0','id':1,'result':{'structuredContent':result,'isError':False}})}}
        controller.managed_api.side_effect=api
        def git(argv, **_kwargs):
            self.assertEqual('git',argv[0])  # No gh or legacy credential bridge.
            if 'rev-parse' in argv:
                return SHA if argv[-1].startswith('refs/pull/') else 'b'*40
            return ''
        def launch(config,_path,pr,operation):
            body=review.review_body_path(config,pr,operation);body.parent.mkdir(parents=True,exist_ok=True);body.write_text(pr['body'])
            review.bridge_call('submit_pull_request_review',{'repository':config['repository'],'operation_id':operation['review_operation'],'pull_number':9,'head_sha':SHA,'event':'ALLOW','body':'Independent review'})
            return 0
        with patch.object(review,'bridge_call',side_effect=CUSTOMER_BRIDGE), patch.object(review,'observe_review',side_effect=CUSTOMER_OBSERVE), patch.object(review,'mirror',return_value=Path('/mirror')) as mirror, patch.object(review.intake,'command',side_effect=git), patch.object(review,'launch_review',side_effect=launch) as launched, patch.object(review.intake,'task_state',side_effect=lambda _c,task:'queued' if task['task_id'] in enqueued else None), patch.object(review.intake,'enqueue',side_effect=lambda _c,task:enqueued.add(task['task_id'])):
            review.run_once(self.config,managed)
            review.run_once(self.config,managed)
            self.assertEqual('delivery/target',mirror.call_args.args[0]['repository'])
            self.assertEqual(1,launched.call_count)
        self.assertEqual(1,calls.count('enqueue_pull_request'))
        self.assertEqual(1,len(enqueued))
        self.assertEqual('o/r',self.config['repository'])
        self.assertIsNone(review.CUSTOMER_REVIEW)
        for state in ('denied','unavailable'):
            controller.managed_api.return_value={'state':state}; controller.managed_api.side_effect=None
            with patch.object(review.intake,'command') as command, patch.object(review,'mirror') as mirror:
                with self.assertRaisesRegex(review.ReviewError,'Customer review access unavailable'):
                    review.run_once(self.config,managed)
                command.assert_not_called();mirror.assert_not_called()

    def test_review_child_retains_same_lock_after_parent_scope_exits(self):
        child = None
        try:
            with review.review_ownership(self.config,None):
                descriptor = review.intake.CONTROLLER_LOCK_FD
                with patch.object(review.intake.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'ok','')) as run:
                    self.assertEqual('ok',review.intake.command(['fixture']))
                    self.assertEqual((descriptor,),run.call_args.kwargs['pass_fds'])
                child = subprocess.Popen([sys.executable,'-c','import sys; print("ready",flush=True); sys.stdin.read()'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,pass_fds=(descriptor,))
                self.assertEqual(b'ready\n',child.stdout.readline())
            self.assertIsNone(review.intake.CONTROLLER_LOCK_FD)
            with open(self.config['factory_home']+'.autonomy.lock','rb') as competing:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(competing,fcntl.LOCK_EX|fcntl.LOCK_NB)
                child.communicate(timeout=5)
                self.assertEqual(0,child.returncode)
                fcntl.flock(competing,fcntl.LOCK_EX|fcntl.LOCK_NB)
        finally:
            if child is not None and child.poll() is None:
                child.kill()
                child.communicate()

    def test_qualified_source_cannot_borrow_same_number_local_issue(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        self.assertIsNone(review.linked_issue(self.config, {'number': 9, 'body': 'Refs other/backlog#7'}, journal))
        self.assertEqual(7, review.linked_issue(self.config, {'number': 9, 'body': 'Refs O/R#7'}, journal))

    def test_post_cutover_human_issue_uses_imported_lineage_not_frozen_journal(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        before = json.dumps(journal, sort_keys=True)
        pr = {'number': 19, 'body': 'Refs o/r#8'}
        controller = Mock()
        receipt = {'request': {'source_id': 'a'*32, 'project_id': self.config['project_id'],
                              'configuration': {'repository': 'o/r', 'target_repository_id': 'b'*32}}}
        managed = controller, receipt, Path('/installed/factoryctl')
        controller.managed_api.return_value = {'state':'imported','acceptance_id':'c'*32,'task_id':'d'*32}
        with patch.object(review, 'app_receipt', return_value=True), patch.object(review.intake, 'exact_issue') as exact:
            self.assertEqual(8, review.linked_issue(self.config, pr, journal, managed=managed))
            exact.assert_not_called()  # Human source need not carry an App create_issue marker.
        request = controller.managed_api.call_args.args[3]
        self.assertEqual('legacy_lineage',request['action'])
        self.assertEqual(8,request['issue_number'])
        self.assertEqual('b'*32,request['configuration']['target_repository_id'])
        self.assertEqual(before,json.dumps(journal,sort_keys=True))
        for state in ('not_found','withdrawn','denied','unavailable'):
            controller.managed_api.return_value = {'state':state}
            with patch.object(review, 'app_receipt', side_effect=[True,False]), patch.object(review.intake, 'exact_issue', return_value={'body':'human instructions'}):
                with self.assertRaises(review.Unproven):
                    review.linked_issue(self.config,pr,journal,existing={'source_marker':'FACTORY_SOURCE o/r#8'},managed=managed)
        with patch.object(review, 'app_receipt', return_value=False), self.assertRaises(review.Unproven):
            review.linked_issue(self.config,pr,journal,managed=managed)

    def test_managed_followup_rechecks_lineage_and_freezes_operator_destination(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        pr = {'number':19,'body':'Refs o/r#8'}
        controller = Mock()
        receipt = {'request': {'source_id':'a'*32,'configuration':{'target_repository_id':'b'*32}}}
        managed = controller, receipt, Path('/installed/factoryctl')
        controller.managed_api.return_value = {'state':'imported','acceptance_id':'c'*32,'task_id':'d'*32}
        followup = review.review_followup(self.config,dict(self.operation,review_operation='e'*32),'block')
        with patch.object(review,'app_receipt',return_value=True), patch.object(review.intake,'command',return_value=json.dumps({'id':followup['task_id'],'incarnation_id':followup['incarnation_id']})) as command:
            self.assertTrue(review.enqueue_followup(self.config,followup,pr,journal,managed))
            self.assertEqual(['--repository','b'*32],command.call_args.args[0][-2:])
        controller.managed_api.return_value = {'state':'withdrawn'}
        with patch.object(review,'app_receipt',return_value=True), patch.object(review.intake,'exact_issue') as exact, patch.object(review.intake,'enqueue') as enqueue:
            self.assertFalse(review.enqueue_followup(self.config,followup,pr,journal,managed))
            enqueue.assert_not_called()
            exact.assert_not_called()  # An App-created source cannot bypass withdrawal.

    def test_managed_stale_body_followup_uses_frozen_route_and_rechecks_withdrawal(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        pr = {'number':9, 'headRefOid':SHA, 'body':'Old description\nRefs o/r#7'}
        controller = Mock()
        managed = controller, {'request': {'source_id':'a'*32, 'configuration':{'target_repository_id':'b'*32}}}, Path('/installed/factoryctl')
        for state in ('imported', 'withdrawn'):
            with self.subTest(state=state):
                receipt = Path(self.temp.name) / (state + '.reviews.json')
                controller.managed_api.side_effect = [{'state':'imported','task_id':'d'*32}, {'state':state,'task_id':'d'*32}]
                with patch.object(review,'list_prs',return_value=[pr]), patch.object(review,'ready',return_value=dict(self.operation)), patch.object(review,'observe_review',return_value='missing'), patch.object(review.intake,'task_state',return_value=None), patch.object(review.intake,'enqueue') as enqueue:
                    messages = review.run_locked(self.config,Path('/mirror'),journal,receipt,managed)
                    if state == 'withdrawn':
                        enqueue.assert_not_called()
                        self.assertEqual([],messages)
                    else:
                        self.assertEqual('b'*32,enqueue.call_args.args[1]['repository_id'])
                        self.assertEqual(['woke PR #9 stale body'],messages)
                self.assertEqual(2,controller.managed_api.call_count)
                controller.reset_mock()

    def test_legacy_journal_alone_cannot_authorize_managed_followup(self):
        journal = json.loads(Path(self.config['journal']).read_text())
        pr = {'number':9,'body':'Refs o/r#7'}
        controller = Mock()
        managed = controller, {'request': {'source_id':'a'*32,'configuration':{'target_repository_id':'b'*32}}}, Path('/installed/factoryctl')
        followup = review.review_followup(self.config,dict(self.operation,review_operation='e'*32),'block')
        for state in ('not_found','legacy_existing_work','imported'):
            controller.managed_api.return_value = {'state':state,'task_id':'d'*32}
            with patch.object(review.intake,'enqueue') as enqueue:
                self.assertEqual(state != 'not_found',review.enqueue_followup(self.config,followup,pr,journal,managed))
                self.assertEqual(state != 'not_found',enqueue.called)

    def test_discovery_processes_one_bounded_overflow_pr_instead_of_starving_it(self):
        prs = [{'number': number, 'head': {'sha': ('%040d' % number)}, 'body': 'Refs #7'} for number in range(1, 11)]
        with patch.object(review.intake, 'command', return_value=json.dumps(prs)) as command:
            discovered = review.list_prs(dict(self.config, max_issues=10))
        self.assertEqual([{'number': number, 'headRefOid': ('%040d' % number), 'body': 'Refs #7'} for number in range(1, 11)], discovered)
        self.assertEqual(['--field', 'page=1'], command.call_args.args[0][-2:])

    def test_discovery_cursor_reaches_prs_beyond_first_bounded_page(self):
        page_one = [{'number': number, 'head': {'sha': ('%040d' % number)}, 'body': 'Refs #7'} for number in range(1, 11)]
        page_two = [{'number': 11, 'head': {'sha': '%040d' % 11}, 'body': 'Refs #7'}]
        with patch.object(review.intake, 'command', side_effect=[json.dumps(page_one), json.dumps(page_two)]) as command:
            self.assertEqual(10, len(review.list_prs(dict(self.config, max_issues=10), 1)))
            self.assertEqual([{'number': 11, 'headRefOid': '%040d' % 11, 'body': 'Refs #7'}], review.list_prs(dict(self.config, max_issues=10), 2))
        self.assertEqual('page=1', command.call_args_list[0].args[0][-1])
        self.assertEqual('page=2', command.call_args_list[1].args[0][-1])

    def test_discovery_uses_github_page_cap_and_progresses_past_100(self):
        page_one = [{'number': number, 'head': {'sha': ('%040d' % number)}, 'body': 'Refs #7'} for number in range(1, 101)]
        page_two = [{'number': 101, 'head': {'sha': '%040d' % 101}, 'body': 'Refs #7'}]
        config = dict(self.config, max_issues=200)
        with patch.object(review.intake, 'command', side_effect=[json.dumps(page_one), json.dumps(page_two)]) as command:
            self.assertEqual(100, len(review.list_prs(config, 1)))
            self.assertEqual([{'number': 101, 'headRefOid': '%040d' % 101, 'body': 'Refs #7'}], review.list_prs(config, 2))
        self.assertEqual('per_page=100', command.call_args_list[0].args[0][-3])
        self.assertEqual('page=2', command.call_args_list[1].args[0][-1])
        self.assertEqual(100, review.discovery_batch_size(config))
        self.assertEqual(2, review.next_discovery_page(config, 1, len(page_one)))
        self.assertEqual(1, review.next_discovery_page(config, 2, len(page_two)))

    def test_discovery_rejects_page_over_configured_per_pass_cap(self):
        prs = [{'number': number, 'head': {'sha': ('%040d' % number)}, 'body': 'Refs #7'} for number in range(1, 27)]
        with patch.object(review.intake, 'command', return_value=json.dumps(prs)):
            with self.assertRaisesRegex(review.ReviewError, 'bounded discovery batch'):
                review.list_prs(dict(self.config, max_issues=25), 1)

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
        prs = [{'number': n, 'headRefOid': SHA, 'body': SHA + '\nRefs #7'} for n in (9, 10)]
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

    def test_body_naming_a_predecessor_head_wakes_overseer_without_spending_a_review(self):
        self.observe.return_value = 'missing'
        prs = [{'number': 9, 'headRefOid': SHA, 'body': 'published head ' + 'b' * 40 + '\nRefs #7'}]
        with patch.object(review, 'mirror', return_value=Path('/mirror/o/r')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'ready', return_value=self.operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'launch_review', return_value=0) as launch, \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'succeeded'}, None, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue:
            self.assertEqual(['woke PR #9 stale body'], review.run_once(self.config))
            self.assertEqual([], review.run_once(self.config))
            prs[0]['body'] = 'rewritten, head still missing\nRefs #7'
            self.assertEqual(['woke PR #9 stale body'], review.run_once(self.config))
            self.assertNotEqual(*[call.args[1]['task_id'] for call in enqueue.call_args_list])
            launch.assert_not_called()
            prs[0]['body'] = 'published head ' + SHA + '\nRefs #7'
            review.run_once(self.config)
        self.assertEqual(1, launch.call_count)
        self.assertEqual(2, enqueue.call_count)
        self.assertIn('does not name this exact head', enqueue.call_args.args[1]['body'])
        self.assertIn(SHA, enqueue.call_args.args[1]['body'])

    def test_block_is_completed_review_and_lost_wakeup_does_not_repeat_launch(self):
        self.observe.side_effect = ['missing', 'block', 'block']
        with patch.object(review, 'mirror', return_value=Path('/mirror/o/r')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': SHA + '\nRefs #7'}]), \
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
        new_body = '111+ / 3- production delta to ' + SHA + '\n' + ('x' * 80) + '\nRefs #7\n'
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

    def test_app_receipt_urls_match_only_the_canonical_object(self):
        operation_id = '22222222-2222-4222-8222-222222222222'
        request = {'repository': 'o/r', 'operation_id': operation_id, 'pull_number': 9, 'body': 'verified'}
        digest = review.hashlib.sha256(json.dumps(request, separators=(',', ':')).encode()).hexdigest()
        body = 'verified\n\n<!-- dark-factory-operation:%s:%s -->' % (operation_id, digest)
        canonical = 'https://github.com/o/r/pull/9'
        cases = [(canonical, True), (canonical.upper(), True)] + [(url, False) for url in (
            canonical + suffix for suffix in ('?x=1', '#fragment', ';params', '/', '?', '#'))]
        cases += [(url, False) for url in (
            'http://github.com/o/r/pull/9', 'https://github.com/other/repo/pull/9',
            'https://user@github.com/o/r/pull/9', 'https://github.com:443/o/r/pull/9',
            'https://github.com.evil/o/r/pull/9', 'https://[github.com/o/r/pull/9',
            'https://github.com/o/r/pull/%39', 'https://github.com/o/r/issues/9',
            '\n' + canonical, canonical.replace('github', 'git\nhub'), None)]
        for url, accepted in cases:
            with self.subTest(url=url):
                value = {'state': 'completed', 'kind': 'update_pull_request_body', 'request_digest': digest,
                         'result': {'number': 9, 'url': url}}
                with patch.object(review, 'observe_operation', return_value=value):
                    self.assertEqual(accepted, review.app_receipt(body, {'update_pull_request_body'}, 9, 'o/r', 'pull'))
                    self.assertEqual(accepted, review.app_update_receipt(body, 9, 'o/r'))

    def test_launch_uses_host_boundary_exact_receipt_and_owned_group(self):
        operation = dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111')
        process = Mock(pid=123, wait=Mock(return_value=0))
        with patch.object(review.subprocess, 'Popen', return_value=process) as popen, patch.object(review, 'process_start', return_value='Sat Sep 20 12:00:00 2026'):
            self.assertEqual(0, review.launch_review(self.config, Path('/mirror/o/r'), {'number': 9, 'body': 'exact body'}, operation))
        argv = popen.call_args.args[0]
        self.assertIn('go_gate_run_bounded', argv[2])
        self.assertEqual(['o/r', '9', SHA, 'b' * 40], argv[-5:-1])
        self.assertEqual('exact body', Path(argv[-1]).read_text())
        self.assertEqual('file:///mirror', popen.call_args.kwargs['env']['DARK_FACTORY_REVIEW_REMOTE'])
        self.assertEqual(operation['review_operation'], popen.call_args.kwargs['env']['DARK_FACTORY_REVIEW_OPERATION_ID'])
        self.assertNotIn('timeout', popen.call_args.kwargs)
        activity = json.loads(review.review_activity_path(self.config, {'number': 9}, operation).read_text())
        self.assertEqual({'operation': operation['review_operation'], 'pr': 9, 'head': SHA, 'repository': 'o/r', 'pid': 123, 'process_start': 'Sat Sep 20 12:00:00 2026', 'exit': 0}, {key: activity[key] for key in ('operation', 'pr', 'head', 'repository', 'pid', 'process_start', 'exit')})

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
        real_popen = subprocess.Popen
        def short_deadline(argv, **kwargs):
            if argv[:2] == ['/bin/sh', '-c']:
                argv = list(argv)
                argv[5] = '1'
            return real_popen(argv, **kwargs)
        with patch.object(review, 'HERE', scripts), patch.object(review.subprocess, 'Popen', side_effect=short_deadline):
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
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': SHA + '\nRefs #7'}]), \
             patch.object(review, 'ready', return_value=queued_operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, operation: operation.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), patch.object(review, 'observe_merge', return_value={'state': 'NOT_QUEUED', 'pull_state': 'open'}), \
             patch.object(review, 'send_back_merge_failure', return_value='merge queue CI failed: jobs=checks; tests=TestFixture') as send_back, \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}, {'status': 'queued'}, {'status': 'queued'}]), \
             patch.object(review.intake, 'enqueue') as enqueue:
            first = review.run_once(self.config)
            second = review.run_once(self.config)
        self.assertEqual(['sent back PR #9 queue failure: merge queue CI failed: jobs=checks; tests=TestFixture', 'woke PR #9 review allow'], first)
        self.assertEqual([], second)
        self.assertEqual(1, enqueue.call_count)
        self.assertEqual(1, send_back.call_count)

    def test_dropped_entry_sends_bounded_merge_group_failure_to_source_task(self):
        operation = dict(self.operation, enqueue_operation='44444444-4444-4444-8444-444444444444',
                         enqueue_observed_at=100, merge_observed_at=200)
        run = {'workflow_runs': [
            {'id': 70, 'event': 'merge_group', 'head_sha': 'b' * 40, 'created_at': '1970-01-01T00:01:00Z',
             'pull_requests': [{'number': 9}]},
            {'id': 71, 'event': 'merge_group', 'head_sha': SHA, 'created_at': '1970-01-01T00:02:30Z',
             'pull_requests': [{'number': 9}]}]}
        jobs = {'jobs': [{'id': 72, 'name': 'macOS full gate', 'conclusion': 'failure'},
                         {'id': 73, 'name': 'required', 'conclusion': 'success'}]}
        calls = []
        def command(argv, **kwargs):
            calls.append(argv)
            if argv[1] == 'api' and '/actions/runs?' in argv[2]:
                return json.dumps(run)
            if any('/actions/runs/71/jobs' in item for item in argv):
                return json.dumps(jobs)
            if argv[1:3] == ['run', 'view']:
                return 'macOS full gate / TestDaemonSourceTitleOnlyWorkerUsesEffectiveHandoff failed\n'
            return '{}'
        with patch.object(review.intake, 'command', side_effect=command):
            note = review.send_back_merge_failure(self.config, operation)
        self.assertEqual('merge queue CI failed: jobs=macOS full gate; tests=TestDaemonSourceTitleOnlyWorkerUsesEffectiveHandoff. Exact head ' + SHA + '.', note)
        send_back = calls[-1]
        self.assertEqual(['factoryctl', 'task', 'send-back', '--task', 'c' * 32, '--note', note], send_back)

    def test_failed_pre_review_gate_is_persisted_and_not_rerun(self):
        bare = Path(self.temp.name) / 'bare'
        bare.mkdir()
        (bare / 'HEAD').write_text('ref: refs/heads/main\n')
        operation = dict(self.operation, review_operation='11111111-1111-4111-8111-111111111111')
        evidence = Path(self.temp.name) / 'gate.json'
        evidence.write_text(json.dumps({'head': SHA, 'base': operation['base'], 'exit_code': 1}))
        (Path(self.temp.name) / 'gate.log').write_text('FAIL TestGateFixture\n')
        self.observe.return_value = 'missing'
        with patch.object(review, 'mirror', return_value=bare), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': SHA + '\nRefs #7'}]), \
             patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'run_full_gate', return_value=evidence) as gate, \
             patch.object(review, 'send_back_source_task', return_value='sent') as send_back:
            first = review.run_once(self.config)
            second = review.run_once(self.config)
        self.assertEqual(1, gate.call_count)
        self.assertEqual(1, send_back.call_count)
        self.assertIn('pre-review gate failure', first[0])
        self.assertEqual([], second)
        receipt = json.loads(Path(self.config['journal'] + '.reviews.json').read_text())['pulls']['9:' + SHA]
        self.assertEqual(('failed', True), (receipt['gate_state'], receipt['gate_failure_sent_back']))

    def test_merge_observation_reuses_reviewed_digest_after_body_edit(self):
        operation = dict(self.operation, enqueue_operation='44444444-4444-4444-8444-444444444444',
                         enqueue_base='main', reviewed_body_digest='sha256:' + ('0' * 64))
        observed = {'structuredContent': {'pull_number': 9, 'head_sha': SHA, 'base': 'main',
                     'pull_state': 'open', 'state': 'ACTIVE_QUEUE'}, 'isError': False}
        with patch.object(review, 'bridge_call', return_value=observed) as bridge:
            self.assertEqual({'state': 'ACTIVE_QUEUE', 'pull_state': 'open'}, review.observe_merge(self.config, operation))
        self.assertEqual('sha256:' + ('0' * 64), bridge.call_args.args[1]['reviewed_body_digest'])

    def test_legacy_merge_observation_preserves_original_request_binding(self):
        operation = dict(self.operation, enqueue_operation='44444444-4444-4444-8444-444444444444', enqueue_base='main')
        observed = {'structuredContent': {'pull_number': 9, 'head_sha': SHA, 'base': 'main',
                     'pull_state': 'open', 'state': 'ACTIVE_QUEUE'}, 'isError': False}
        with patch.object(review, 'bridge_call', return_value=observed) as bridge:
            self.assertEqual({'state': 'ACTIVE_QUEUE', 'pull_state': 'open'}, review.observe_merge(self.config, operation))
        self.assertNotIn('reviewed_body_digest', bridge.call_args.args[1])

    def test_queued_replay_observes_after_body_edit_without_revalidation(self):
        self.observe.return_value = 'allow'
        operation = dict(self.operation, enqueue_operation='44444444-4444-4444-8444-444444444444',
                         enqueue_base='main', enqueue_state='queued', enqueue_attempted=True,
                         reviewed_body_digest='sha256:' + ('0' * 64), review_state='allow')
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'edited after enqueue\nRefs #7'}]), \
             patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body') as verify, patch.object(review, 'enqueue_allowed'), \
                     patch.object(review, 'observe_merge', return_value={'state': 'NOT_QUEUED', 'pull_state': 'open'}), \
                     patch.object(review, 'send_back_merge_failure', return_value='merge queue CI failed: jobs=checks; tests=TestFixture'), \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued'}]), patch.object(review.intake, 'enqueue') as enqueue:
            self.assertEqual(['sent back PR #9 queue failure: merge queue CI failed: jobs=checks; tests=TestFixture', 'woke PR #9 review allow'], review.run_once(self.config))
        verify.assert_not_called()
        self.assertEqual(1, enqueue.call_count)

    def test_queued_replay_stays_quiet_for_closed_active_and_merged(self):
        for observation in (
            {'state': 'NOT_QUEUED', 'pull_state': 'closed'},
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open'},
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open', 'queue_state': 'AWAITING_CHECKS'},
            {'state': 'MERGED_AFTER_ENQUEUE_ATTEMPT', 'pull_state': 'closed'},
        ):
            with self.subTest(observation=observation):
                operation = dict(self.operation, enqueue_operation='44444444-4444-4444-8444-444444444444',
                                 enqueue_base='main', enqueue_state='queued', enqueue_attempted=True,
                                 reviewed_body_digest='sha256:' + ('0' * 64), review_state='allow')
                observed = dict(observation)
                with patch.object(review, 'mirror', return_value=Path('/mirror')), \
                     patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'edited after enqueue\nRefs #7'}]), \
                     patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
                     patch.object(review, 'verify_review_body') as verify, patch.object(review, 'enqueue_allowed'), \
                     patch.object(review, 'observe_merge', return_value=observed), \
                     patch.object(review.intake, 'task_state', return_value={'status': 'queued'}), patch.object(review.intake, 'enqueue') as enqueue:
                    self.observe.return_value = 'allow'
                    self.assertEqual([], review.run_once(self.config))
                verify.assert_not_called()
                enqueue.assert_not_called()

    def test_merge_queue_wait_task_settles_without_a_provider_run(self):
        self.observe.return_value = 'allow'
        operation = dict(self.operation)
        def mark_queued(_config, current, _journal_path, _receipts):
            current.update(enqueue_operation='44444444-4444-4444-8444-444444444444', enqueue_base='main', enqueue_state='queued')
        observations = [
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open', 'queue_state': 'AWAITING_CHECKS'},
            {'state': 'MERGED_AFTER_ENQUEUE_ATTEMPT', 'pull_state': 'closed'},
        ]
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, current: current.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), patch.object(review, 'observe_merge', side_effect=observations), \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'blocked', 'revision': 3}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review.intake, 'command', return_value='') as command, \
             patch.object(review, 'launch_review') as launch:
            first = review.run_once(self.config)
            second = review.run_once(self.config)
        self.assertEqual(['waiting PR #9 merge queue'], first)
        self.assertEqual(['settled PR #9 merged publication'], second)
        self.assertEqual(1, enqueue.call_count)
        launch.assert_not_called()
        self.assertIn('--publication-state', command.call_args.args[0])
        self.assertIn('succeeded', command.call_args.args[0])

    def test_merged_journaled_pr_is_reconciled_when_absent_from_open_discovery(self):
        self.observe.return_value = 'allow'
        operation = dict(self.operation)
        def mark_queued(_config, current, _journal_path, _receipts):
            current.update(enqueue_operation='44444444-4444-4444-8444-444444444444', enqueue_base='main', enqueue_state='queued')
        merge_states = [
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open', 'queue_state': 'AWAITING_CHECKS'},
            {'state': 'MERGED_AFTER_ENQUEUE_ATTEMPT', 'pull_state': 'closed'},
        ]
        lists = [[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}], [], []]
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', side_effect=lists), \
             patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, current: current.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), patch.object(review, 'observe_merge', side_effect=merge_states), \
             patch.object(review, 'journaled_pr', return_value={'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}), \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'blocked', 'revision': 3}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review.intake, 'command', return_value='') as command, \
             patch.object(review, 'launch_review') as launch:
            self.assertEqual(['waiting PR #9 merge queue'], review.run_once(self.config))
            self.assertEqual(['settled PR #9 merged publication'], review.run_once(self.config))
            self.assertEqual([], review.run_once(self.config))
        launch.assert_not_called()
        self.assertEqual(1, enqueue.call_count)
        task_id = enqueue.call_args.args[1]['task_id']
        update = command.call_args.args[0]
        self.assertEqual(task_id, update[update.index('--task') + 1])
        self.assertIn('succeeded', update)

    def test_closed_unmerged_journaled_pr_routes_failure_when_absent_from_open_discovery(self):
        self.observe.return_value = 'allow'
        operation = dict(self.operation)
        def mark_queued(_config, current, _journal_path, _receipts):
            current.update(enqueue_operation='44444444-4444-4444-8444-444444444444', enqueue_base='main', enqueue_state='queued')
        merge_states = [
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open', 'queue_state': 'AWAITING_CHECKS'},
            {'state': 'NOT_QUEUED', 'pull_state': 'closed'},
        ]
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', side_effect=[[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}], []]), \
             patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, current: current.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), patch.object(review, 'observe_merge', side_effect=merge_states), \
             patch.object(review, 'journaled_pr', return_value={'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}), \
             patch.object(review, 'send_back_merge_failure', return_value='dropped after enqueue') as send_back, \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued', 'revision': 4}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review.intake, 'command', return_value='') as command:
            self.assertEqual(['waiting PR #9 merge queue'], review.run_once(self.config))
            self.assertEqual(['settled PR #9 dropped publication'], review.run_once(self.config))
        send_back.assert_called_once()
        self.assertEqual(1, enqueue.call_count)
        update = command.call_args.args[0]
        self.assertIn('--publication-state', update)
        self.assertIn('failed', update)

    def test_blocked_review_followup_is_admissible(self):
        task = Mock()
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': 'Refs #7'}]), \
             patch.object(review, 'ready', return_value=dict(self.operation)), patch.object(review, 'verify_existing'), \
             patch.object(review.intake, 'task_state', return_value=None), patch.object(review.intake, 'enqueue', side_effect=task) as enqueue:
            self.assertEqual(['woke PR #9 review block'], review.run_once(self.config))
        self.assertEqual(1, enqueue.call_count)
        self.assertNotIn('Factory publication wait', enqueue.call_args.args[1]['body'])

    def test_waiting_task_becomes_admissible_after_unmergeable_transition(self):
        self.observe.return_value = 'allow'
        operation = dict(self.operation)
        def mark_queued(_config, current, _journal_path, _receipts):
            current.update(enqueue_operation='44444444-4444-4444-8444-444444444444', enqueue_base='main', enqueue_state='queued')
        observations = [
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open', 'queue_state': 'AWAITING_CHECKS'},
            {'state': 'ACTIVE_QUEUE', 'pull_state': 'open', 'queue_state': 'UNMERGEABLE'},
        ]
        with patch.object(review, 'mirror', return_value=Path('/mirror')), \
             patch.object(review, 'list_prs', return_value=[{'number': 9, 'headRefOid': SHA, 'body': SHA + '\nRefs #7'}]), \
             patch.object(review, 'ready', return_value=operation), patch.object(review, 'verify_existing'), \
             patch.object(review, 'verify_review_body', side_effect=lambda _config, _pr, current: current.update(reviewed_body_digest='sha256:' + ('0' * 64))), \
             patch.object(review, 'enqueue_allowed', side_effect=mark_queued), \
             patch.object(review, 'observe_merge', side_effect=observations), \
             patch.object(review.intake, 'task_state', side_effect=[None, {'status': 'queued', 'revision': 3}]), \
             patch.object(review.intake, 'enqueue') as enqueue, patch.object(review.intake, 'command', return_value='') as command:
            self.assertEqual(['waiting PR #9 merge queue'], review.run_once(self.config))
            self.assertEqual(['made PR #9 publication followup admissible'], review.run_once(self.config))
        self.assertEqual(1, enqueue.call_count)
        task_id = enqueue.call_args.args[1]['task_id']
        self.assertEqual(task_id, command.call_args.args[0][command.call_args.args[0].index('--task') + 1])
        self.assertIn('--body', command.call_args.args[0])
        self.assertNotIn('Factory publication wait', command.call_args.args[0][command.call_args.args[0].index('--body') + 1])

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

    def test_oversized_app_source_is_skipped_and_following_pr_is_processed(self):
        body = 'Refs #20' + self.MARK % (self.PR_OP, 'a' * 64)
        prs = [{'number': 9, 'headRefOid': SHA, 'body': body},
               {'number': 10, 'headRefOid': 'b' * 40, 'body': body.replace('9', '10')}]
        operation = dict(self.operation, pr=10, head='b' * 40, source_marker='FACTORY_SOURCE o/r#20')
        with patch.object(review, 'mirror', return_value=Path('/mirror')), patch.object(review, 'list_prs', return_value=prs), \
             patch.object(review, 'app_receipt', side_effect=[True, True, True]), \
             patch.object(review.intake, 'exact_issue', side_effect=[review.intake.IssueBodyTooLarge('too large'), {'number': 20, 'body': 'tracked'}]), \
             patch.object(review, 'ready', return_value=operation) as ready, patch.object(review, 'verify_existing'), \
             patch.object(review.intake, 'task_state', return_value={'status': 'queued'}):
            messages = review.run_once(self.config)
        self.assertEqual(1, len(messages))
        self.assertIn('skipped PR #9', messages[0])
        self.assertIn('body exceeds the intake limit', messages[0])
        self.assertEqual(1, ready.call_count)
        self.assertEqual(10, ready.call_args.args[2]['number'])


if __name__ == '__main__':
    unittest.main()
