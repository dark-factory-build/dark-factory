// Immutable git archive scan at 6eb17496a343edb8e0d27c45aa49d77aedd97510; fixture actors and proposals are synthetic.
// Inventory samples span the exact manifest below; source revision and file counts are unchanged.
import type { TopologyView } from "@dark-factory/client";
const topology: TopologyView = {
  "digest": "fa733d2221abdba59ac808803d5c3a5d9367963d33703b8d1045ec987a2a8054",
  "sources": [
    {
      "repository_id": "22222222222222222222222222222222",
      "prefix": "",
      "kind": "integrated",
      "target_ref": "refs/remotes/origin/main",
      "revision": "6eb17496a343edb8e0d27c45aa49d77aedd97510",
      "observed_at": 1791115200000
    }
  ],
  "nodes": [
    {
      "id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "parent_id": "6af719e7296c98fe46f194701a767b10e6666f6295d9019b71cfcad9e31f71ec",
      "kind": "module",
      "label": "github.com/dark-factory-build/dark-factory",
      "language": "go",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 8,
          "configuration": 3,
          "assets": 0,
          "unclassified": 1
        },
        "total": {
          "source": 397,
          "tests": 321,
          "documentation": 34,
          "configuration": 114,
          "assets": 3,
          "unclassified": 9
        },
        "samples": [
          ".gitignore",
          "AGENTS.md",
          "ARCHITECTURE.md",
          "CLAUDE.md",
          "CONTRIBUTING.md",
          "LICENSE",
          "README.md",
          "ROADMAP.md",
          "SECURITY.md",
          "VERSION",
          "go.mod",
          "go.sum"
        ],
        "samples_omitted": 0
      },
      "path": "."
    },
    {
      "id": "6af719e7296c98fe46f194701a767b10e6666f6295d9019b71cfcad9e31f71ec",
      "parent_id": "",
      "kind": "repository",
      "label": "repository",
      "language": "",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 8,
          "configuration": 3,
          "assets": 0,
          "unclassified": 1
        },
        "total": {
          "source": 397,
          "tests": 321,
          "documentation": 34,
          "configuration": 114,
          "assets": 3,
          "unclassified": 9
        },
        "samples": [
          ".gitignore",
          "AGENTS.md",
          "ARCHITECTURE.md",
          "CLAUDE.md",
          "CONTRIBUTING.md",
          "LICENSE",
          "README.md",
          "ROADMAP.md",
          "SECURITY.md",
          "VERSION",
          "go.mod",
          "go.sum"
        ],
        "samples_omitted": 0
      },
      "path": "."
    },
    {
      "id": "6374261abf1b88de3ff111c751b7a8c3f7331f3ddc7964da84d0dcf72f3cfa37",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "directory",
      "label": "cmd",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 17,
          "tests": 24,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "cmd"
    },
    {
      "id": "13489b7aaf406663d61f7c9173c9bad8ecff9452f04a7f3034c9a52d7e9deaa5",
      "parent_id": "6374261abf1b88de3ff111c751b7a8c3f7331f3ddc7964da84d0dcf72f3cfa37",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "main.go",
          "main_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "cmd/cloudflare-admin"
    },
    {
      "id": "367ed2395b35ed9419eab08b5d5dc797c79e79a915e182b3b6e4a2c61d4cfb39",
      "parent_id": "6374261abf1b88de3ff111c751b7a8c3f7331f3ddc7964da84d0dcf72f3cfa37",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "main.go",
          "main_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "cmd/factory-runner"
    },
    {
      "id": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
      "parent_id": "6374261abf1b88de3ff111c751b7a8c3f7331f3ddc7964da84d0dcf72f3cfa37",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 14,
          "tests": 21,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 14,
          "tests": 21,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "agent_paths_test.go",
          "browser_darwin.go",
          "browser_other.go",
          "community.go",
          "community_test.go",
          "content_parse_test.go",
          "github.go",
          "github_test.go",
          "human_options_test.go",
          "intake_legacy.go",
          "intake_legacy_test.go",
          "intake_review_test.go",
          "intake_service.go",
          "intake_service_test.go",
          "intake_test.go",
          "local_ci_identity_darwin_test.go",
          "main.go",
          "main_test.go",
          "maintainer.go",
          "maintainer_test.go",
          "mcp.go",
          "operator_test.go",
          "outcomes.go",
          "outcomes_test.go",
          "production.go",
          "production_test.go",
          "remote.go",
          "remote_test.go",
          "repository_readiness_test.go",
          "review.go",
          "review_test.go",
          "web_test.go"
        ],
        "samples_omitted": 3
      },
      "path": "cmd/factoryctl"
    },
    {
      "id": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
      "parent_id": "6374261abf1b88de3ff111c751b7a8c3f7331f3ddc7964da84d0dcf72f3cfa37",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "main.go",
          "main_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "cmd/factoryd"
    },
    {
      "id": "e7c8a3f2db32adc4e0129f2bb16d92faae28ce08dbcadaf0da355f1be081d65c",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "package",
      "label": "dark-factory-control-plane-worker-tests",
      "language": "javascript",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 1,
          "configuration": 6,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 14,
          "tests": 5,
          "documentation": 1,
          "configuration": 7,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          ".gitignore",
          "Cargo.lock",
          "Cargo.toml",
          "README.md",
          "package-lock.json",
          "package.json",
          "wrangler.toml"
        ],
        "samples_omitted": 0
      },
      "path": "control-plane"
    },
    {
      "id": "be655945b48949c37245d853d5f32cebf163e3ddc2d16f1f3d6cb4809d387362",
      "parent_id": "e7c8a3f2db32adc4e0129f2bb16d92faae28ce08dbcadaf0da355f1be081d65c",
      "kind": "directory",
      "label": "migrations",
      "language": "",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 3,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "0001_maintainer_deliveries.sql",
          "0004_maintainer_operations.sql",
          "0005_operation_authorities.sql"
        ],
        "samples_omitted": 0
      },
      "path": "control-plane/migrations"
    },
    {
      "id": "7824b6a82521ebfaa811b07eb29746b0378f00e4f71506852955145071eac7fd",
      "parent_id": "e7c8a3f2db32adc4e0129f2bb16d92faae28ce08dbcadaf0da355f1be081d65c",
      "kind": "directory",
      "label": "scripts",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 4,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 4,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "build-worker.sh",
          "local-ci.sh",
          "test-clean-wrangler-env.sh",
          "with-clean-wrangler-env.sh"
        ],
        "samples_omitted": 0
      },
      "path": "control-plane/scripts"
    },
    {
      "id": "aaf1b2a15adb365bdc61cfe4e471d8ba226a74b85160d0f4bb76daaf4b0ef3ed",
      "parent_id": "e7c8a3f2db32adc4e0129f2bb16d92faae28ce08dbcadaf0da355f1be081d65c",
      "kind": "directory",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 7,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 7,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "access.rs",
          "connection.rs",
          "github_app.rs",
          "journal.rs",
          "lib.rs",
          "maintainer.rs",
          "mcp.rs"
        ],
        "samples_omitted": 0
      },
      "path": "control-plane/src"
    },
    {
      "id": "1cef24f8c2629542fc8397a68a3c9caef5d8ae1e1f3805655d3d7105ab5c9e88",
      "parent_id": "e7c8a3f2db32adc4e0129f2bb16d92faae28ce08dbcadaf0da355f1be081d65c",
      "kind": "directory",
      "label": "tests",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 5,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 5,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "cloudflare_boundary.rs",
          "connections.integration.test.mjs",
          "inactive_default.rs",
          "maintainer_webhook.rs",
          "worker.integration.test.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "control-plane/tests"
    },
    {
      "id": "b07c1a2a7ea93833c0033d9e094581f684efc62ecb4cf2fe3444a60f9a8b7e1d",
      "parent_id": "1cef24f8c2629542fc8397a68a3c9caef5d8ae1e1f3805655d3d7105ab5c9e88",
      "kind": "directory",
      "label": "fixtures",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "repository-without-administration.json"
        ],
        "samples_omitted": 0
      },
      "path": "control-plane/tests/fixtures"
    },
    {
      "id": "5481968d59ba8592e30e00819e2e29971df1b8c752e20c2e9e0d56ee136a4c79",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "directory",
      "label": "docs",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 2,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 21,
          "configuration": 0,
          "assets": 2,
          "unclassified": 4
        },
        "samples": [
          "install.md",
          "providers.md"
        ],
        "samples_omitted": 0
      },
      "path": "docs"
    },
    {
      "id": "f124bc6b90d3c6cfa9a020b0fa4f00e6954e9c60308fd803f0adb78f3c1093cf",
      "parent_id": "5481968d59ba8592e30e00819e2e29971df1b8c752e20c2e9e0d56ee136a4c79",
      "kind": "directory",
      "label": "assets",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 2,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 2,
          "unclassified": 0
        },
        "samples": [
          "factory-floor-demo-mobile.png",
          "factory-floor-demo.png"
        ],
        "samples_omitted": 0
      },
      "path": "docs/assets"
    },
    {
      "id": "015e0f1ba8311d33542e9be2631ee7c1086c39cc6c207820bbf16fd2e1c9e356",
      "parent_id": "5481968d59ba8592e30e00819e2e29971df1b8c752e20c2e9e0d56ee136a4c79",
      "kind": "directory",
      "label": "canary",
      "language": "",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 4
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 4
        },
        "samples": [
          "issue-origin-2.txt",
          "issue-origin.txt",
          "operator-origin-3.txt",
          "operator-origin-4.txt"
        ],
        "samples_omitted": 0
      },
      "path": "docs/canary"
    },
    {
      "id": "ea600ad7ee893339c35cbe2d3e2d45fbb3e1a778aa40b8acefeba3ace44ee1c5",
      "parent_id": "5481968d59ba8592e30e00819e2e29971df1b8c752e20c2e9e0d56ee136a4c79",
      "kind": "directory",
      "label": "development",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 19,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 19,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "ARCHITECTURE_REVIEW.md",
          "ATTEMPT_RESULT_RECOVERY.md",
          "BROWSER_BOUNDARY_SIMPLIFICATION_HANDOVER.md",
          "DEPLOY.md",
          "FACTORY_FLOOR.md",
          "GITHUB_APP.md",
          "GITHUB_CONNECTIONS.md",
          "GO_REWRITE.md",
          "ONBOARDING_SETTINGS_REFINEMENT_HANDOFF.md",
          "OPTIONAL_KNOWLEDGE.md",
          "OVERSEER.md",
          "OVERSEER_SNAPSHOT_REDUCTION.md",
          "PIXEL_WORLD.md",
          "PRODUCTISATION.md",
          "PROJECT_CONTENT_HANDOFF.md",
          "SCRIPTS-AUDIT.md",
          "UNATTENDED.md",
          "WORKFLOW.md",
          "YIELDED_CONTINUATION.md"
        ],
        "samples_omitted": 0
      },
      "path": "docs/development"
    },
    {
      "id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "directory",
      "label": "internal",
      "language": "",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 219,
          "tests": 250,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "internal"
    },
    {
      "id": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "api",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 16,
          "tests": 19,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 16,
          "tests": 19,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "attempt_source_test.go",
          "attempt_task_test.go",
          "client.go",
          "client_test.go",
          "content_client.go",
          "github.go",
          "github_test.go",
          "intake.go",
          "intake_budget_test.go",
          "intake_legacy.go",
          "intake_legacy_test.go",
          "intake_review_test.go",
          "intake_test.go",
          "maintainer.go",
          "maintainer_test.go",
          "outcome_client.go",
          "overseer_control_test.go",
          "path_ancestor_darwin.go",
          "path_ancestor_other.go",
          "path_race_test.go",
          "path_unix.go",
          "peer_darwin_test.go",
          "peer_linux.go",
          "peer_status_test.go",
          "production.go",
          "production_test.go",
          "remote_contract_test.go",
          "server.go",
          "server_test.go",
          "snapshot_reply_test.go",
          "task_recovery_test.go",
          "web_contract_test.go"
        ],
        "samples_omitted": 3
      },
      "path": "internal/api"
    },
    {
      "id": "d73a555d38f2dc324eda4f4ce026fad8b7ca053114075c2c86cb7ae5dcc5f884",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "browser",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 3,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "agent_control_test.go",
          "backend.go",
          "connection.go",
          "connection_identity_test.go",
          "console_control_test.go",
          "pair_test.go",
          "review_repairs_test.go",
          "server.go",
          "server_test.go",
          "task_enqueue_test.go",
          "terminal_transport_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/browser"
    },
    {
      "id": "ddf439b2340c50af3038277dcbf5caf0c912774280b07e900dae261d170a9e69",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "browserprotocol",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 11,
          "tests": 9,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 11,
          "tests": 9,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "agent_control.go",
          "binary.go",
          "console_control.go",
          "console_control_test.go",
          "github_control.go",
          "github_control_test.go",
          "human_options_test.go",
          "project_content.go",
          "project_content_test.go",
          "remote_control.go",
          "remote_control_test.go",
          "state.go",
          "state_test.go",
          "task_control.go",
          "task_control_test.go",
          "terminal_control.go",
          "transcript.go",
          "transcript_test.go",
          "wire.go",
          "wire_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/browserprotocol"
    },
    {
      "id": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "buildinfo",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 2,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "artifact.go",
          "artifact_test.go",
          "buildinfo.go",
          "buildinfo_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/buildinfo"
    },
    {
      "id": "db22efa118602cbde3351b4dadbbc10af97a9219b57a395bd8410b776eba7caa",
      "parent_id": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
      "kind": "directory",
      "label": "cmd",
      "language": "",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "internal/buildinfo/cmd"
    },
    {
      "id": "4d64296b0b7ce3e3198a84ceaecadce3f7ff2578bd8e43f41c8952f309aefe81",
      "parent_id": "db22efa118602cbde3351b4dadbbc10af97a9219b57a395bd8410b776eba7caa",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "main.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/buildinfo/cmd/release-artifact"
    },
    {
      "id": "36ce3b617704d288a88bf3b9e867069db35f2830bf511ae72cd78be85055c579",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "change",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 8,
          "tests": 6,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 8,
          "tests": 6,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "content_darwin.go",
          "errors.go",
          "git.go",
          "git_darwin.go",
          "git_darwin_test.go",
          "git_external_darwin_test.go",
          "git_fake_darwin_test.go",
          "git_protocol_darwin_test.go",
          "git_unsupported.go",
          "repository_source.go",
          "repository_source_darwin.go",
          "repository_source_darwin_test.go",
          "repository_source_unsupported.go",
          "worktree_darwin_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/change"
    },
    {
      "id": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "changeworker",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 4,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 4,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "authority_darwin_test.go",
          "cache_darwin_test.go",
          "codec.go",
          "codec_test.go",
          "worker.go",
          "worker_darwin.go",
          "worker_darwin_test.go",
          "worker_unsupported.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/changeworker"
    },
    {
      "id": "f0ac6681df86312628dad4347e2cc661650009cee21302173ed6d9971116f656",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "cloudflareadmin",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 7,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 7,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "admin.go",
          "admin_test.go",
          "credentials.go",
          "credentials_test.go",
          "credentials_unix.go",
          "credentials_unsupported.go",
          "dns.go",
          "stat_darwin.go",
          "stat_linux.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/cloudflareadmin"
    },
    {
      "id": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "daemon",
      "language": "go",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 63,
          "tests": 78,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 63,
          "tests": 78,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "accounts.go",
          "api_dispatch_test.go",
          "browser_agent_control.go",
          "browser_console_test.go",
          "browser_lifecycle.go",
          "browser_operator_test.go",
          "browser_subscription.go",
          "browser_terminal_backend_test.go",
          "database_test.go",
          "handover_darwin.go",
          "intake_legacy.go",
          "intake_status.go",
          "live_attempt_darwin.go",
          "live_attempt_unsupported.go",
          "maintainer_test.go",
          "peer_question_test.go",
          "provider_defaults.go",
          "push_test.go",
          "recovery_darwin_test.go",
          "release.go",
          "repository_darwin.go",
          "repository_readiness_darwin_test.go",
          "review_api_test.go",
          "review_verdict_test.go",
          "runtime_darwin_test.go",
          "runtime_unsupported_test.go",
          "settlement_darwin_test.go",
          "success_source_proposal_darwin_test.go",
          "supervisor_darwin.go",
          "supervisor_unsupported.go",
          "terminal_effects_darwin_test.go",
          "topology_test.go"
        ],
        "samples_omitted": 109
      },
      "path": "internal/daemon"
    },
    {
      "id": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "e2e_test",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 5,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 5,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "browser_pty_darwin_test.go",
          "daemon_blackbox_darwin_test.go",
          "daemon_handover_darwin_test.go",
          "database_darwin_test.go",
          "service_blackbox_darwin_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/e2e"
    },
    {
      "id": "7eb5d689a166500d297916d0ec8645c9b23c6e14b0687eecdcfd13997f98532f",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "gitauthor",
      "language": "go",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "identity.go",
          "identity_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/gitauthor"
    },
    {
      "id": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "install",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 15,
          "tests": 9,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 15,
          "tests": 9,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "install.go",
          "install_darwin.go",
          "install_darwin_test.go",
          "install_unsupported.go",
          "local_api_darwin.go",
          "local_api_darwin_test.go",
          "maintainer.go",
          "maintainer_darwin.go",
          "maintainer_darwin_test.go",
          "maintainer_unsupported.go",
          "operational_darwin.go",
          "operational_darwin_test.go",
          "operational_unsupported.go",
          "operational_unsupported_test.go",
          "paths.go",
          "service.go",
          "service_darwin.go",
          "service_darwin_test.go",
          "service_manage_darwin.go",
          "service_manage_darwin_test.go",
          "service_unsupported.go",
          "service_unsupported_test.go",
          "toolchain.go",
          "toolchain_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/install"
    },
    {
      "id": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "kernel",
      "language": "go",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 56,
          "tests": 69,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 56,
          "tests": 69,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "account_test.go",
          "agent_stop_test.go",
          "attempt_result_test.go",
          "browser_authority_test.go",
          "change_settlement_repair_test.go",
          "console_update_test.go",
          "continuation.go",
          "finalizing_footprint_test.go",
          "human_request_test.go",
          "intake.go",
          "intake_priority.go",
          "lifecycle_read.go",
          "migrate_test.go",
          "outcomes.go",
          "overseer_test.go",
          "peer_question_test.go",
          "production_test.go",
          "project_tokens.go",
          "public_state.go",
          "repository.go",
          "repository_identity.go",
          "resource_declared_test.go",
          "send_back.go",
          "sqlite.go",
          "sqlite_crash_test.go",
          "sqlite_lifecycle_cancellation_test.go",
          "store.go",
          "task_intervention.go",
          "task_recovery.go",
          "terminal_session.go",
          "test_database_test.go",
          "validated_write_test.go"
        ],
        "samples_omitted": 93
      },
      "path": "internal/kernel"
    },
    {
      "id": "69a7fc51f560dbe1420b63259d071558c5d5fff6e91321a8d3367f9181dfaf55",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "linear",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "client.go",
          "client_test.go",
          "host_darwin_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/linear"
    },
    {
      "id": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "maintainer",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 4,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 4,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "client.go",
          "client_test.go",
          "host.go",
          "host_darwin_test.go",
          "issues.go",
          "issues_test.go",
          "mcp.go",
          "mcp_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/maintainer"
    },
    {
      "id": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "provider",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 2,
          "tests": 3,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 2,
          "tests": 3,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "account_darwin_test.go",
          "provider.go",
          "provider_darwin_test.go",
          "tokens.go",
          "tokens_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/provider"
    },
    {
      "id": "a3009c88e694bf1e996454f50c8467bf95cd75c6c8464108a77917b4b5b9164a",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "relayhost",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 6,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 6,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "connector.go",
          "connector_test.go",
          "fake_loopback_test.go",
          "fake_relay_test.go",
          "identity.go",
          "identity_test.go",
          "queue.go",
          "queue_test.go",
          "record.go",
          "record_test.go",
          "session.go",
          "token.go",
          "token_test.go",
          "vectors_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/relayhost"
    },
    {
      "id": "8dbb48955f8b1188906abb995137bed72cdbaf226204bcf976bb848395a3bd87",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "review",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "coordinator.go",
          "coordinator_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/review"
    },
    {
      "id": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "runner",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 17,
          "tests": 18,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 17,
          "tests": 18,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "attempt_control_eof_darwin_test.go",
          "attempt_darwin.go",
          "attempt_darwin_test.go",
          "attempt_handover_darwin_test.go",
          "attempt_ready_darwin_test.go",
          "attempt_result.go",
          "attempt_result_test.go",
          "attempt_terminal_darwin_test.go",
          "attempt_types.go",
          "claude_task.go",
          "committed_launch_darwin_test.go",
          "environment_darwin_test.go",
          "group_signal_darwin_test.go",
          "launch_darwin.go",
          "private_darwin.go",
          "private_unsupported.go",
          "process_darwin.go",
          "process_terminate_race_darwin_test.go",
          "protocol.go",
          "protocol_test.go",
          "pty_darwin.go",
          "runner_darwin_test.go",
          "spool.go",
          "spool_darwin.go",
          "spool_test.go",
          "spool_unsupported.go",
          "terminal_loop_darwin.go",
          "terminal_loop_darwin_test.go",
          "terminal_stream.go",
          "terminal_stream_test.go",
          "types.go",
          "unsupported_test.go"
        ],
        "samples_omitted": 3
      },
      "path": "internal/runner"
    },
    {
      "id": "04213fed1558697c7d66e2d2894eff7bfae96629db3c0b06d2aa72326ee1cf14",
      "parent_id": "d41decd2b0e1342676ef17dd919e996a6dc4dbb776a38d04267be46797831490",
      "kind": "package",
      "label": "topology",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "special_file_unix_test.go",
          "topology.go",
          "topology_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "internal/topology"
    },
    {
      "id": "fc4ae44482be3267d75c122367aa07eac8eb612092cfb135a2baea94b98df2f7",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "directory",
      "label": "launchd",
      "language": "",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "README.md"
        ],
        "samples_omitted": 0
      },
      "path": "launchd"
    },
    {
      "id": "6f9d6e1aad785aaf1475e16c11fc761c4b0024fba399ab7fc0bbe87cf8475e68",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "directory",
      "label": "protocol",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 87,
          "assets": 0,
          "unclassified": 2
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "protocol"
    },
    {
      "id": "998bb9b0156e0287a60f3200e790a40b53fca38e2246853bd5a28eee65b591a0",
      "parent_id": "6f9d6e1aad785aaf1475e16c11fc761c4b0024fba399ab7fc0bbe87cf8475e68",
      "kind": "directory",
      "label": "browser",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 87,
          "assets": 0,
          "unclassified": 2
        },
        "samples": [
          "manifest.json"
        ],
        "samples_omitted": 0
      },
      "path": "protocol/browser"
    },
    {
      "id": "9aa26c7dbf0cc86c8de98966e568fd1d1603684543c6d677e8339a4fe04c5053",
      "parent_id": "998bb9b0156e0287a60f3200e790a40b53fca38e2246853bd5a28eee65b591a0",
      "kind": "directory",
      "label": "fixtures",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 86,
          "assets": 0,
          "unclassified": 2
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 86,
          "assets": 0,
          "unclassified": 2
        },
        "samples": [
          "account_link.json",
          "account_update.json",
          "accounts_discover.json",
          "agent_update.json",
          "attachment_retention_result.json",
          "browser_client_revoke.json",
          "browser_clients.json",
          "factory_dispatch.json",
          "github_connection_result.json",
          "human_request_cancel_run_result.json",
          "human_request_reply.json",
          "intake.json",
          "pair_result.json",
          "project_create.json",
          "project_limits_result.json",
          "remote_invite.json",
          "repositories.json",
          "repository_mutate_result.json",
          "state_changed.json",
          "state_watch.json",
          "task_detail.json",
          "task_enqueue.json",
          "task_history_get.json",
          "task_update.json",
          "terminal_attach.json",
          "terminal_detached.json",
          "terminal_exit.json",
          "terminal_lease_acquire.json",
          "terminal_lease_result.json",
          "terminal_resize.json",
          "terminal_target_get.json",
          "transcript.json"
        ],
        "samples_omitted": 56
      },
      "path": "protocol/browser/fixtures"
    },
    {
      "id": "8269689ebd2f2e8e2b2eb9ede3208e6205397dbb9e39bb85370e99b7e59541c3",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "package",
      "label": "dark-factory-relay-worker",
      "language": "typescript",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 1,
          "configuration": 3,
          "assets": 0,
          "unclassified": 1
        },
        "total": {
          "source": 7,
          "tests": 5,
          "documentation": 1,
          "configuration": 4,
          "assets": 0,
          "unclassified": 1
        },
        "samples": [
          "README.md",
          "package-lock.json",
          "package.json",
          "tsconfig.json",
          "wrangler.jsonc"
        ],
        "samples_omitted": 0
      },
      "path": "relay"
    },
    {
      "id": "580605aac8fb62b04100747578446329d011327bfe8ed4c350e5a102c5d394bb",
      "parent_id": "8269689ebd2f2e8e2b2eb9ede3208e6205397dbb9e39bb85370e99b7e59541c3",
      "kind": "directory",
      "label": "fixtures",
      "language": "",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "tokens.json"
        ],
        "samples_omitted": 0
      },
      "path": "relay/fixtures"
    },
    {
      "id": "c77b93d966af8277895ac78d58420979d73e8386f4cb875d1df9931a2b7f04b5",
      "parent_id": "8269689ebd2f2e8e2b2eb9ede3208e6205397dbb9e39bb85370e99b7e59541c3",
      "kind": "directory",
      "label": "scripts",
      "language": "",
      "size_bucket": "tiny",
      "inventory": {
        "direct": {
          "source": 2,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 2,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "local-ci.sh",
          "with-clean-wrangler-env.sh"
        ],
        "samples_omitted": 0
      },
      "path": "relay/scripts"
    },
    {
      "id": "8524748167e1ad8cae0f2ac1accf9a63fa731fc5e206700d7d8ac73287b0e4d3",
      "parent_id": "8269689ebd2f2e8e2b2eb9ede3208e6205397dbb9e39bb85370e99b7e59541c3",
      "kind": "directory",
      "label": "src",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 5,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 5,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "encoding.ts",
          "envelope.ts",
          "index.ts",
          "relay.ts",
          "tokens.ts"
        ],
        "samples_omitted": 0
      },
      "path": "relay/src"
    },
    {
      "id": "3503b0cace8513740f53da355c642e3ebd5769afdc518d995c84dd1d6ed71aef",
      "parent_id": "8269689ebd2f2e8e2b2eb9ede3208e6205397dbb9e39bb85370e99b7e59541c3",
      "kind": "directory",
      "label": "tests",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 5,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 5,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "helpers.mjs",
          "relay.integration.test.mjs",
          "relay.rate.test.mjs",
          "tokens.vectors.test.mjs",
          "ts-loader.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "relay/tests"
    },
    {
      "id": "109d93f388b1d4f8105afa074a0e1c0500955bcca4a25a7e926f734fba867297",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 78,
          "tests": 0,
          "documentation": 1,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 79,
          "tests": 0,
          "documentation": 1,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "bootstrap-maintainer-v2.sh",
          "check-toolchain-pins.sh",
          "dark-factory-browser-mcp.py",
          "deploy-site.sh",
          "factory-intake.example.json",
          "factory-production-reviews.py",
          "factory-release.py",
          "gh-comment.sh",
          "go-check.sh",
          "go-e2e-tools.sh",
          "go-service-e2e.sh",
          "legacy-workflow-provider.go",
          "local-ci-lease.sh",
          "package-release.sh",
          "publication-parents.sh",
          "render-homebrew-formula.sh",
          "test-bootstrap-maintainer-v2.sh",
          "test-cold-review.sh",
          "test-factory-autonomy.py",
          "test-factory-delivery.py",
          "test-factory-production-reviews.py",
          "test-factory-review-intake.py",
          "test-go-e2e-tools.sh",
          "test-legacy-workflow-e2e.py",
          "test-local-ci-lease.sh",
          "test-package-release.sh",
          "test-publish-release.sh",
          "test-repository-settings.sh",
          "test-verify-adversarial-review.sh",
          "verification-profile.mjs",
          "verify-live-runtime.py",
          "with-local-ci-lease.sh"
        ],
        "samples_omitted": 48
      },
      "path": "scripts"
    },
    {
      "id": "098e17efe3043d9b9330cf16a43d6d7ddea54a9e56e78342f19a1bf7c5ef8ce7",
      "parent_id": "109d93f388b1d4f8105afa074a0e1c0500955bcca4a25a7e926f734fba867297",
      "kind": "directory",
      "label": "test-fixtures",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 1,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "fake-release-gh.sh"
        ],
        "samples_omitted": 0
      },
      "path": "scripts/test-fixtures"
    },
    {
      "id": "b1e66dcd6bd080a9ddb7f697b00f6158ac4fb04176b2ff09d0e60c6052448d94",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "directory",
      "label": "spikes",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 1,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "spikes"
    },
    {
      "id": "1354c303853731a5c111203f436ff689819187c15b5527b5c2f5095df5408d21",
      "parent_id": "b1e66dcd6bd080a9ddb7f697b00f6158ac4fb04176b2ff09d0e60c6052448d94",
      "kind": "package",
      "label": "main",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 3,
          "tests": 1,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 1,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "README.md",
          "main.go",
          "probe.html",
          "server.go",
          "server_test.go"
        ],
        "samples_omitted": 0
      },
      "path": "spikes/browser-connectivity"
    },
    {
      "id": "b6ab767f1b02f9d1edee9b53bd0d418bdd183ba9e280f49c919fff3bec41fce5",
      "parent_id": "0d9d67a17a7019f8cec08852b20306c697b11f6cc6e448110bdd3d1804d6b524",
      "kind": "package",
      "label": "dark-factory-web",
      "language": "typescript",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 7,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 58,
          "tests": 36,
          "documentation": 0,
          "configuration": 12,
          "assets": 1,
          "unclassified": 1
        },
        "samples": [
          ".gitignore",
          "package.json",
          "pnpm-lock.yaml",
          "pnpm-workspace.yaml",
          "toolchain-integrity.json",
          "tsconfig.build.json",
          "tsconfig.json"
        ],
        "samples_omitted": 0
      },
      "path": "web"
    },
    {
      "id": "6910d445f459675a7cef3d29329526e48754f7a9e8ea4027d802e669622caf5a",
      "parent_id": "b6ab767f1b02f9d1edee9b53bd0d418bdd183ba9e280f49c919fff3bec41fce5",
      "kind": "directory",
      "label": "apps",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 4,
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "web/apps"
    },
    {
      "id": "c64a1d145cb4451a7b54006acd0ce804420b3a02b779a774c485fb86dfa2b084",
      "parent_id": "6910d445f459675a7cef3d29329526e48754f7a9e8ea4027d802e669622caf5a",
      "kind": "package",
      "label": "dark-factory-dev",
      "language": "typescript",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 4,
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "index.html",
          "package.json",
          "tsconfig.json"
        ],
        "samples_omitted": 0
      },
      "path": "web/apps/dev"
    },
    {
      "id": "8bc37bea0b03ac8929add40480285c5c020478d77da3df8cd94383273e83cda6",
      "parent_id": "c64a1d145cb4451a7b54006acd0ce804420b3a02b779a774c485fb86dfa2b084",
      "kind": "directory",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 3,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "actual-topology.ts",
          "main.tsx",
          "styles.css"
        ],
        "samples_omitted": 0
      },
      "path": "web/apps/dev/src"
    },
    {
      "id": "1e70d7b6c0e45ed2f75b6e49c984fa58c92bb9349772a4ca7c0e62bd11be9b45",
      "parent_id": "b6ab767f1b02f9d1edee9b53bd0d418bdd183ba9e280f49c919fff3bec41fce5",
      "kind": "directory",
      "label": "fixtures",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 2,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 2,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "state.d.mts",
          "state.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "web/fixtures"
    },
    {
      "id": "cac87f9246f4ad251fa24e54c900d0f2520f6726c474d2fdb9da30179b043b5e",
      "parent_id": "b6ab767f1b02f9d1edee9b53bd0d418bdd183ba9e280f49c919fff3bec41fce5",
      "kind": "directory",
      "label": "packages",
      "language": "",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 51,
          "tests": 35,
          "documentation": 0,
          "configuration": 3,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      },
      "path": "web/packages"
    },
    {
      "id": "c0017507c637d77cc344e6951260afe09f798cd6e02dfc94389c8c7cd8beb9c1",
      "parent_id": "cac87f9246f4ad251fa24e54c900d0f2520f6726c474d2fdb9da30179b043b5e",
      "kind": "package",
      "label": "@dark-factory/client",
      "language": "typescript",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 16,
          "tests": 11,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "package.json"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/client"
    },
    {
      "id": "fabf7075f37f48a344f07896fc61dea819962c8968221cf51ecbc75ecf9af877",
      "parent_id": "c0017507c637d77cc344e6951260afe09f798cd6e02dfc94389c8c7cd8beb9c1",
      "kind": "directory",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 10,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 16,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "control.ts",
          "errors.ts",
          "index.ts",
          "manifest.ts",
          "project-content.ts",
          "session.ts",
          "state.ts",
          "terminal.ts",
          "terminal_session.ts",
          "transcript.ts"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/client/src"
    },
    {
      "id": "c72b98d93c8527aa6b8d021a04af9be7db6521b52f8092a414e27b37ddaafc7a",
      "parent_id": "fabf7075f37f48a344f07896fc61dea819962c8968221cf51ecbc75ecf9af877",
      "kind": "directory",
      "label": "remote",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 6,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 6,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "index.ts",
          "invitation.ts",
          "manager.ts",
          "relay-socket.ts",
          "store.ts",
          "tokens.ts"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/client/src/remote"
    },
    {
      "id": "149eb688a2dc629b20d06ad10834918c7a20c36c4bf5c8598f2b459265344b7c",
      "parent_id": "c0017507c637d77cc344e6951260afe09f798cd6e02dfc94389c8c7cd8beb9c1",
      "kind": "directory",
      "label": "test",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 10,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 11,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "client.test.mjs",
          "packed-consumer.test.mjs",
          "remote-fake.mjs",
          "remote-invitation.test.mjs",
          "remote-manager.test.mjs",
          "remote-socket.test.mjs",
          "remote-tokens.test.mjs",
          "session.test.mjs",
          "state.test.mjs",
          "terminal-lifecycle.test.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/client/test"
    },
    {
      "id": "7ad1e103bffe3323136c12d63b6293b2c8f9cbd793a5159095d8956b2cf69bec",
      "parent_id": "149eb688a2dc629b20d06ad10834918c7a20c36c4bf5c8598f2b459265344b7c",
      "kind": "directory",
      "label": "e2e",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "go-browser-pty.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/client/test/e2e"
    },
    {
      "id": "f1807e79f4a6d3f45a09bb18d4bfac64b33ada4360aac3c077f2554dbd11a0c1",
      "parent_id": "cac87f9246f4ad251fa24e54c900d0f2520f6726c474d2fdb9da30179b043b5e",
      "kind": "package",
      "label": "@dark-factory/ui",
      "language": "typescript",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 35,
          "tests": 24,
          "documentation": 0,
          "configuration": 2,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [
          "package.json",
          "tsconfig.json"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui"
    },
    {
      "id": "fce7fdb9cc67d069487e101dda541589602c5f1994fd91bc656d2222e4e3b8be",
      "parent_id": "f1807e79f4a6d3f45a09bb18d4bfac64b33ada4360aac3c077f2554dbd11a0c1",
      "kind": "directory",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 23,
          "tests": 5,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 35,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [
          "console-interactions.tsx",
          "console-screens.tsx",
          "console-sidebar.tsx",
          "console-view.ts",
          "contraption.test.mjs",
          "contraption.tsx",
          "factory-app-controller.ts",
          "factory-app.tsx",
          "factory-console.css",
          "factory-console.tsx",
          "factory-settings-coordinator.ts",
          "floor-appearance.ts",
          "human-request-flow.ts",
          "index.ts",
          "missions-panel.tsx",
          "production-area.test.mjs",
          "production-area.tsx",
          "production-data.test.mjs",
          "production-data.ts",
          "production-panel.test.mjs",
          "production-panel.tsx",
          "production-view.test.mjs",
          "production-view.ts",
          "project-library.tsx",
          "project-outcomes.tsx",
          "remote-invite.tsx",
          "terminal-controller.ts",
          "xterm-terminal.tsx"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui/src"
    },
    {
      "id": "ad054bf0def76e7fc038b653667a2a327e94e8bde5917e9bbaac5105cf1f1372",
      "parent_id": "fce7fdb9cc67d069487e101dda541589602c5f1994fd91bc656d2222e4e3b8be",
      "kind": "directory",
      "label": "factory-scene",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 7,
          "tests": 3,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 10,
          "tests": 3,
          "documentation": 0,
          "configuration": 0,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [
          "appearance.ts",
          "factory-scene.test.mjs",
          "factory-scene.tsx",
          "idle-life.test.mjs",
          "idle-life.ts",
          "messages.test.mjs",
          "messages.ts",
          "movement.ts",
          "scene.ts",
          "sprite-editor.tsx"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui/src/factory-scene"
    },
    {
      "id": "3f335f26e1aa99db565fb70b765109a6c30e305923db478e646ce62a12db3701",
      "parent_id": "ad054bf0def76e7fc038b653667a2a327e94e8bde5917e9bbaac5105cf1f1372",
      "kind": "directory",
      "label": "sprites",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 3,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 1,
          "unclassified": 0
        },
        "total": {
          "source": 3,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [
          "gen-sprites.mjs",
          "preview.html",
          "sprites.generated.ts",
          "sprites.png"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui/src/factory-scene/sprites"
    },
    {
      "id": "f628f3c1c90242c3e63de9dac8ba2cd08ae2ad5a2dae2000ff3a52d6d51b9a2e",
      "parent_id": "fce7fdb9cc67d069487e101dda541589602c5f1994fd91bc656d2222e4e3b8be",
      "kind": "directory",
      "label": "remote",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 2,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 2,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "remote-app.tsx",
          "remote-view.ts"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui/src/remote"
    },
    {
      "id": "c6b410122cb349a92615e02f3a3dfc1b4fc7564ff804d1293a11d7e8db4ed017",
      "parent_id": "f1807e79f4a6d3f45a09bb18d4bfac64b33ada4360aac3c077f2554dbd11a0c1",
      "kind": "directory",
      "label": "test",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 14,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 16,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "artifact-inventory.test.mjs",
          "console-view.test.mjs",
          "factory-app-controller.test.mjs",
          "factory-app-lifecycle.test.mjs",
          "factory-app-sidebar.test.mjs",
          "factory-app-terminal.test.mjs",
          "factory-console.test.mjs",
          "factory-settings-coordinator.test.mjs",
          "missions-panel.test.mjs",
          "packed-consumer.test.mjs",
          "project-library.test.mjs",
          "remote-app.test.mjs",
          "terminal-controller.test.mjs",
          "xterm-terminal.test.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui/test"
    },
    {
      "id": "7474071c643e380ec882144f90a60c5d517091d2db7e1cfa2b6a9634ef533072",
      "parent_id": "c6b410122cb349a92615e02f3a3dfc1b4fc7564ff804d1293a11d7e8db4ed017",
      "kind": "directory",
      "label": "fixtures",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 2,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "strict-composition-loader.mjs",
          "strict-composition-probe.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "web/packages/ui/test/fixtures"
    },
    {
      "id": "93b61df729ecd5f7f637f2aa1d5a67198cad95cc54c205b844bdd39dc7a8c4c2",
      "parent_id": "b6ab767f1b02f9d1edee9b53bd0d418bdd183ba9e280f49c919fff3bec41fce5",
      "kind": "directory",
      "label": "scripts",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 1
        },
        "total": {
          "source": 1,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 1
        },
        "samples": [
          "package-artifacts",
          "package-artifacts.mjs",
          "package-artifacts.test.mjs"
        ],
        "samples_omitted": 0
      },
      "path": "web/scripts"
    }
  ],
  "projectId": "11111111111111111111111111111111",
  "sourceRevision": "6eb17496a343edb8e0d27c45aa49d77aedd97510",
  "dependencies": {
    "source": "go-imports-package-manifests",
    "edges": [
      {
        "from": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 3
      },
      {
        "from": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 2
      },
      {
        "from": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
        "to": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "weight": 20
      },
      {
        "from": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
        "to": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
        "weight": 1
      },
      {
        "from": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 2
      },
      {
        "from": "10a9b6ff87d9119155c61a90cbdb91f9b58d8d01a7d1cc5869e4a146b7dea89b",
        "to": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
        "weight": 2
      },
      {
        "from": "13489b7aaf406663d61f7c9173c9bad8ecff9452f04a7f3034c9a52d7e9deaa5",
        "to": "f0ac6681df86312628dad4347e2cc661650009cee21302173ed6d9971116f656",
        "weight": 1
      },
      {
        "from": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 1
      },
      {
        "from": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 7
      },
      {
        "from": "367ed2395b35ed9419eab08b5d5dc797c79e79a915e182b3b6e4a2c61d4cfb39",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 1
      },
      {
        "from": "367ed2395b35ed9419eab08b5d5dc797c79e79a915e182b3b6e4a2c61d4cfb39",
        "to": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "weight": 1
      },
      {
        "from": "367ed2395b35ed9419eab08b5d5dc797c79e79a915e182b3b6e4a2c61d4cfb39",
        "to": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
        "weight": 1
      },
      {
        "from": "36ce3b617704d288a88bf3b9e867069db35f2830bf511ae72cd78be85055c579",
        "to": "7eb5d689a166500d297916d0ec8645c9b23c6e14b0687eecdcfd13997f98532f",
        "weight": 3
      },
      {
        "from": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 4
      },
      {
        "from": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "to": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
        "weight": 1
      },
      {
        "from": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 5
      },
      {
        "from": "4d64296b0b7ce3e3198a84ceaecadce3f7ff2578bd8e43f41c8952f309aefe81",
        "to": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
        "weight": 1
      },
      {
        "from": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 2
      },
      {
        "from": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 2
      },
      {
        "from": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "to": "7eb5d689a166500d297916d0ec8645c9b23c6e14b0687eecdcfd13997f98532f",
        "weight": 2
      },
      {
        "from": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 5
      },
      {
        "from": "69a7fc51f560dbe1420b63259d071558c5d5fff6e91321a8d3367f9181dfaf55",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 2
      },
      {
        "from": "69a7fc51f560dbe1420b63259d071558c5d5fff6e91321a8d3367f9181dfaf55",
        "to": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
        "weight": 1
      },
      {
        "from": "69a7fc51f560dbe1420b63259d071558c5d5fff6e91321a8d3367f9181dfaf55",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 1
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 4
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 2
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "36ce3b617704d288a88bf3b9e867069db35f2830bf511ae72cd78be85055c579",
        "weight": 2
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "weight": 3
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 2
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "weight": 1
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "weight": 1
      },
      {
        "from": "9ca4f44520cff3f968440fa0464ad7caf550a00d9648b59024caaacfae75e47f",
        "to": "d73a555d38f2dc324eda4f4ce026fad8b7ca053114075c2c86cb7ae5dcc5f884",
        "weight": 1
      },
      {
        "from": "a3009c88e694bf1e996454f50c8467bf95cd75c6c8464108a77917b4b5b9164a",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 2
      },
      {
        "from": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 2
      },
      {
        "from": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
        "to": "7eb5d689a166500d297916d0ec8645c9b23c6e14b0687eecdcfd13997f98532f",
        "weight": 4
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 2
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 1
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "36ce3b617704d288a88bf3b9e867069db35f2830bf511ae72cd78be85055c579",
        "weight": 2
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "weight": 2
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "weight": 1
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "a3009c88e694bf1e996454f50c8467bf95cd75c6c8464108a77917b4b5b9164a",
        "weight": 1
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 2
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "weight": 2
      },
      {
        "from": "afc862dabbd00f142e3134296b8ad30adf3af9ace0ec10a4999956d39e89424d",
        "to": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
        "weight": 1
      },
      {
        "from": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 5
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "04213fed1558697c7d66e2d2894eff7bfae96629db3c0b06d2aa72326ee1cf14",
        "weight": 3
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 14
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 32
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "36ce3b617704d288a88bf3b9e867069db35f2830bf511ae72cd78be85055c579",
        "weight": 19
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "397ca60d33b901d5791e4cfa422b04685a363468cf777264819813492d74331f",
        "weight": 47
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "weight": 9
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "69a7fc51f560dbe1420b63259d071558c5d5fff6e91321a8d3367f9181dfaf55",
        "weight": 3
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "7eb5d689a166500d297916d0ec8645c9b23c6e14b0687eecdcfd13997f98532f",
        "weight": 1
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "8dbb48955f8b1188906abb995137bed72cdbaf226204bcf976bb848395a3bd87",
        "weight": 9
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "a3009c88e694bf1e996454f50c8467bf95cd75c6c8464108a77917b4b5b9164a",
        "weight": 5
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "a95b04fb097ab2d5a50df6795aed932b0eb3ca4db1908510ea272f64aa6bba8b",
        "weight": 13
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 122
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "weight": 6
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "cc6402ba32ae6fb4621afd3a40e5291ed238d1ce44800e3676fc87b2d0108bdb",
        "weight": 1
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "d73a555d38f2dc324eda4f4ce026fad8b7ca053114075c2c86cb7ae5dcc5f884",
        "weight": 26
      },
      {
        "from": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "to": "ddf439b2340c50af3038277dcbf5caf0c912774280b07e900dae261d170a9e69",
        "weight": 32
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "27d18925cc4fe843a7d307bf51d440eaedb01b2b54d37d75e1c495f4126ea7c4",
        "weight": 3
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "2f8ba8c0f29865031049b6c979b0e325f3b05a83a6774dfdf419bdcc413b5187",
        "weight": 5
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "36ce3b617704d288a88bf3b9e867069db35f2830bf511ae72cd78be85055c579",
        "weight": 4
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "524a905d534ef4eb11264387b6159645110166f812bff650879c2b1f5068e1bd",
        "weight": 3
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "7eb5d689a166500d297916d0ec8645c9b23c6e14b0687eecdcfd13997f98532f",
        "weight": 2
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "b2489b467464780c20f49e5ba5b643cf13bbfab647ef07936bf4ea8b3327a616",
        "weight": 5
      },
      {
        "from": "c36e32d3640922360942df1b3c4268baedfd344b2e56e0a5fdae18bdbe18f346",
        "to": "bd133bc67b97e8023999f677d6ce84e24b9a5ac2f159612faa4ad8a1c5bbb402",
        "weight": 1
      },
      {
        "from": "c64a1d145cb4451a7b54006acd0ce804420b3a02b779a774c485fb86dfa2b084",
        "to": "c0017507c637d77cc344e6951260afe09f798cd6e02dfc94389c8c7cd8beb9c1",
        "weight": 1
      },
      {
        "from": "c64a1d145cb4451a7b54006acd0ce804420b3a02b779a774c485fb86dfa2b084",
        "to": "f1807e79f4a6d3f45a09bb18d4bfac64b33ada4360aac3c077f2554dbd11a0c1",
        "weight": 1
      },
      {
        "from": "d73a555d38f2dc324eda4f4ce026fad8b7ca053114075c2c86cb7ae5dcc5f884",
        "to": "ddf439b2340c50af3038277dcbf5caf0c912774280b07e900dae261d170a9e69",
        "weight": 10
      },
      {
        "from": "f1807e79f4a6d3f45a09bb18d4bfac64b33ada4360aac3c077f2554dbd11a0c1",
        "to": "c0017507c637d77cc344e6951260afe09f798cd6e02dfc94389c8c7cd8beb9c1",
        "weight": 1
      }
    ],
    "omitted": 0
  }
};
export default topology;

