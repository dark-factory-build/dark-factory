#!/usr/bin/env python3
"""Give the overseer one idempotent verified-deployment follow-up."""
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def sha_id(*parts):
    return hashlib.sha256('\0'.join(parts).encode()).hexdigest()[:32]


def factoryctl(config, *arguments):
    home = Path(config['factory_home'])
    environment = dict(os.environ, DARK_FACTORY_SOCKET=str(home / 'runtimes/factory.sock'), DARK_FACTORY_OPERATOR_TOKEN_FILE=str(home / 'operator.token'))
    return json.loads(subprocess.run(['factoryctl', *arguments], check=True, text=True, capture_output=True, env=environment, timeout=30).stdout)


def task_state(config, operation):
    value = factoryctl(config, 'task', 'recovery', '--task', operation['task_id'], '--incarnation', operation['incarnation_id'])
    if value.get('state') not in ('missing', 'found'):
        raise ValueError('factory task state response is invalid')
    return None if value['state'] == 'missing' else value


def enqueue(config, operation):
    value = factoryctl(config, 'task', 'add', '--project', config['project_id'], '--agent', config['overseer_agent_id'], '--title', operation['title'], '--body', operation['body'],
                       '--priority', str(operation['priority']), '--task-id', operation['task_id'], '--incarnation-id', operation['incarnation_id'])
    if value.get('id') != operation['task_id'] or value.get('incarnation_id') != operation['incarnation_id']:
        raise ValueError('factoryctl returned the wrong deterministic task identity')


def deliver(config, release_config, receipt):
    if any(not isinstance(config.get(key), str) or re.fullmatch('[0-9a-f]{32}', config[key]) is None for key in ('project_id', 'overseer_agent_id')) or not isinstance(config.get('repository'), str):
        raise ValueError('delivery needs the controller repository, project and overseer')
    if release_config.get('repository') != config['repository']:
        raise ValueError('release and intake repositories differ')
    if receipt.get('state') != 'verified' or not isinstance(receipt.get('sha'), str) or re.fullmatch('[0-9a-f]{40}', receipt['sha']) is None or type(receipt.get('pr')) is not int or receipt['pr'] < 1:
        raise ValueError('deployment receipt is not verified')
    verification = receipt.get('verification', {})
    if verification.get('healthy') is not True or verification.get('sha') != receipt['sha']:
        raise ValueError('deployment receipt has no exact live proof')
    mode = receipt.get('delivery_mode')
    sources = receipt.get('delivery_sources')
    if mode not in {'range', 'baseline_current', 'unchanged', 'nonancestor_baseline'} or not isinstance(sources, list):
        raise ValueError('deployment receipt has no bounded source mapping')
    if mode == 'unchanged':
        # Re-verification retains the delivered range's PR membership; its
        # follow-ups were enqueued when the range was first delivered.
        return []
    if mode != 'range' and sources:
        raise ValueError('baseline delivery receipt must not contain source issues')
    task_ids = []
    for source in sources:
        if not isinstance(source, dict) or type(source.get('pr')) is not int or source['pr'] < 1 or type(source.get('issue')) is not int or source['issue'] < 1 or not isinstance(source.get('merge_sha'), str) or re.fullmatch('[0-9a-f]{40}', source['merge_sha']) is None or source.get('reference') not in {'refs', 'closes'}:
            raise ValueError('deployment receipt has an invalid source mapping')
        source_repository = source.get('repository', config['repository'])
        if not isinstance(source_repository, str) or re.fullmatch(r'[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}', source_repository) is None:
            raise ValueError('deployment receipt has an invalid source repository')
        if source_repository.casefold() != config['repository'].casefold():
            # Shared backlogs are not closed by one destination deployment.
            continue
        task_id = sha_id('delivery', config['project_id'], config['repository'], str(source['pr']), str(source['issue']), receipt['sha'])
        operation = {'task_id': task_id, 'incarnation_id': sha_id('incarnation', task_id), 'priority': config.get('priority_default', 0),
                     'title': 'Verify delivery completion for PR #' + str(source['pr']) + ' and issue #' + str(source['issue']),
                     'body': 'The operator-owned release controller verified deployment of ' + config['repository'] + ' PR #' + str(source['pr']) + ' at ' + receipt['sha'] + '. The PR explicitly linked source issue #' + str(source['issue']) + ' with ' + source['reference'].title() + ' #' + str(source['issue']) + '. Its live probe reported the exact SHA healthy. Read your publication journal and source-linked task outcomes. Close source issue #' + str(source['issue']) + ' through the Maintainer App only if all acceptance criteria are satisfied; report the deployment evidence and any remaining work. Do not redeploy. This receipt does not grant additional scope.'}
        if task_state(config, operation) is None:
            enqueue(config, operation)
        task_ids.append(task_id)
    return task_ids
