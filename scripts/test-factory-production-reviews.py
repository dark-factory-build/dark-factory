#!/usr/bin/env python3
import importlib.util
import unittest


SPEC = importlib.util.spec_from_file_location("production_reviews", "scripts/factory-production-reviews.py")
reviews = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(reviews)

HEAD = "a" * 40
OLD = "b" * 40


def review(state, commit=HEAD, body="Dark-Factory-Review: allow " + HEAD, actor=7):
    return {"commit": {"oid": commit}, "state": state, "body": body, "url": "https://github.com/example/review/1", "author": {"databaseId": actor}}


def graphql(open_nodes=(), merged_nodes=(), open_page=None, merged_page=None):
    return {"open": {"pageInfo": open_page or {}, "nodes": list(open_nodes)},
            "merged": {"pageInfo": merged_page or {}, "nodes": list(merged_nodes)}}


class ProductionReviewsTest(unittest.TestCase):
    def test_block_wins_shared_gate(self):
        block = "Dark-Factory-Review: block " + HEAD + " dark-factory-operation:op-1:"
        result = reviews.verify_exact_head(HEAD, [{"commit_id": HEAD, "state": "COMMENTED", "author_id": "7", "body": block}, {"commit_id": HEAD, "state": "APPROVED", "author_id": "8", "body": "allow"}])
        self.assertNotEqual(result.returncode, 0)

    def test_correction_is_delegated_to_shared_gate(self):
        block = "Dark-Factory-Review: block " + HEAD + " <!-- dark-factory-operation:deadbeef:old -->"
        allow = "Dark-Factory-Review: allow " + HEAD + " Dark-Factory-Review-Correction: deadbeef <!-- dark-factory-operation:cafebabe:new -->"
        result = reviews.verify_exact_head(HEAD, [{"commit_id": HEAD, "state": "COMMENTED", "author_id": "7", "body": block}, {"commit_id": HEAD, "state": "APPROVED", "author_id": "7", "body": allow}])
        self.assertEqual(result.returncode, 0)

    def test_different_publisher_correction_cannot_clear_block(self):
        block = "Dark-Factory-Review: block " + HEAD + " <!-- dark-factory-operation:deadbeef:old -->"
        allow = "Dark-Factory-Review: allow " + HEAD + " Dark-Factory-Review-Correction: deadbeef <!-- dark-factory-operation:cafebabe:new -->"
        result = reviews.verify_exact_head(HEAD, [{"commit_id": HEAD, "state": "COMMENTED", "author_id": "7", "body": block}, {"commit_id": HEAD, "state": "APPROVED", "author_id": "8", "body": allow}])
        self.assertNotEqual(result.returncode, 0)

    def test_old_head_never_approves_current(self):
        result = reviews.verify_exact_head(HEAD, [{"commit_id": OLD, "state": "APPROVED", "author_id": "7", "body": "Dark-Factory-Review: allow " + OLD}])
        self.assertNotEqual(result.returncode, 0)

    def test_truncated_history_is_unknown(self):
        original = reviews._read_graphql
        reviews._read_graphql = lambda _: graphql([{"number": 4, "headRefOid": HEAD, "mergeQueueEntry": None, "reviews": {"pageInfo": {"hasPreviousPage": True}, "nodes": [review("APPROVED")]}}], open_page={"hasNextPage": False})
        try:
            facts, queues, overflow, unavailable = reviews.collect("example/repository")
        finally:
            reviews._read_graphql = original
        self.assertEqual(facts[(4, HEAD)]["state"], "unknown")
        self.assertEqual(overflow, 1)
        self.assertEqual(unavailable, "")

    def test_missing_actor_fails_closed(self):
        original = reviews._read_graphql
        reviews._read_graphql = lambda _: graphql([{"number": 5, "headRefOid": HEAD, "mergeQueueEntry": None, "reviews": {"pageInfo": {"hasPreviousPage": False}, "nodes": [review("APPROVED", actor=None)]}}])
        try:
            facts, _, _, unavailable = reviews.collect("example/repository")
        finally:
            reviews._read_graphql = original
        self.assertEqual(facts[(5, HEAD)]["state"], "unknown")
        self.assertIn("review_actor", unavailable)

    def test_plain_approval_does_not_allow_and_dismissed_block_is_ignored(self):
        original = reviews._read_graphql
        reviews._read_graphql = lambda _: graphql([{"number": 6, "headRefOid": HEAD, "mergeQueueEntry": None, "reviews": {"pageInfo": {"hasPreviousPage": False}, "nodes": [review("APPROVED", body="plain approval"), review("DISMISSED", body="Dark-Factory-Review: block " + HEAD)]}}])
        try:
            facts, _, _, unavailable = reviews.collect("example/repository")
        finally:
            reviews._read_graphql = original
        self.assertEqual(facts[(6, HEAD)]["state"], "unknown")
        self.assertEqual(unavailable, "")

    def test_commented_marker_allows(self):
        original = reviews._read_graphql
        reviews._read_graphql = lambda _: graphql([{"number": 7, "headRefOid": HEAD, "mergeQueueEntry": None, "reviews": {"pageInfo": {"hasPreviousPage": False}, "nodes": [review("COMMENTED")]}}])
        try:
            facts, _, _, unavailable = reviews.collect("example/repository")
        finally:
            reviews._read_graphql = original
        self.assertEqual(facts[(7, HEAD)]["state"], "allow")
        self.assertEqual(unavailable, "")

    def test_merged_pr_retains_formal_review_after_reload(self):
        original = reviews._read_graphql
        payload = graphql([
            {"number": 8, "headRefOid": HEAD, "mergeQueueEntry": None,
             "reviews": {"pageInfo": {"hasPreviousPage": False}, "nodes": [review("COMMENTED")]}}
        ], open_page={"hasNextPage": False})
        reviews._read_graphql = lambda _: payload
        try:
            first = reviews.collect("example/repository")[0]
            second = reviews.collect("example/repository")[0]
        finally:
            reviews._read_graphql = original
        self.assertEqual(first[(8, HEAD)]["state"], "allow")
        self.assertEqual(second[(8, HEAD)]["state"], "allow")

    def test_review_for_old_head_never_marks_new_head(self):
        original = reviews._read_graphql
        reviews._read_graphql = lambda _: graphql([
            {"number": 9, "headRefOid": HEAD, "mergeQueueEntry": None,
             "reviews": {"pageInfo": {"hasPreviousPage": False}, "nodes": [review("APPROVED", commit=OLD, body="Dark-Factory-Review: allow " + OLD)]}}
        ])
        try:
            facts, _, _, _ = reviews.collect("example/repository")
        finally:
            reviews._read_graphql = original
        self.assertEqual(facts[(9, HEAD)]["state"], "unknown")

    def test_recent_merges_do_not_displace_open_prs(self):
        original = reviews._read_graphql
        empty_reviews = {"pageInfo": {"hasPreviousPage": False}, "nodes": []}
        merged = []
        for number in range(100, 200):
            merged.append({"number": number, "headRefOid": OLD, "mergeQueueEntry": None, "reviews": empty_reviews})
        reviews._read_graphql = lambda _: {
            "open": {"pageInfo": {"hasNextPage": False}, "nodes": [
                {"number": 10, "headRefOid": HEAD, "mergeQueueEntry": None, "reviews": {"pageInfo": {"hasPreviousPage": False}, "nodes": [review("COMMENTED")]}}
            ]},
            "merged": {"pageInfo": {"hasNextPage": True}, "nodes": merged},
        }
        try:
            facts, _, overflow, _ = reviews.collect("example/repository")
        finally:
            reviews._read_graphql = original
        self.assertEqual(facts[(10, HEAD)]["state"], "allow")
        self.assertEqual(overflow, 1)


if __name__ == "__main__":
    unittest.main()