// Exact eligible file inventory from the same archive; no file bodies are included.
export const actualFiles = [
  {"path":".gitignore","kind":"configuration","bytes":351},
  {"path":"AGENTS.md","kind":"documentation","bytes":6283},
  {"path":"ARCHITECTURE.md","kind":"documentation","bytes":24527},
  {"path":"CLAUDE.md","kind":"documentation","bytes":76},
  {"path":"CONTRIBUTING.md","kind":"documentation","bytes":3063},
  {"path":"LICENSE","kind":"documentation","bytes":1078},
  {"path":"README.md","kind":"documentation","bytes":7323},
  {"path":"ROADMAP.md","kind":"documentation","bytes":1233},
  {"path":"SECURITY.md","kind":"documentation","bytes":15710},
  {"path":"VERSION","kind":"unclassified","bytes":6},
  {"path":"cmd/cloudflare-admin/main.go","kind":"source","bytes":844},
  {"path":"cmd/cloudflare-admin/main_test.go","kind":"tests","bytes":288},
  {"path":"cmd/factory-runner/main.go","kind":"source","bytes":1381},
  {"path":"cmd/factory-runner/main_test.go","kind":"tests","bytes":1686},
  {"path":"cmd/factoryctl/agent_paths_test.go","kind":"tests","bytes":454},
  {"path":"cmd/factoryctl/browser_darwin.go","kind":"source","bytes":183},
  {"path":"cmd/factoryctl/browser_other.go","kind":"source","bytes":189},
  {"path":"cmd/factoryctl/community.go","kind":"source","bytes":4487},
  {"path":"cmd/factoryctl/community_test.go","kind":"tests","bytes":5553},
  {"path":"cmd/factoryctl/content_parse_test.go","kind":"tests","bytes":3243},
  {"path":"cmd/factoryctl/github.go","kind":"source","bytes":8111},
  {"path":"cmd/factoryctl/github_test.go","kind":"tests","bytes":6170},
  {"path":"cmd/factoryctl/human_options_test.go","kind":"tests","bytes":938},
  {"path":"cmd/factoryctl/intake_legacy.go","kind":"source","bytes":1279},
  {"path":"cmd/factoryctl/intake_legacy_test.go","kind":"tests","bytes":741},
  {"path":"cmd/factoryctl/intake_review.go","kind":"source","bytes":6301},
  {"path":"cmd/factoryctl/intake_review_test.go","kind":"tests","bytes":3931},
  {"path":"cmd/factoryctl/intake_service.go","kind":"source","bytes":6116},
  {"path":"cmd/factoryctl/intake_service_test.go","kind":"tests","bytes":6495},
  {"path":"cmd/factoryctl/intake_test.go","kind":"tests","bytes":4454},
  {"path":"cmd/factoryctl/local_ci_identity_darwin_test.go","kind":"tests","bytes":2378},
  {"path":"cmd/factoryctl/main.go","kind":"source","bytes":120419},
  {"path":"cmd/factoryctl/main_test.go","kind":"tests","bytes":40656},
  {"path":"cmd/factoryctl/maintainer.go","kind":"source","bytes":2081},
  {"path":"cmd/factoryctl/maintainer_test.go","kind":"tests","bytes":1436},
  {"path":"cmd/factoryctl/mcp.go","kind":"source","bytes":4874},
  {"path":"cmd/factoryctl/mcp_test.go","kind":"tests","bytes":16166},
  {"path":"cmd/factoryctl/operator_test.go","kind":"tests","bytes":35292},
  {"path":"cmd/factoryctl/outcomes.go","kind":"source","bytes":4055},
  {"path":"cmd/factoryctl/outcomes_test.go","kind":"tests","bytes":882},
  {"path":"cmd/factoryctl/production.go","kind":"source","bytes":1350},
  {"path":"cmd/factoryctl/production_test.go","kind":"tests","bytes":1096},
  {"path":"cmd/factoryctl/remote.go","kind":"source","bytes":1657},
  {"path":"cmd/factoryctl/remote_test.go","kind":"tests","bytes":3473},
  {"path":"cmd/factoryctl/repository_readiness_test.go","kind":"tests","bytes":1350},
  {"path":"cmd/factoryctl/review.go","kind":"source","bytes":1956},
  {"path":"cmd/factoryctl/review_test.go","kind":"tests","bytes":1325},
  {"path":"cmd/factoryctl/service_test.go","kind":"tests","bytes":14710},
  {"path":"cmd/factoryctl/web_test.go","kind":"tests","bytes":12139},
  {"path":"cmd/factoryd/main.go","kind":"source","bytes":24894},
  {"path":"cmd/factoryd/main_test.go","kind":"tests","bytes":35423},
  {"path":"control-plane/.gitignore","kind":"configuration","bytes":124},
  {"path":"control-plane/Cargo.lock","kind":"configuration","bytes":30438},
  {"path":"control-plane/Cargo.toml","kind":"configuration","bytes":1310},
  {"path":"control-plane/README.md","kind":"documentation","bytes":27502},
  {"path":"control-plane/migrations/0001_maintainer_deliveries.sql","kind":"source","bytes":1489},
  {"path":"control-plane/migrations/0004_maintainer_operations.sql","kind":"source","bytes":1651},
  {"path":"control-plane/migrations/0005_operation_authorities.sql","kind":"source","bytes":425},
  {"path":"control-plane/package-lock.json","kind":"configuration","bytes":49563},
  {"path":"control-plane/package.json","kind":"configuration","bytes":358},
  {"path":"control-plane/scripts/build-worker.sh","kind":"source","bytes":395},
  {"path":"control-plane/scripts/local-ci.sh","kind":"source","bytes":2854},
  {"path":"control-plane/scripts/test-clean-wrangler-env.sh","kind":"source","bytes":3519},
  {"path":"control-plane/scripts/with-clean-wrangler-env.sh","kind":"source","bytes":1739},
  {"path":"control-plane/src/access.rs","kind":"source","bytes":30009},
  {"path":"control-plane/src/connection.rs","kind":"source","bytes":43614},
  {"path":"control-plane/src/github_app.rs","kind":"source","bytes":453763},
  {"path":"control-plane/src/journal.rs","kind":"source","bytes":91048},
  {"path":"control-plane/src/lib.rs","kind":"source","bytes":16614},
  {"path":"control-plane/src/maintainer.rs","kind":"source","bytes":11461},
  {"path":"control-plane/src/mcp.rs","kind":"source","bytes":83608},
  {"path":"control-plane/tests/cloudflare_boundary.rs","kind":"tests","bytes":32976},
  {"path":"control-plane/tests/connections.integration.test.mjs","kind":"tests","bytes":41841},
  {"path":"control-plane/tests/fixtures/repository-without-administration.json","kind":"configuration","bytes":7632},
  {"path":"control-plane/tests/inactive_default.rs","kind":"tests","bytes":1135},
  {"path":"control-plane/tests/maintainer_webhook.rs","kind":"tests","bytes":15329},
  {"path":"control-plane/tests/worker.integration.test.mjs","kind":"tests","bytes":7863},
  {"path":"control-plane/wrangler.toml","kind":"configuration","bytes":1057},
  {"path":"docs/assets/factory-floor-demo-mobile.png","kind":"assets","bytes":49975},
  {"path":"docs/assets/factory-floor-demo.png","kind":"assets","bytes":145636},
  {"path":"docs/canary/issue-origin-2.txt","kind":"unclassified","bytes":103},
  {"path":"docs/canary/issue-origin.txt","kind":"unclassified","bytes":90},
  {"path":"docs/canary/operator-origin-3.txt","kind":"unclassified","bytes":76},
  {"path":"docs/canary/operator-origin-4.txt","kind":"unclassified","bytes":74},
  {"path":"docs/development/ARCHITECTURE_REVIEW.md","kind":"documentation","bytes":5498},
  {"path":"docs/development/ATTEMPT_RESULT_RECOVERY.md","kind":"documentation","bytes":11972},
  {"path":"docs/development/BROWSER_BOUNDARY_SIMPLIFICATION_HANDOVER.md","kind":"documentation","bytes":27811},
  {"path":"docs/development/DEPLOY.md","kind":"documentation","bytes":8986},
  {"path":"docs/development/FACTORY_FLOOR.md","kind":"documentation","bytes":19234},
  {"path":"docs/development/GITHUB_APP.md","kind":"documentation","bytes":31931},
  {"path":"docs/development/GITHUB_CONNECTIONS.md","kind":"documentation","bytes":9696},
  {"path":"docs/development/GO_REWRITE.md","kind":"documentation","bytes":877},
  {"path":"docs/development/ONBOARDING_SETTINGS_REFINEMENT_HANDOFF.md","kind":"documentation","bytes":2404},
  {"path":"docs/development/OPTIONAL_KNOWLEDGE.md","kind":"documentation","bytes":6802},
  {"path":"docs/development/OVERSEER.md","kind":"documentation","bytes":44432},
  {"path":"docs/development/OVERSEER_SNAPSHOT_REDUCTION.md","kind":"documentation","bytes":4416},
  {"path":"docs/development/PIXEL_WORLD.md","kind":"documentation","bytes":1350},
  {"path":"docs/development/PRODUCTISATION.md","kind":"documentation","bytes":5479},
  {"path":"docs/development/PROJECT_CONTENT_HANDOFF.md","kind":"documentation","bytes":3976},
  {"path":"docs/development/SCRIPTS-AUDIT.md","kind":"documentation","bytes":6221},
  {"path":"docs/development/UNATTENDED.md","kind":"documentation","bytes":16020},
  {"path":"docs/development/WORKFLOW.md","kind":"documentation","bytes":20125},
  {"path":"docs/development/YIELDED_CONTINUATION.md","kind":"documentation","bytes":3300},
  {"path":"docs/install.md","kind":"documentation","bytes":26906},
  {"path":"docs/providers.md","kind":"documentation","bytes":26211},
  {"path":"go.mod","kind":"configuration","bytes":317},
  {"path":"go.sum","kind":"configuration","bytes":1173},
  {"path":"internal/api/attempt_source_test.go","kind":"tests","bytes":2703},
  {"path":"internal/api/attempt_task_test.go","kind":"tests","bytes":1640},
  {"path":"internal/api/client.go","kind":"source","bytes":51225},
  {"path":"internal/api/client_test.go","kind":"tests","bytes":66464},
  {"path":"internal/api/content_client.go","kind":"source","bytes":4274},
  {"path":"internal/api/github.go","kind":"source","bytes":3079},
  {"path":"internal/api/github_test.go","kind":"tests","bytes":1359},
  {"path":"internal/api/intake.go","kind":"source","bytes":9750},
  {"path":"internal/api/intake_budget_test.go","kind":"tests","bytes":1596},
  {"path":"internal/api/intake_legacy.go","kind":"source","bytes":3920},
  {"path":"internal/api/intake_legacy_test.go","kind":"tests","bytes":1488},
  {"path":"internal/api/intake_review.go","kind":"source","bytes":3744},
  {"path":"internal/api/intake_review_test.go","kind":"tests","bytes":2029},
  {"path":"internal/api/intake_test.go","kind":"tests","bytes":3279},
  {"path":"internal/api/maintainer.go","kind":"source","bytes":3063},
  {"path":"internal/api/maintainer_test.go","kind":"tests","bytes":2156},
  {"path":"internal/api/outcome_client.go","kind":"source","bytes":1768},
  {"path":"internal/api/overseer_control_test.go","kind":"tests","bytes":11098},
  {"path":"internal/api/path_ancestor_darwin.go","kind":"source","bytes":168},
  {"path":"internal/api/path_ancestor_other.go","kind":"source","bytes":104},
  {"path":"internal/api/path_race_test.go","kind":"tests","bytes":9161},
  {"path":"internal/api/path_unix.go","kind":"source","bytes":10520},
  {"path":"internal/api/peer_darwin.go","kind":"source","bytes":1206},
  {"path":"internal/api/peer_darwin_test.go","kind":"tests","bytes":245},
  {"path":"internal/api/peer_linux.go","kind":"source","bytes":611},
  {"path":"internal/api/peer_status_test.go","kind":"tests","bytes":2040},
  {"path":"internal/api/production.go","kind":"source","bytes":513},
  {"path":"internal/api/production_test.go","kind":"tests","bytes":2482},
  {"path":"internal/api/remote_contract_test.go","kind":"tests","bytes":5700},
  {"path":"internal/api/server.go","kind":"source","bytes":57131},
  {"path":"internal/api/server_test.go","kind":"tests","bytes":59854},
  {"path":"internal/api/snapshot_reply_test.go","kind":"tests","bytes":1902},
  {"path":"internal/api/task_recovery_test.go","kind":"tests","bytes":7103},
  {"path":"internal/api/types.go","kind":"source","bytes":53838},
  {"path":"internal/api/web_contract_test.go","kind":"tests","bytes":1893},
  {"path":"internal/browser/agent_control_test.go","kind":"tests","bytes":2982},
  {"path":"internal/browser/backend.go","kind":"source","bytes":13002},
  {"path":"internal/browser/connection.go","kind":"source","bytes":51879},
  {"path":"internal/browser/connection_identity_test.go","kind":"tests","bytes":8013},
  {"path":"internal/browser/console_control_test.go","kind":"tests","bytes":28123},
  {"path":"internal/browser/pair_test.go","kind":"tests","bytes":9825},
  {"path":"internal/browser/review_repairs_test.go","kind":"tests","bytes":18468},
  {"path":"internal/browser/server.go","kind":"source","bytes":22541},
  {"path":"internal/browser/server_test.go","kind":"tests","bytes":44197},
  {"path":"internal/browser/task_enqueue_test.go","kind":"tests","bytes":7846},
  {"path":"internal/browser/terminal_transport_test.go","kind":"tests","bytes":63065},
  {"path":"internal/browserprotocol/agent_control.go","kind":"source","bytes":9161},
  {"path":"internal/browserprotocol/binary.go","kind":"source","bytes":4246},
  {"path":"internal/browserprotocol/console_control.go","kind":"source","bytes":41382},
  {"path":"internal/browserprotocol/console_control_test.go","kind":"tests","bytes":13285},
  {"path":"internal/browserprotocol/github_control.go","kind":"source","bytes":8060},
  {"path":"internal/browserprotocol/github_control_test.go","kind":"tests","bytes":3304},
  {"path":"internal/browserprotocol/human_options_test.go","kind":"tests","bytes":786},
  {"path":"internal/browserprotocol/project_content.go","kind":"source","bytes":2165},
  {"path":"internal/browserprotocol/project_content_test.go","kind":"tests","bytes":2432},
  {"path":"internal/browserprotocol/remote_control.go","kind":"source","bytes":4909},
  {"path":"internal/browserprotocol/remote_control_test.go","kind":"tests","bytes":1901},
  {"path":"internal/browserprotocol/state.go","kind":"source","bytes":21176},
  {"path":"internal/browserprotocol/state_test.go","kind":"tests","bytes":36692},
  {"path":"internal/browserprotocol/task_control.go","kind":"source","bytes":3998},
  {"path":"internal/browserprotocol/task_control_test.go","kind":"tests","bytes":7883},
  {"path":"internal/browserprotocol/terminal_control.go","kind":"source","bytes":12586},
  {"path":"internal/browserprotocol/transcript.go","kind":"source","bytes":5101},
  {"path":"internal/browserprotocol/transcript_test.go","kind":"tests","bytes":13470},
  {"path":"internal/browserprotocol/wire.go","kind":"source","bytes":44097},
  {"path":"internal/browserprotocol/wire_test.go","kind":"tests","bytes":62803},
  {"path":"internal/buildinfo/artifact.go","kind":"source","bytes":7418},
  {"path":"internal/buildinfo/artifact_test.go","kind":"tests","bytes":5531},
  {"path":"internal/buildinfo/buildinfo.go","kind":"source","bytes":5010},
  {"path":"internal/buildinfo/buildinfo_test.go","kind":"tests","bytes":3161},
  {"path":"internal/buildinfo/cmd/release-artifact/main.go","kind":"source","bytes":2215},
  {"path":"internal/change/content_darwin.go","kind":"source","bytes":13010},
  {"path":"internal/change/errors.go","kind":"source","bytes":718},
  {"path":"internal/change/git.go","kind":"source","bytes":9971},
  {"path":"internal/change/git_darwin.go","kind":"source","bytes":73108},
  {"path":"internal/change/git_darwin_test.go","kind":"tests","bytes":42350},
  {"path":"internal/change/git_external_darwin_test.go","kind":"tests","bytes":10490},
  {"path":"internal/change/git_fake_darwin_test.go","kind":"tests","bytes":974},
  {"path":"internal/change/git_protocol_darwin_test.go","kind":"tests","bytes":19048},
  {"path":"internal/change/git_unsupported.go","kind":"source","bytes":1696},
  {"path":"internal/change/repository_source.go","kind":"source","bytes":1998},
  {"path":"internal/change/repository_source_darwin.go","kind":"source","bytes":6504},
  {"path":"internal/change/repository_source_darwin_test.go","kind":"tests","bytes":4569},
  {"path":"internal/change/repository_source_unsupported.go","kind":"source","bytes":635},
  {"path":"internal/change/worktree_darwin_test.go","kind":"tests","bytes":20320},
  {"path":"internal/changeworker/authority_darwin_test.go","kind":"tests","bytes":3517},
  {"path":"internal/changeworker/cache_darwin_test.go","kind":"tests","bytes":854},
  {"path":"internal/changeworker/codec.go","kind":"source","bytes":18801},
  {"path":"internal/changeworker/codec_test.go","kind":"tests","bytes":13519},
  {"path":"internal/changeworker/worker.go","kind":"source","bytes":791},
  {"path":"internal/changeworker/worker_darwin.go","kind":"source","bytes":25418},
  {"path":"internal/changeworker/worker_darwin_test.go","kind":"tests","bytes":32999},
  {"path":"internal/changeworker/worker_unsupported.go","kind":"source","bytes":126},
  {"path":"internal/cloudflareadmin/admin.go","kind":"source","bytes":5842},
  {"path":"internal/cloudflareadmin/admin_test.go","kind":"tests","bytes":11830},
  {"path":"internal/cloudflareadmin/credentials.go","kind":"source","bytes":1931},
  {"path":"internal/cloudflareadmin/credentials_test.go","kind":"tests","bytes":4808},
  {"path":"internal/cloudflareadmin/credentials_unix.go","kind":"source","bytes":4587},
  {"path":"internal/cloudflareadmin/credentials_unsupported.go","kind":"source","bytes":368},
  {"path":"internal/cloudflareadmin/dns.go","kind":"source","bytes":6335},
  {"path":"internal/cloudflareadmin/stat_darwin.go","kind":"source","bytes":240},
  {"path":"internal/cloudflareadmin/stat_linux.go","kind":"source","bytes":219},
  {"path":"internal/daemon/accounts.go","kind":"source","bytes":9774},
  {"path":"internal/daemon/accounts_test.go","kind":"tests","bytes":9721},
  {"path":"internal/daemon/api_dispatch.go","kind":"source","bytes":96974},
  {"path":"internal/daemon/api_dispatch_sandbox_darwin_test.go","kind":"tests","bytes":2551},
  {"path":"internal/daemon/api_dispatch_test.go","kind":"tests","bytes":76530},
  {"path":"internal/daemon/api_operator_human_darwin_test.go","kind":"tests","bytes":12143},
  {"path":"internal/daemon/api_projection.go","kind":"source","bytes":9543},
  {"path":"internal/daemon/api_projection_test.go","kind":"tests","bytes":4200},
  {"path":"internal/daemon/attempt_result_binding_darwin_test.go","kind":"tests","bytes":7716},
  {"path":"internal/daemon/browser_agent_control.go","kind":"source","bytes":12109},
  {"path":"internal/daemon/browser_agent_control_darwin_test.go","kind":"tests","bytes":13582},
  {"path":"internal/daemon/browser_backend.go","kind":"source","bytes":65618},
  {"path":"internal/daemon/browser_backend_test.go","kind":"tests","bytes":50585},
  {"path":"internal/daemon/browser_console_test.go","kind":"tests","bytes":46203},
  {"path":"internal/daemon/browser_content.go","kind":"source","bytes":18454},
  {"path":"internal/daemon/browser_content_test.go","kind":"tests","bytes":10179},
  {"path":"internal/daemon/browser_intake.go","kind":"source","bytes":4929},
  {"path":"internal/daemon/browser_intake_test.go","kind":"tests","bytes":4858},
  {"path":"internal/daemon/browser_lifecycle.go","kind":"source","bytes":5653},
  {"path":"internal/daemon/browser_lifecycle_test.go","kind":"tests","bytes":1588},
  {"path":"internal/daemon/browser_mission_test.go","kind":"tests","bytes":5432},
  {"path":"internal/daemon/browser_operator.go","kind":"source","bytes":7504},
  {"path":"internal/daemon/browser_operator_test.go","kind":"tests","bytes":6047},
  {"path":"internal/daemon/browser_production_test.go","kind":"tests","bytes":4559},
  {"path":"internal/daemon/browser_revocation.go","kind":"source","bytes":4080},
  {"path":"internal/daemon/browser_revocation_test.go","kind":"tests","bytes":13254},
  {"path":"internal/daemon/browser_state_test.go","kind":"tests","bytes":10059},
  {"path":"internal/daemon/browser_subscription.go","kind":"source","bytes":7564},
  {"path":"internal/daemon/browser_subscription_shutdown_test.go","kind":"tests","bytes":6388},
  {"path":"internal/daemon/browser_task_enqueue_test.go","kind":"tests","bytes":8376},
  {"path":"internal/daemon/browser_terminal_backend.go","kind":"source","bytes":13065},
  {"path":"internal/daemon/browser_terminal_backend_test.go","kind":"tests","bytes":2052},
  {"path":"internal/daemon/content.go","kind":"source","bytes":20898},
  {"path":"internal/daemon/content_api_test.go","kind":"tests","bytes":16976},
  {"path":"internal/daemon/continuation_context.go","kind":"source","bytes":3074},
  {"path":"internal/daemon/continuation_context_test.go","kind":"tests","bytes":1883},
  {"path":"internal/daemon/database_test.go","kind":"tests","bytes":1099},
  {"path":"internal/daemon/gate.go","kind":"source","bytes":6284},
  {"path":"internal/daemon/gate_test.go","kind":"tests","bytes":8073},
  {"path":"internal/daemon/github.go","kind":"source","bytes":3202},
  {"path":"internal/daemon/handover_darwin.go","kind":"source","bytes":4832},
  {"path":"internal/daemon/handover_darwin_test.go","kind":"tests","bytes":19553},
  {"path":"internal/daemon/identity.go","kind":"source","bytes":9004},
  {"path":"internal/daemon/identity_test.go","kind":"tests","bytes":5705},
  {"path":"internal/daemon/intake.go","kind":"source","bytes":22479},
  {"path":"internal/daemon/intake_legacy.go","kind":"source","bytes":15520},
  {"path":"internal/daemon/intake_legacy_test.go","kind":"tests","bytes":14948},
  {"path":"internal/daemon/intake_review.go","kind":"source","bytes":4015},
  {"path":"internal/daemon/intake_review_test.go","kind":"tests","bytes":5331},
  {"path":"internal/daemon/intake_status.go","kind":"source","bytes":2059},
  {"path":"internal/daemon/intake_status_test.go","kind":"tests","bytes":1273},
  {"path":"internal/daemon/intake_test.go","kind":"tests","bytes":19398},
  {"path":"internal/daemon/legacy_git_retry_darwin_test.go","kind":"tests","bytes":6686},
  {"path":"internal/daemon/live_attempt.go","kind":"source","bytes":27692},
  {"path":"internal/daemon/live_attempt_darwin.go","kind":"source","bytes":43525},
  {"path":"internal/daemon/live_attempt_darwin_test.go","kind":"tests","bytes":34384},
  {"path":"internal/daemon/live_attempt_liveness_test.go","kind":"tests","bytes":9366},
  {"path":"internal/daemon/live_attempt_test.go","kind":"tests","bytes":13171},
  {"path":"internal/daemon/live_attempt_unsupported.go","kind":"source","bytes":155},
  {"path":"internal/daemon/local_ci_lease_darwin.go","kind":"source","bytes":2617},
  {"path":"internal/daemon/local_ci_lease_darwin_test.go","kind":"tests","bytes":1688},
  {"path":"internal/daemon/maintainer.go","kind":"source","bytes":20359},
  {"path":"internal/daemon/maintainer_publication_test.go","kind":"tests","bytes":13132},
  {"path":"internal/daemon/maintainer_test.go","kind":"tests","bytes":14369},
  {"path":"internal/daemon/outcomes.go","kind":"source","bytes":4507},
  {"path":"internal/daemon/outcomes_api_test.go","kind":"tests","bytes":1954},
  {"path":"internal/daemon/parallelism_darwin_test.go","kind":"tests","bytes":5440},
  {"path":"internal/daemon/peer_question_test.go","kind":"tests","bytes":745},
  {"path":"internal/daemon/production_refresh.go","kind":"source","bytes":9398},
  {"path":"internal/daemon/production_refresh_test.go","kind":"tests","bytes":3714},
  {"path":"internal/daemon/project_limits.go","kind":"source","bytes":836},
  {"path":"internal/daemon/project_limits_darwin_test.go","kind":"tests","bytes":2201},
  {"path":"internal/daemon/provider_defaults.go","kind":"source","bytes":7522},
  {"path":"internal/daemon/provider_defaults_test.go","kind":"tests","bytes":12713},
  {"path":"internal/daemon/publication_review_test.go","kind":"tests","bytes":12299},
  {"path":"internal/daemon/push.go","kind":"source","bytes":7529},
  {"path":"internal/daemon/push_test.go","kind":"tests","bytes":11589},
  {"path":"internal/daemon/queued_task_patch.go","kind":"source","bytes":4407},
  {"path":"internal/daemon/recovered_runtime_darwin.go","kind":"source","bytes":17019},
  {"path":"internal/daemon/recovered_runtime_darwin_test.go","kind":"tests","bytes":25996},
  {"path":"internal/daemon/recovery_darwin.go","kind":"source","bytes":39656},
  {"path":"internal/daemon/recovery_darwin_test.go","kind":"tests","bytes":47876},
  {"path":"internal/daemon/recovery_unsupported.go","kind":"source","bytes":410},
  {"path":"internal/daemon/relay_lifecycle.go","kind":"source","bytes":5393},
  {"path":"internal/daemon/relay_lifecycle_test.go","kind":"tests","bytes":22039},
  {"path":"internal/daemon/release.go","kind":"source","bytes":3669},
  {"path":"internal/daemon/release_test.go","kind":"tests","bytes":3711},
  {"path":"internal/daemon/remote_pair.go","kind":"source","bytes":7958},
  {"path":"internal/daemon/remote_pair_test.go","kind":"tests","bytes":19098},
  {"path":"internal/daemon/repository.go","kind":"source","bytes":2774},
  {"path":"internal/daemon/repository_darwin.go","kind":"source","bytes":928},
  {"path":"internal/daemon/repository_darwin_test.go","kind":"tests","bytes":6484},
  {"path":"internal/daemon/repository_github.go","kind":"source","bytes":1502},
  {"path":"internal/daemon/repository_readiness.go","kind":"source","bytes":2715},
  {"path":"internal/daemon/repository_readiness_darwin_test.go","kind":"tests","bytes":5771},
  {"path":"internal/daemon/repository_unsupported.go","kind":"source","bytes":298},
  {"path":"internal/daemon/retained_source_darwin.go","kind":"source","bytes":3227},
  {"path":"internal/daemon/retained_source_unsupported.go","kind":"source","bytes":476},
  {"path":"internal/daemon/review.go","kind":"source","bytes":24456},
  {"path":"internal/daemon/review_api_test.go","kind":"tests","bytes":1523},
  {"path":"internal/daemon/review_public_path_test.go","kind":"tests","bytes":15147},
  {"path":"internal/daemon/review_response_test.go","kind":"tests","bytes":1459},
  {"path":"internal/daemon/review_security_test.go","kind":"tests","bytes":10080},
  {"path":"internal/daemon/review_verdict_test.go","kind":"tests","bytes":418},
  {"path":"internal/daemon/run_fixture_test.go","kind":"tests","bytes":1257},
  {"path":"internal/daemon/run_paths.go","kind":"source","bytes":7776},
  {"path":"internal/daemon/run_paths_test.go","kind":"tests","bytes":14749},
  {"path":"internal/daemon/runtime_darwin.go","kind":"source","bytes":37813},
  {"path":"internal/daemon/runtime_darwin_test.go","kind":"tests","bytes":77852},
  {"path":"internal/daemon/runtime_parent_darwin.go","kind":"source","bytes":15668},
  {"path":"internal/daemon/runtime_remove_darwin.go","kind":"source","bytes":15075},
  {"path":"internal/daemon/runtime_unsupported.go","kind":"source","bytes":2607},
  {"path":"internal/daemon/runtime_unsupported_test.go","kind":"tests","bytes":1031},
  {"path":"internal/daemon/scheduler.go","kind":"source","bytes":10702},
  {"path":"internal/daemon/scheduler_darwin_test.go","kind":"tests","bytes":4426},
  {"path":"internal/daemon/scheduler_test.go","kind":"tests","bytes":25982},
  {"path":"internal/daemon/settlement.go","kind":"source","bytes":5492},
  {"path":"internal/daemon/settlement_darwin_test.go","kind":"tests","bytes":28142},
  {"path":"internal/daemon/shared_queue_test.go","kind":"tests","bytes":5606},
  {"path":"internal/daemon/shell_provider_e2e_darwin_test.go","kind":"tests","bytes":6165},
  {"path":"internal/daemon/success_source_darwin.go","kind":"source","bytes":3134},
  {"path":"internal/daemon/success_source_proposal_darwin_test.go","kind":"tests","bytes":6176},
  {"path":"internal/daemon/success_source_unsupported.go","kind":"source","bytes":236},
  {"path":"internal/daemon/supervisor.go","kind":"source","bytes":4713},
  {"path":"internal/daemon/supervisor_admitted_identity_darwin_test.go","kind":"tests","bytes":991},
  {"path":"internal/daemon/supervisor_control_eof_darwin_test.go","kind":"tests","bytes":3667},
  {"path":"internal/daemon/supervisor_darwin.go","kind":"source","bytes":68307},
  {"path":"internal/daemon/supervisor_darwin_test.go","kind":"tests","bytes":129540},
  {"path":"internal/daemon/supervisor_runner_exit_evidence_darwin_test.go","kind":"tests","bytes":11870},
  {"path":"internal/daemon/supervisor_test.go","kind":"tests","bytes":2092},
  {"path":"internal/daemon/supervisor_unsupported.go","kind":"source","bytes":246},
  {"path":"internal/daemon/supervisor_worker_failure_darwin_test.go","kind":"tests","bytes":3299},
  {"path":"internal/daemon/task_attachments.go","kind":"source","bytes":1044},
  {"path":"internal/daemon/task_attachments_darwin_test.go","kind":"tests","bytes":5085},
  {"path":"internal/daemon/terminal_effects.go","kind":"source","bytes":29910},
  {"path":"internal/daemon/terminal_effects_darwin_test.go","kind":"tests","bytes":83270},
  {"path":"internal/daemon/terminal_effects_unsupported_test.go","kind":"tests","bytes":1455},
  {"path":"internal/daemon/terminal_observation.go","kind":"source","bytes":18250},
  {"path":"internal/daemon/terminal_observation_test.go","kind":"tests","bytes":25817},
  {"path":"internal/daemon/topology.go","kind":"source","bytes":3963},
  {"path":"internal/daemon/topology_test.go","kind":"tests","bytes":4654},
  {"path":"internal/e2e/browser_pty_darwin_test.go","kind":"tests","bytes":31008},
  {"path":"internal/e2e/daemon_blackbox_darwin_test.go","kind":"tests","bytes":20237},
  {"path":"internal/e2e/daemon_handover_darwin_test.go","kind":"tests","bytes":13659},
  {"path":"internal/e2e/database_darwin_test.go","kind":"tests","bytes":1092},
  {"path":"internal/e2e/service_blackbox_darwin_test.go","kind":"tests","bytes":10293},
  {"path":"internal/gitauthor/identity.go","kind":"source","bytes":1198},
  {"path":"internal/gitauthor/identity_test.go","kind":"tests","bytes":875},
  {"path":"internal/install/install.go","kind":"source","bytes":7549},
  {"path":"internal/install/install_darwin.go","kind":"source","bytes":39248},
  {"path":"internal/install/install_darwin_test.go","kind":"tests","bytes":62962},
  {"path":"internal/install/install_unsupported.go","kind":"source","bytes":241},
  {"path":"internal/install/local_api_darwin.go","kind":"source","bytes":31410},
  {"path":"internal/install/local_api_darwin_test.go","kind":"tests","bytes":56656},
  {"path":"internal/install/maintainer.go","kind":"source","bytes":1961},
  {"path":"internal/install/maintainer_darwin.go","kind":"source","bytes":3012},
  {"path":"internal/install/maintainer_darwin_test.go","kind":"tests","bytes":3463},
  {"path":"internal/install/maintainer_unsupported.go","kind":"source","bytes":346},
  {"path":"internal/install/operational_darwin.go","kind":"source","bytes":30130},
  {"path":"internal/install/operational_darwin_test.go","kind":"tests","bytes":42378},
  {"path":"internal/install/operational_unsupported.go","kind":"source","bytes":2133},
  {"path":"internal/install/operational_unsupported_test.go","kind":"tests","bytes":1121},
  {"path":"internal/install/paths.go","kind":"source","bytes":1600},
  {"path":"internal/install/service.go","kind":"source","bytes":15425},
  {"path":"internal/install/service_darwin.go","kind":"source","bytes":35965},
  {"path":"internal/install/service_darwin_test.go","kind":"tests","bytes":35622},
  {"path":"internal/install/service_manage_darwin.go","kind":"source","bytes":33819},
  {"path":"internal/install/service_manage_darwin_test.go","kind":"tests","bytes":44336},
  {"path":"internal/install/service_unsupported.go","kind":"source","bytes":1120},
  {"path":"internal/install/service_unsupported_test.go","kind":"tests","bytes":542},
  {"path":"internal/install/toolchain.go","kind":"source","bytes":5148},
  {"path":"internal/install/toolchain_test.go","kind":"tests","bytes":4606},
  {"path":"internal/kernel/account_test.go","kind":"tests","bytes":11100},
  {"path":"internal/kernel/admission.go","kind":"source","bytes":22849},
  {"path":"internal/kernel/admission_test.go","kind":"tests","bytes":47823},
  {"path":"internal/kernel/agent_stop.go","kind":"source","bytes":6456},
  {"path":"internal/kernel/agent_stop_test.go","kind":"tests","bytes":12256},
  {"path":"internal/kernel/appearance.go","kind":"source","bytes":1444},
  {"path":"internal/kernel/appearance_test.go","kind":"tests","bytes":600},
  {"path":"internal/kernel/attempt_result.go","kind":"source","bytes":52738},
  {"path":"internal/kernel/attempt_result_test.go","kind":"tests","bytes":49333},
  {"path":"internal/kernel/authority_repair_test.go","kind":"tests","bytes":10940},
  {"path":"internal/kernel/browser_authority.go","kind":"source","bytes":42660},
  {"path":"internal/kernel/browser_authority_causal_test.go","kind":"tests","bytes":73546},
  {"path":"internal/kernel/browser_authority_test.go","kind":"tests","bytes":8971},
  {"path":"internal/kernel/browser_task.go","kind":"source","bytes":5813},
  {"path":"internal/kernel/browser_task_test.go","kind":"tests","bytes":16281},
  {"path":"internal/kernel/change.go","kind":"source","bytes":13757},
  {"path":"internal/kernel/change_settlement_repair_test.go","kind":"tests","bytes":22669},
  {"path":"internal/kernel/change_test.go","kind":"tests","bytes":18418},
  {"path":"internal/kernel/chronology_test.go","kind":"tests","bytes":76135},
  {"path":"internal/kernel/console_update.go","kind":"source","bytes":15887},
  {"path":"internal/kernel/console_update_test.go","kind":"tests","bytes":28994},
  {"path":"internal/kernel/content.go","kind":"source","bytes":40516},
  {"path":"internal/kernel/content_optional_test.go","kind":"tests","bytes":8409},
  {"path":"internal/kernel/content_test.go","kind":"tests","bytes":11822},
  {"path":"internal/kernel/continuation.go","kind":"source","bytes":39034},
  {"path":"internal/kernel/continuation_test.go","kind":"tests","bytes":15583},
  {"path":"internal/kernel/errors.go","kind":"source","bytes":2242},
  {"path":"internal/kernel/errors_test.go","kind":"tests","bytes":490},
  {"path":"internal/kernel/finalizing_footprint_test.go","kind":"tests","bytes":3729},
  {"path":"internal/kernel/human_options_migration_test.go","kind":"tests","bytes":4400},
  {"path":"internal/kernel/human_request.go","kind":"source","bytes":42614},
  {"path":"internal/kernel/human_request_terminal_projection_test.go","kind":"tests","bytes":11032},
  {"path":"internal/kernel/human_request_test.go","kind":"tests","bytes":43965},
  {"path":"internal/kernel/human_request_types.go","kind":"source","bytes":5824},
  {"path":"internal/kernel/idle.go","kind":"source","bytes":6360},
  {"path":"internal/kernel/idle_test.go","kind":"tests","bytes":25560},
  {"path":"internal/kernel/intake.go","kind":"source","bytes":12463},
  {"path":"internal/kernel/intake_binding_test.go","kind":"tests","bytes":9923},
  {"path":"internal/kernel/intake_legacy.go","kind":"source","bytes":13350},
  {"path":"internal/kernel/intake_legacy_test.go","kind":"tests","bytes":10748},
  {"path":"internal/kernel/intake_priority.go","kind":"source","bytes":1800},
  {"path":"internal/kernel/intake_store.go","kind":"source","bytes":33791},
  {"path":"internal/kernel/intake_test.go","kind":"tests","bytes":24740},
  {"path":"internal/kernel/lifecycle.go","kind":"source","bytes":50892},
  {"path":"internal/kernel/lifecycle_read.go","kind":"source","bytes":28156},
  {"path":"internal/kernel/lifecycle_test.go","kind":"tests","bytes":54456},
  {"path":"internal/kernel/lifecycle_types.go","kind":"source","bytes":30509},
  {"path":"internal/kernel/migrate.go","kind":"source","bytes":78635},
  {"path":"internal/kernel/migrate_test.go","kind":"tests","bytes":42739},
  {"path":"internal/kernel/mission.go","kind":"source","bytes":7711},
  {"path":"internal/kernel/mission_migration_test.go","kind":"tests","bytes":1850},
  {"path":"internal/kernel/mission_test.go","kind":"tests","bytes":6934},
  {"path":"internal/kernel/outcomes.go","kind":"source","bytes":33918},
  {"path":"internal/kernel/outcomes_test.go","kind":"tests","bytes":16036},
  {"path":"internal/kernel/overseer.go","kind":"source","bytes":21727},
  {"path":"internal/kernel/overseer_context_test.go","kind":"tests","bytes":5139},
  {"path":"internal/kernel/overseer_test.go","kind":"tests","bytes":15107},
  {"path":"internal/kernel/overseer_wakeup.go","kind":"source","bytes":15323},
  {"path":"internal/kernel/peer_integrity_test.go","kind":"tests","bytes":6878},
  {"path":"internal/kernel/peer_question.go","kind":"source","bytes":20490},
  {"path":"internal/kernel/peer_question_test.go","kind":"tests","bytes":23781},
  {"path":"internal/kernel/peer_question_types.go","kind":"source","bytes":2864},
  {"path":"internal/kernel/production.go","kind":"source","bytes":38649},
  {"path":"internal/kernel/production_persistence_test.go","kind":"tests","bytes":42559},
  {"path":"internal/kernel/production_test.go","kind":"tests","bytes":24860},
  {"path":"internal/kernel/production_types.go","kind":"source","bytes":3428},
  {"path":"internal/kernel/project_limits.go","kind":"source","bytes":4651},
  {"path":"internal/kernel/project_limits_test.go","kind":"tests","bytes":6484},
  {"path":"internal/kernel/project_tokens.go","kind":"source","bytes":2312},
  {"path":"internal/kernel/project_tokens_test.go","kind":"tests","bytes":4284},
  {"path":"internal/kernel/provider_activation_test.go","kind":"tests","bytes":7286},
  {"path":"internal/kernel/provider_exit_test.go","kind":"tests","bytes":11992},
  {"path":"internal/kernel/public_state.go","kind":"source","bytes":16734},
  {"path":"internal/kernel/public_state_test.go","kind":"tests","bytes":21640},
  {"path":"internal/kernel/read.go","kind":"source","bytes":21846},
  {"path":"internal/kernel/recoverable_run_test.go","kind":"tests","bytes":9934},
  {"path":"internal/kernel/repository.go","kind":"source","bytes":16646},
  {"path":"internal/kernel/repository_default_revision_test.go","kind":"tests","bytes":2384},
  {"path":"internal/kernel/repository_github.go","kind":"source","bytes":1766},
  {"path":"internal/kernel/repository_github_test.go","kind":"tests","bytes":3379},
  {"path":"internal/kernel/repository_identity.go","kind":"source","bytes":8677},
  {"path":"internal/kernel/repository_identity_test.go","kind":"tests","bytes":5242},
  {"path":"internal/kernel/repository_test.go","kind":"tests","bytes":11352},
  {"path":"internal/kernel/resource_activation_time_test.go","kind":"tests","bytes":13524},
  {"path":"internal/kernel/resource_declared_test.go","kind":"tests","bytes":3403},
  {"path":"internal/kernel/runner_exit_test.go","kind":"tests","bytes":21481},
  {"path":"internal/kernel/schema.go","kind":"source","bytes":60778},
  {"path":"internal/kernel/schema_test.go","kind":"tests","bytes":16848},
  {"path":"internal/kernel/send_back.go","kind":"source","bytes":15021},
  {"path":"internal/kernel/shared_queue_test.go","kind":"tests","bytes":16992},
  {"path":"internal/kernel/source_route.go","kind":"source","bytes":4699},
  {"path":"internal/kernel/source_route_test.go","kind":"tests","bytes":16335},
  {"path":"internal/kernel/sqlite.go","kind":"source","bytes":19889},
  {"path":"internal/kernel/sqlite_ancestor_darwin.go","kind":"source","bytes":257},
  {"path":"internal/kernel/sqlite_ancestor_other.go","kind":"source","bytes":115},
  {"path":"internal/kernel/sqlite_compact.go","kind":"source","bytes":1510},
  {"path":"internal/kernel/sqlite_crash_test.go","kind":"tests","bytes":9671},
  {"path":"internal/kernel/sqlite_files.go","kind":"source","bytes":39762},
  {"path":"internal/kernel/sqlite_image.go","kind":"source","bytes":15998},
  {"path":"internal/kernel/sqlite_image_test.go","kind":"tests","bytes":46794},
  {"path":"internal/kernel/sqlite_lifecycle_cancellation_test.go","kind":"tests","bytes":3563},
  {"path":"internal/kernel/sqlite_parity_test.go","kind":"tests","bytes":21464},
  {"path":"internal/kernel/sqlite_path_test.go","kind":"tests","bytes":14606},
  {"path":"internal/kernel/sqlite_writer_gate_test.go","kind":"tests","bytes":20483},
  {"path":"internal/kernel/store.go","kind":"source","bytes":29514},
  {"path":"internal/kernel/store_test.go","kind":"tests","bytes":40532},
  {"path":"internal/kernel/task_attachments.go","kind":"source","bytes":5883},
  {"path":"internal/kernel/task_attachments_test.go","kind":"tests","bytes":10052},
  {"path":"internal/kernel/task_intervention.go","kind":"source","bytes":23432},
  {"path":"internal/kernel/task_intervention_test.go","kind":"tests","bytes":8797},
  {"path":"internal/kernel/task_list.go","kind":"source","bytes":1904},
  {"path":"internal/kernel/task_list_test.go","kind":"tests","bytes":6247},
  {"path":"internal/kernel/task_recovery.go","kind":"source","bytes":10072},
  {"path":"internal/kernel/task_recovery_test.go","kind":"tests","bytes":18062},
  {"path":"internal/kernel/terminal_diagnostics.go","kind":"source","bytes":3374},
  {"path":"internal/kernel/terminal_diagnostics_test.go","kind":"tests","bytes":3339},
  {"path":"internal/kernel/terminal_session.go","kind":"source","bytes":8674},
  {"path":"internal/kernel/terminal_session_test.go","kind":"tests","bytes":11778},
  {"path":"internal/kernel/terminal_target.go","kind":"source","bytes":4830},
  {"path":"internal/kernel/terminal_target_test.go","kind":"tests","bytes":11044},
  {"path":"internal/kernel/test_database_test.go","kind":"tests","bytes":1588},
  {"path":"internal/kernel/types.go","kind":"source","bytes":20270},
  {"path":"internal/kernel/validate.go","kind":"source","bytes":56013},
  {"path":"internal/kernel/validate_history_test.go","kind":"tests","bytes":8002},
  {"path":"internal/kernel/validated_write_test.go","kind":"tests","bytes":21460},
  {"path":"internal/linear/client.go","kind":"source","bytes":7662},
  {"path":"internal/linear/client_test.go","kind":"tests","bytes":4617},
  {"path":"internal/linear/host_darwin_test.go","kind":"tests","bytes":1445},
  {"path":"internal/maintainer/client.go","kind":"source","bytes":8485},
  {"path":"internal/maintainer/client_test.go","kind":"tests","bytes":4404},
  {"path":"internal/maintainer/host.go","kind":"source","bytes":6503},
  {"path":"internal/maintainer/host_darwin_test.go","kind":"tests","bytes":9209},
  {"path":"internal/maintainer/issues.go","kind":"source","bytes":2453},
  {"path":"internal/maintainer/issues_test.go","kind":"tests","bytes":2374},
  {"path":"internal/maintainer/mcp.go","kind":"source","bytes":2025},
  {"path":"internal/maintainer/mcp_test.go","kind":"tests","bytes":2908},
  {"path":"internal/provider/account_darwin_test.go","kind":"tests","bytes":5347},
  {"path":"internal/provider/provider.go","kind":"source","bytes":58170},
  {"path":"internal/provider/provider_darwin_test.go","kind":"tests","bytes":90585},
  {"path":"internal/provider/tokens.go","kind":"source","bytes":3785},
  {"path":"internal/provider/tokens_test.go","kind":"tests","bytes":2912},
  {"path":"internal/relayhost/connector.go","kind":"source","bytes":23917},
  {"path":"internal/relayhost/connector_test.go","kind":"tests","bytes":28189},
  {"path":"internal/relayhost/fake_loopback_test.go","kind":"tests","bytes":6626},
  {"path":"internal/relayhost/fake_relay_test.go","kind":"tests","bytes":6845},
  {"path":"internal/relayhost/identity.go","kind":"source","bytes":7479},
  {"path":"internal/relayhost/identity_test.go","kind":"tests","bytes":3206},
  {"path":"internal/relayhost/queue.go","kind":"source","bytes":3155},
  {"path":"internal/relayhost/queue_test.go","kind":"tests","bytes":1270},
  {"path":"internal/relayhost/record.go","kind":"source","bytes":3607},
  {"path":"internal/relayhost/record_test.go","kind":"tests","bytes":4541},
  {"path":"internal/relayhost/session.go","kind":"source","bytes":6899},
  {"path":"internal/relayhost/token.go","kind":"source","bytes":7916},
  {"path":"internal/relayhost/token_test.go","kind":"tests","bytes":3998},
  {"path":"internal/relayhost/vectors_test.go","kind":"tests","bytes":13085},
  {"path":"internal/review/coordinator.go","kind":"source","bytes":14076},
  {"path":"internal/review/coordinator_test.go","kind":"tests","bytes":12010},
  {"path":"internal/runner/attempt_control_eof_darwin_test.go","kind":"tests","bytes":4968},
  {"path":"internal/runner/attempt_darwin.go","kind":"source","bytes":67789},
  {"path":"internal/runner/attempt_darwin_test.go","kind":"tests","bytes":120226},
  {"path":"internal/runner/attempt_handover_darwin_test.go","kind":"tests","bytes":25238},
  {"path":"internal/runner/attempt_ready_darwin_test.go","kind":"tests","bytes":2831},
  {"path":"internal/runner/attempt_result.go","kind":"source","bytes":20798},
  {"path":"internal/runner/attempt_result_test.go","kind":"tests","bytes":20216},
  {"path":"internal/runner/attempt_terminal_darwin_test.go","kind":"tests","bytes":8497},
  {"path":"internal/runner/attempt_types.go","kind":"source","bytes":11274},
  {"path":"internal/runner/claude_task.go","kind":"source","bytes":2263},
  {"path":"internal/runner/committed_launch_darwin_test.go","kind":"tests","bytes":4529},
  {"path":"internal/runner/control_fork_race_darwin_test.go","kind":"tests","bytes":1636},
  {"path":"internal/runner/environment_darwin_test.go","kind":"tests","bytes":10610},
  {"path":"internal/runner/group_signal_darwin_test.go","kind":"tests","bytes":6619},
  {"path":"internal/runner/launch_darwin.go","kind":"source","bytes":11802},
  {"path":"internal/runner/private_darwin.go","kind":"source","bytes":1319},
  {"path":"internal/runner/private_unsupported.go","kind":"source","bytes":158},
  {"path":"internal/runner/process_darwin.go","kind":"source","bytes":49799},
  {"path":"internal/runner/process_terminate_race_darwin_test.go","kind":"tests","bytes":18825},
  {"path":"internal/runner/protocol.go","kind":"source","bytes":9488},
  {"path":"internal/runner/protocol_test.go","kind":"tests","bytes":16252},
  {"path":"internal/runner/pty_darwin.go","kind":"source","bytes":4080},
  {"path":"internal/runner/pty_darwin_test.go","kind":"tests","bytes":21628},
  {"path":"internal/runner/runner_darwin_test.go","kind":"tests","bytes":80287},
  {"path":"internal/runner/spool.go","kind":"source","bytes":10190},
  {"path":"internal/runner/spool_darwin.go","kind":"source","bytes":194},
  {"path":"internal/runner/spool_test.go","kind":"tests","bytes":21935},
  {"path":"internal/runner/spool_unsupported.go","kind":"source","bytes":111},
  {"path":"internal/runner/terminal_loop_darwin.go","kind":"source","bytes":38972},
  {"path":"internal/runner/terminal_loop_darwin_test.go","kind":"tests","bytes":9504},
  {"path":"internal/runner/terminal_stream.go","kind":"source","bytes":6126},
  {"path":"internal/runner/terminal_stream_test.go","kind":"tests","bytes":6731},
  {"path":"internal/runner/types.go","kind":"source","bytes":5525},
  {"path":"internal/runner/unsupported.go","kind":"source","bytes":3639},
  {"path":"internal/runner/unsupported_test.go","kind":"tests","bytes":1529},
  {"path":"internal/topology/special_file_unix_test.go","kind":"tests","bytes":1002},
  {"path":"internal/topology/topology.go","kind":"source","bytes":24332},
  {"path":"internal/topology/topology_test.go","kind":"tests","bytes":18434},
  {"path":"launchd/README.md","kind":"documentation","bytes":1439},
  {"path":"protocol/browser/fixtures/account_link.json","kind":"configuration","bytes":122},
  {"path":"protocol/browser/fixtures/account_link_result.json","kind":"configuration","bytes":125},
  {"path":"protocol/browser/fixtures/account_update.json","kind":"configuration","bytes":144},
  {"path":"protocol/browser/fixtures/account_update_result.json","kind":"configuration","bytes":127},
  {"path":"protocol/browser/fixtures/accounts.json","kind":"configuration","bytes":503},
  {"path":"protocol/browser/fixtures/accounts_discover.json","kind":"configuration","bytes":57},
  {"path":"protocol/browser/fixtures/agent_control.json","kind":"configuration","bytes":338},
  {"path":"protocol/browser/fixtures/agent_control_result.json","kind":"configuration","bytes":241},
  {"path":"protocol/browser/fixtures/agent_update.json","kind":"configuration","bytes":504},
  {"path":"protocol/browser/fixtures/agent_update_result.json","kind":"configuration","bytes":123},
  {"path":"protocol/browser/fixtures/attachment_retention.json","kind":"configuration","bytes":75},
  {"path":"protocol/browser/fixtures/attachment_retention_result.json","kind":"configuration","bytes":82},
  {"path":"protocol/browser/fixtures/auth_prove.json","kind":"configuration","bytes":235},
  {"path":"protocol/browser/fixtures/auth_result.json","kind":"configuration","bytes":110},
  {"path":"protocol/browser/fixtures/browser_client_revoke.json","kind":"configuration","bytes":129},
  {"path":"protocol/browser/fixtures/browser_client_revoke_result.json","kind":"configuration","bytes":127},
  {"path":"protocol/browser/fixtures/browser_clients.json","kind":"configuration","bytes":305},
  {"path":"protocol/browser/fixtures/browser_clients_get.json","kind":"configuration","bytes":58},
  {"path":"protocol/browser/fixtures/error.json","kind":"configuration","bytes":66},
  {"path":"protocol/browser/fixtures/factory_dispatch.json","kind":"configuration","bytes":94},
  {"path":"protocol/browser/fixtures/factory_dispatch_result.json","kind":"configuration","bytes":92},
  {"path":"protocol/browser/fixtures/github_connection.json","kind":"configuration","bytes":88},
  {"path":"protocol/browser/fixtures/github_connection_result.json","kind":"configuration","bytes":125},
  {"path":"protocol/browser/fixtures/hello.json","kind":"configuration","bytes":204},
  {"path":"protocol/browser/fixtures/human_request_cancel_run.json","kind":"configuration","bytes":193},
  {"path":"protocol/browser/fixtures/human_request_cancel_run_result.json","kind":"configuration","bytes":226},
  {"path":"protocol/browser/fixtures/human_request_detail.json","kind":"configuration","bytes":427},
  {"path":"protocol/browser/fixtures/human_request_detail_get.json","kind":"configuration","bytes":133},
  {"path":"protocol/browser/fixtures/human_request_reply.json","kind":"configuration","bytes":165},
  {"path":"protocol/browser/fixtures/human_request_reply_result.json","kind":"configuration","bytes":170},
  {"path":"protocol/browser/fixtures/intake.json","kind":"configuration","bytes":118},
  {"path":"protocol/browser/fixtures/intake_result.json","kind":"configuration","bytes":795},
  {"path":"protocol/browser/fixtures/pair_prove.json","kind":"configuration","bytes":418},
  {"path":"protocol/browser/fixtures/pair_result.json","kind":"configuration","bytes":111},
  {"path":"protocol/browser/fixtures/project_content.json","kind":"configuration","bytes":170},
  {"path":"protocol/browser/fixtures/project_content_result.json","kind":"configuration","bytes":165},
  {"path":"protocol/browser/fixtures/project_create.json","kind":"configuration","bytes":167},
  {"path":"protocol/browser/fixtures/project_create_result.json","kind":"configuration","bytes":129},
  {"path":"protocol/browser/fixtures/project_limits.json","kind":"configuration","bytes":171},
  {"path":"protocol/browser/fixtures/project_limits_result.json","kind":"configuration","bytes":129},
  {"path":"protocol/browser/fixtures/push_subscribe.json","kind":"configuration","bytes":269},
  {"path":"protocol/browser/fixtures/push_subscribe_result.json","kind":"configuration","bytes":57},
  {"path":"protocol/browser/fixtures/remote_invite.json","kind":"configuration","bytes":58},
  {"path":"protocol/browser/fixtures/remote_invite_result.json","kind":"configuration","bytes":4210},
  {"path":"protocol/browser/fixtures/repositories.json","kind":"configuration","bytes":319},
  {"path":"protocol/browser/fixtures/repositories_get.json","kind":"configuration","bytes":107},
  {"path":"protocol/browser/fixtures/repository_mutate.json","kind":"configuration","bytes":164},
  {"path":"protocol/browser/fixtures/repository_mutate_result.json","kind":"configuration","bytes":293},
  {"path":"protocol/browser/fixtures/run_paths.json","kind":"configuration","bytes":189},
  {"path":"protocol/browser/fixtures/run_paths_get.json","kind":"configuration","bytes":99},
  {"path":"protocol/browser/fixtures/state_changed.json","kind":"configuration","bytes":75},
  {"path":"protocol/browser/fixtures/state_get.json","kind":"configuration","bytes":46},
  {"path":"protocol/browser/fixtures/state_snapshot.json","kind":"configuration","bytes":2074},
  {"path":"protocol/browser/fixtures/state_watch.json","kind":"configuration","bytes":79},
  {"path":"protocol/browser/fixtures/task_attachment.json","kind":"configuration","bytes":118},
  {"path":"protocol/browser/fixtures/task_attachment_result.json","kind":"configuration","bytes":72},
  {"path":"protocol/browser/fixtures/task_detail.json","kind":"configuration","bytes":188},
  {"path":"protocol/browser/fixtures/task_detail_get.json","kind":"configuration","bytes":122},
  {"path":"protocol/browser/fixtures/task_enqueue.json","kind":"configuration","bytes":323},
  {"path":"protocol/browser/fixtures/task_enqueue_result.json","kind":"configuration","bytes":143},
  {"path":"protocol/browser/fixtures/task_history.json","kind":"configuration","bytes":250},
  {"path":"protocol/browser/fixtures/task_history_get.json","kind":"configuration","bytes":99},
  {"path":"protocol/browser/fixtures/task_list.json","kind":"configuration","bytes":398},
  {"path":"protocol/browser/fixtures/task_list_get.json","kind":"configuration","bytes":188},
  {"path":"protocol/browser/fixtures/task_update.json","kind":"configuration","bytes":240},
  {"path":"protocol/browser/fixtures/task_update_result.json","kind":"configuration","bytes":120},
  {"path":"protocol/browser/fixtures/terminal_ack.json","kind":"configuration","bytes":101},
  {"path":"protocol/browser/fixtures/terminal_attach.json","kind":"configuration","bytes":249},
  {"path":"protocol/browser/fixtures/terminal_attached.json","kind":"configuration","bytes":205},
  {"path":"protocol/browser/fixtures/terminal_detach.json","kind":"configuration","bytes":124},
  {"path":"protocol/browser/fixtures/terminal_detached.json","kind":"configuration","bytes":126},
  {"path":"protocol/browser/fixtures/terminal_eof.json","kind":"configuration","bytes":121},
  {"path":"protocol/browser/fixtures/terminal_exit.json","kind":"configuration","bytes":168},
  {"path":"protocol/browser/fixtures/terminal_input.hex","kind":"unclassified","bytes":91},
  {"path":"protocol/browser/fixtures/terminal_input_result.json","kind":"configuration","bytes":203},
  {"path":"protocol/browser/fixtures/terminal_lease_acquire.json","kind":"configuration","bytes":235},
  {"path":"protocol/browser/fixtures/terminal_lease_release.json","kind":"configuration","bytes":252},
  {"path":"protocol/browser/fixtures/terminal_lease_renew.json","kind":"configuration","bytes":250},
  {"path":"protocol/browser/fixtures/terminal_lease_result.json","kind":"configuration","bytes":306},
  {"path":"protocol/browser/fixtures/terminal_output.hex","kind":"unclassified","bytes":91},
  {"path":"protocol/browser/fixtures/terminal_reset.json","kind":"configuration","bytes":146},
  {"path":"protocol/browser/fixtures/terminal_resize.json","kind":"configuration","bytes":265},
  {"path":"protocol/browser/fixtures/terminal_resized.json","kind":"configuration","bytes":162},
  {"path":"protocol/browser/fixtures/terminal_target.json","kind":"configuration","bytes":290},
  {"path":"protocol/browser/fixtures/terminal_target_get.json","kind":"configuration","bytes":171},
  {"path":"protocol/browser/fixtures/topology.json","kind":"configuration","bytes":928},
  {"path":"protocol/browser/fixtures/topology_get.json","kind":"configuration","bytes":99},
  {"path":"protocol/browser/fixtures/transcript.json","kind":"configuration","bytes":2243},
  {"path":"protocol/browser/manifest.json","kind":"configuration","bytes":13859},
  {"path":"relay/README.md","kind":"documentation","bytes":8090},
  {"path":"relay/fixtures/tokens.json","kind":"configuration","bytes":3243},
  {"path":"relay/package-lock.json","kind":"configuration","bytes":51047},
  {"path":"relay/package.json","kind":"configuration","bytes":383},
  {"path":"relay/scripts/local-ci.sh","kind":"source","bytes":1616},
  {"path":"relay/scripts/with-clean-wrangler-env.sh","kind":"source","bytes":1940},
  {"path":"relay/src/encoding.ts","kind":"source","bytes":2123},
  {"path":"relay/src/envelope.ts","kind":"source","bytes":3023},
  {"path":"relay/src/index.ts","kind":"source","bytes":1068},
  {"path":"relay/src/relay.ts","kind":"source","bytes":17656},
  {"path":"relay/src/tokens.ts","kind":"source","bytes":7028},
  {"path":"relay/tests/helpers.mjs","kind":"tests","bytes":14443},
  {"path":"relay/tests/relay.integration.test.mjs","kind":"tests","bytes":30990},
  {"path":"relay/tests/relay.rate.test.mjs","kind":"tests","bytes":3424},
  {"path":"relay/tests/tokens.vectors.test.mjs","kind":"tests","bytes":5684},
  {"path":"relay/tests/ts-loader.mjs","kind":"tests","bytes":792},
  {"path":"relay/tsconfig.json","kind":"configuration","bytes":476},
  {"path":"relay/wrangler.jsonc","kind":"unclassified","bytes":743},
  {"path":"scripts/bootstrap-maintainer-v2.sh","kind":"source","bytes":13513},
  {"path":"scripts/check-cloudflare-access-policy.sh","kind":"source","bytes":3160},
  {"path":"scripts/check-toolchain-pins.sh","kind":"source","bytes":3610},
  {"path":"scripts/cloudflare-env-clean.sh","kind":"source","bytes":3492},
  {"path":"scripts/cold-review.sh","kind":"source","bytes":15395},
  {"path":"scripts/dark-factory-browser-mcp.py","kind":"source","bytes":3783},
  {"path":"scripts/deploy-runtime.py","kind":"source","bytes":12054},
  {"path":"scripts/deploy-site.sh","kind":"source","bytes":1415},
  {"path":"scripts/factory-autonomy.py","kind":"source","bytes":54048},
  {"path":"scripts/factory-delivery.py","kind":"source","bytes":3919},
  {"path":"scripts/factory-intake.example.json","kind":"configuration","bytes":549},
  {"path":"scripts/factory-intake.py","kind":"source","bytes":22025},
  {"path":"scripts/factory-production-reviews.py","kind":"source","bytes":8663},
  {"path":"scripts/factory-production.py","kind":"source","bytes":22434},
  {"path":"scripts/factory-publication.py","kind":"source","bytes":832},
  {"path":"scripts/factory-release.py","kind":"source","bytes":46794},
  {"path":"scripts/factory-review-intake.py","kind":"source","bytes":82595},
  {"path":"scripts/gh-comment.sh","kind":"source","bytes":1274},
  {"path":"scripts/github-repo-settings.sh","kind":"source","bytes":7937},
  {"path":"scripts/github-step-summary.sh","kind":"source","bytes":2403},
  {"path":"scripts/go-check.sh","kind":"source","bytes":2888},
  {"path":"scripts/go-ci-owned.sh","kind":"source","bytes":2599},
  {"path":"scripts/go-e2e-tools.sh","kind":"source","bytes":1392},
  {"path":"scripts/go-e2e.sh","kind":"source","bytes":3074},
  {"path":"scripts/go-gate-environment.sh","kind":"source","bytes":2776},
  {"path":"scripts/go-service-e2e.sh","kind":"source","bytes":2370},
  {"path":"scripts/import-issues.sh","kind":"source","bytes":3048},
  {"path":"scripts/legacy-workflow-fake.py","kind":"source","bytes":24980},
  {"path":"scripts/legacy-workflow-provider.go","kind":"source","bytes":761},
  {"path":"scripts/local-ci-environment.sh","kind":"source","bytes":14200},
  {"path":"scripts/local-ci-lease.sh","kind":"source","bytes":33822},
  {"path":"scripts/local-ci.sh","kind":"source","bytes":3538},
  {"path":"scripts/new-worktree.sh","kind":"source","bytes":1936},
  {"path":"scripts/package-release.sh","kind":"source","bytes":10450},
  {"path":"scripts/prepare-release-source.sh","kind":"source","bytes":2588},
  {"path":"scripts/publication-parents.sh","kind":"source","bytes":1048},
  {"path":"scripts/publish-release.sh","kind":"source","bytes":12373},
  {"path":"scripts/reinstall-service.sh","kind":"source","bytes":18934},
  {"path":"scripts/render-homebrew-formula.sh","kind":"source","bytes":5048},
  {"path":"scripts/supervision.md","kind":"documentation","bytes":1132},
  {"path":"scripts/test-bootstrap-maintainer-v2.sh","kind":"source","bytes":7262},
  {"path":"scripts/test-cloudflare-access-policy-shape.sh","kind":"source","bytes":7193},
  {"path":"scripts/test-cloudflare-env.sh","kind":"source","bytes":5990},
  {"path":"scripts/test-cold-review.sh","kind":"source","bytes":25233},
  {"path":"scripts/test-deploy-site.sh","kind":"source","bytes":4397},
  {"path":"scripts/test-factory-autonomy.py","kind":"source","bytes":82563},
  {"path":"scripts/test-factory-browser-live.py","kind":"source","bytes":5623},
  {"path":"scripts/test-factory-browser.py","kind":"source","bytes":3539},
  {"path":"scripts/test-factory-delivery.py","kind":"source","bytes":3512},
  {"path":"scripts/test-factory-intake.py","kind":"source","bytes":15388},
  {"path":"scripts/test-factory-production-reviews.py","kind":"source","bytes":7311},
  {"path":"scripts/test-factory-production.py","kind":"source","bytes":16786},
  {"path":"scripts/test-factory-release.py","kind":"source","bytes":70382},
  {"path":"scripts/test-factory-review-intake.py","kind":"source","bytes":134639},
  {"path":"scripts/test-fixtures/fake-release-gh.sh","kind":"source","bytes":5577},
  {"path":"scripts/test-gh-comment.sh","kind":"source","bytes":2066},
  {"path":"scripts/test-github-step-summary.sh","kind":"source","bytes":4916},
  {"path":"scripts/test-go-e2e-tools.sh","kind":"source","bytes":6245},
  {"path":"scripts/test-go-gates.sh","kind":"source","bytes":21952},
  {"path":"scripts/test-legacy-workflow-e2e.py","kind":"source","bytes":20727},
  {"path":"scripts/test-local-ci-environment.sh","kind":"source","bytes":14659},
  {"path":"scripts/test-local-ci-lease-mutations.sh","kind":"source","bytes":9092},
  {"path":"scripts/test-local-ci-lease.sh","kind":"source","bytes":35106},
  {"path":"scripts/test-new-worktree.sh","kind":"source","bytes":2781},
  {"path":"scripts/test-package-release.sh","kind":"source","bytes":22896},
  {"path":"scripts/test-prepare-release-source.sh","kind":"source","bytes":4088},
  {"path":"scripts/test-publication-parents.sh","kind":"source","bytes":3927},
  {"path":"scripts/test-publish-release.sh","kind":"source","bytes":11608},
  {"path":"scripts/test-reinstall-service.sh","kind":"source","bytes":32484},
  {"path":"scripts/test-repository-settings.sh","kind":"source","bytes":18528},
  {"path":"scripts/test-shell-provider-e2e.sh","kind":"source","bytes":316},
  {"path":"scripts/test-verification-profile.mjs","kind":"source","bytes":4342},
  {"path":"scripts/test-verify-adversarial-review.sh","kind":"source","bytes":17748},
  {"path":"scripts/test-verify-live-runtime.py","kind":"source","bytes":912},
  {"path":"scripts/verification-profile.mjs","kind":"source","bytes":2425},
  {"path":"scripts/verify-adversarial-review.sh","kind":"source","bytes":8727},
  {"path":"scripts/verify-live-browser.mjs","kind":"source","bytes":2530},
  {"path":"scripts/verify-live-runtime.py","kind":"source","bytes":3055},
  {"path":"scripts/verify-live-site.py","kind":"source","bytes":2553},
  {"path":"scripts/with-cloudflare-env.sh","kind":"source","bytes":571},
  {"path":"scripts/with-local-ci-lease.sh","kind":"source","bytes":197},
  {"path":"spikes/browser-connectivity/README.md","kind":"documentation","bytes":3378},
  {"path":"spikes/browser-connectivity/main.go","kind":"source","bytes":1324},
  {"path":"spikes/browser-connectivity/probe.html","kind":"source","bytes":4513},
  {"path":"spikes/browser-connectivity/server.go","kind":"source","bytes":15012},
  {"path":"spikes/browser-connectivity/server_test.go","kind":"tests","bytes":18791},
  {"path":"web/.gitignore","kind":"configuration","bytes":47},
  {"path":"web/apps/dev/index.html","kind":"source","bytes":316},
  {"path":"web/apps/dev/package.json","kind":"configuration","bytes":452},
  {"path":"web/apps/dev/src/actual-topology.ts","kind":"source","bytes":70184},
  {"path":"web/apps/dev/src/main.tsx","kind":"source","bytes":21132},
  {"path":"web/apps/dev/src/styles.css","kind":"source","bytes":420},
  {"path":"web/apps/dev/tsconfig.json","kind":"configuration","bytes":102},
  {"path":"web/fixtures/state.d.mts","kind":"source","bytes":587},
  {"path":"web/fixtures/state.mjs","kind":"source","bytes":6499},
  {"path":"web/package.json","kind":"configuration","bytes":981},
  {"path":"web/packages/client/package.json","kind":"configuration","bytes":383},
  {"path":"web/packages/client/src/control.ts","kind":"source","bytes":119837},
  {"path":"web/packages/client/src/errors.ts","kind":"source","bytes":766},
  {"path":"web/packages/client/src/index.ts","kind":"source","bytes":587},
  {"path":"web/packages/client/src/manifest.ts","kind":"source","bytes":13003},
  {"path":"web/packages/client/src/project-content.ts","kind":"source","bytes":2065},
  {"path":"web/packages/client/src/remote/index.ts","kind":"source","bytes":155},
  {"path":"web/packages/client/src/remote/invitation.ts","kind":"source","bytes":5615},
  {"path":"web/packages/client/src/remote/manager.ts","kind":"source","bytes":24167},
  {"path":"web/packages/client/src/remote/relay-socket.ts","kind":"source","bytes":8240},
  {"path":"web/packages/client/src/remote/store.ts","kind":"source","bytes":6038},
  {"path":"web/packages/client/src/remote/tokens.ts","kind":"source","bytes":6108},
  {"path":"web/packages/client/src/session.ts","kind":"source","bytes":98632},
  {"path":"web/packages/client/src/state.ts","kind":"source","bytes":1891},
  {"path":"web/packages/client/src/terminal.ts","kind":"source","bytes":3566},
  {"path":"web/packages/client/src/terminal_session.ts","kind":"source","bytes":39098},
  {"path":"web/packages/client/src/transcript.ts","kind":"source","bytes":4435},
  {"path":"web/packages/client/test/client.test.mjs","kind":"tests","bytes":32933},
  {"path":"web/packages/client/test/e2e/go-browser-pty.mjs","kind":"tests","bytes":18749},
  {"path":"web/packages/client/test/packed-consumer.test.mjs","kind":"tests","bytes":3056},
  {"path":"web/packages/client/test/remote-fake.mjs","kind":"tests","bytes":8224},
  {"path":"web/packages/client/test/remote-invitation.test.mjs","kind":"tests","bytes":6003},
  {"path":"web/packages/client/test/remote-manager.test.mjs","kind":"tests","bytes":25994},
  {"path":"web/packages/client/test/remote-socket.test.mjs","kind":"tests","bytes":7961},
  {"path":"web/packages/client/test/remote-tokens.test.mjs","kind":"tests","bytes":6589},
  {"path":"web/packages/client/test/session.test.mjs","kind":"tests","bytes":132891},
  {"path":"web/packages/client/test/state.test.mjs","kind":"tests","bytes":33928},
  {"path":"web/packages/client/test/terminal-lifecycle.test.mjs","kind":"tests","bytes":56354},
  {"path":"web/packages/ui/package.json","kind":"configuration","bytes":940},
  {"path":"web/packages/ui/src/console-interactions.tsx","kind":"source","bytes":2268},
  {"path":"web/packages/ui/src/console-screens.tsx","kind":"source","bytes":12664},
  {"path":"web/packages/ui/src/console-sidebar.tsx","kind":"source","bytes":93282},
  {"path":"web/packages/ui/src/console-view.ts","kind":"source","bytes":22361},
  {"path":"web/packages/ui/src/contraption.test.mjs","kind":"tests","bytes":1223},
  {"path":"web/packages/ui/src/contraption.tsx","kind":"source","bytes":4243},
  {"path":"web/packages/ui/src/factory-app-controller.ts","kind":"source","bytes":71419},
  {"path":"web/packages/ui/src/factory-app.tsx","kind":"source","bytes":21860},
  {"path":"web/packages/ui/src/factory-console.css","kind":"source","bytes":41726},
  {"path":"web/packages/ui/src/factory-console.tsx","kind":"source","bytes":28323},
  {"path":"web/packages/ui/src/factory-scene/appearance.ts","kind":"source","bytes":9129},
  {"path":"web/packages/ui/src/factory-scene/factory-scene.test.mjs","kind":"tests","bytes":100213},
  {"path":"web/packages/ui/src/factory-scene/factory-scene.tsx","kind":"source","bytes":62130},
  {"path":"web/packages/ui/src/factory-scene/idle-life.test.mjs","kind":"tests","bytes":15875},
  {"path":"web/packages/ui/src/factory-scene/idle-life.ts","kind":"source","bytes":9107},
  {"path":"web/packages/ui/src/factory-scene/messages.test.mjs","kind":"tests","bytes":14843},
  {"path":"web/packages/ui/src/factory-scene/messages.ts","kind":"source","bytes":4927},
  {"path":"web/packages/ui/src/factory-scene/movement.ts","kind":"source","bytes":10426},
  {"path":"web/packages/ui/src/factory-scene/scene.ts","kind":"source","bytes":13732},
  {"path":"web/packages/ui/src/factory-scene/sprite-editor.tsx","kind":"source","bytes":3377},
  {"path":"web/packages/ui/src/factory-scene/sprites/gen-sprites.mjs","kind":"source","bytes":49284},
  {"path":"web/packages/ui/src/factory-scene/sprites/preview.html","kind":"source","bytes":69796},
  {"path":"web/packages/ui/src/factory-scene/sprites/sprites.generated.ts","kind":"source","bytes":36465},
  {"path":"web/packages/ui/src/factory-scene/sprites/sprites.png","kind":"assets","bytes":9498},
  {"path":"web/packages/ui/src/factory-settings-coordinator.ts","kind":"source","bytes":19263},
  {"path":"web/packages/ui/src/floor-appearance.ts","kind":"source","bytes":1768},
  {"path":"web/packages/ui/src/human-request-flow.ts","kind":"source","bytes":8385},
  {"path":"web/packages/ui/src/index.ts","kind":"source","bytes":420},
  {"path":"web/packages/ui/src/missions-panel.tsx","kind":"source","bytes":13150},
  {"path":"web/packages/ui/src/production-area.test.mjs","kind":"tests","bytes":1443},
  {"path":"web/packages/ui/src/production-area.tsx","kind":"source","bytes":9186},
  {"path":"web/packages/ui/src/production-data.test.mjs","kind":"tests","bytes":2824},
  {"path":"web/packages/ui/src/production-data.ts","kind":"source","bytes":4778},
  {"path":"web/packages/ui/src/production-panel.test.mjs","kind":"tests","bytes":7621},
  {"path":"web/packages/ui/src/production-panel.tsx","kind":"source","bytes":11707},
  {"path":"web/packages/ui/src/production-view.test.mjs","kind":"tests","bytes":10851},
  {"path":"web/packages/ui/src/production-view.ts","kind":"source","bytes":16931},
  {"path":"web/packages/ui/src/project-library.tsx","kind":"source","bytes":11585},
  {"path":"web/packages/ui/src/project-outcomes.tsx","kind":"source","bytes":12114},
  {"path":"web/packages/ui/src/remote/remote-app.tsx","kind":"source","bytes":34341},
  {"path":"web/packages/ui/src/remote/remote-view.ts","kind":"source","bytes":5459},
  {"path":"web/packages/ui/src/remote-invite.tsx","kind":"source","bytes":4003},
  {"path":"web/packages/ui/src/terminal-controller.ts","kind":"source","bytes":18648},
  {"path":"web/packages/ui/src/xterm-terminal.tsx","kind":"source","bytes":6674},
  {"path":"web/packages/ui/test/artifact-inventory.test.mjs","kind":"tests","bytes":1044},
  {"path":"web/packages/ui/test/console-view.test.mjs","kind":"tests","bytes":4076},
  {"path":"web/packages/ui/test/factory-app-controller.test.mjs","kind":"tests","bytes":56654},
  {"path":"web/packages/ui/test/factory-app-lifecycle.test.mjs","kind":"tests","bytes":4165},
  {"path":"web/packages/ui/test/factory-app-sidebar.test.mjs","kind":"tests","bytes":11314},
  {"path":"web/packages/ui/test/factory-app-terminal.test.mjs","kind":"tests","bytes":74829},
  {"path":"web/packages/ui/test/factory-console.test.mjs","kind":"tests","bytes":189148},
  {"path":"web/packages/ui/test/factory-settings-coordinator.test.mjs","kind":"tests","bytes":15472},
  {"path":"web/packages/ui/test/fixtures/strict-composition-loader.mjs","kind":"tests","bytes":5333},
  {"path":"web/packages/ui/test/fixtures/strict-composition-probe.mjs","kind":"tests","bytes":3695},
  {"path":"web/packages/ui/test/missions-panel.test.mjs","kind":"tests","bytes":7011},
  {"path":"web/packages/ui/test/packed-consumer.test.mjs","kind":"tests","bytes":4898},
  {"path":"web/packages/ui/test/project-library.test.mjs","kind":"tests","bytes":2034},
  {"path":"web/packages/ui/test/remote-app.test.mjs","kind":"tests","bytes":46168},
  {"path":"web/packages/ui/test/terminal-controller.test.mjs","kind":"tests","bytes":36110},
  {"path":"web/packages/ui/test/xterm-terminal.test.mjs","kind":"tests","bytes":6996},
  {"path":"web/packages/ui/tsconfig.json","kind":"configuration","bytes":185},
  {"path":"web/pnpm-lock.yaml","kind":"configuration","bytes":24946},
  {"path":"web/pnpm-workspace.yaml","kind":"configuration","bytes":199},
  {"path":"web/scripts/package-artifacts","kind":"unclassified","bytes":8208},
  {"path":"web/scripts/package-artifacts.mjs","kind":"source","bytes":41821},
  {"path":"web/scripts/package-artifacts.test.mjs","kind":"tests","bytes":30125},
  {"path":"web/toolchain-integrity.json","kind":"configuration","bytes":1038},
  {"path":"web/tsconfig.build.json","kind":"configuration","bytes":183},
  {"path":"web/tsconfig.json","kind":"configuration","bytes":423},
];
