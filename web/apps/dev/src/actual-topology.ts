// Actual scanner and served projection at 44dce533; illustrative worker state lives in the dev tour.
import type { TopologyView } from "@dark-factory/client";
const topology: TopologyView = {
  "projectId": "11111111111111111111111111111111",
  "digest": "8158980599af3a0ee7b8fae1ea3e0a7ec75e5cace434ad1de71c0a88bfa68e27",
  "sourceRevision": "44dce533fe3a7dc17cc626c85c24209ad3606c67",
  "nodes": [
    {
      "id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "parent_id": "faa867c91ab48778ed90cee11bccbe8f04aeb1ef717fdd943b4f66f33e9b4805",
      "kind": "module",
      "path": ".",
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
          "unclassified": 6
        },
        "total": {
          "source": 332,
          "tests": 208,
          "documentation": 31,
          "configuration": 97,
          "assets": 1,
          "unclassified": 13
        },
        "samples": [
          ".DS_Store",
          ".env.txt",
          ".gitignore"
        ],
        "samples_omitted": 14
      }
    },
    {
      "id": "faa867c91ab48778ed90cee11bccbe8f04aeb1ef717fdd943b4f66f33e9b4805",
      "parent_id": "",
      "kind": "repository",
      "path": ".",
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
          "unclassified": 6
        },
        "total": {
          "source": 332,
          "tests": 208,
          "documentation": 31,
          "configuration": 97,
          "assets": 1,
          "unclassified": 13
        },
        "samples": [
          ".DS_Store",
          ".env.txt",
          ".gitignore"
        ],
        "samples_omitted": 14
      }
    },
    {
      "id": "7a0c287bae0e03d9f1203b6ee00b64a4eeded4d167e6e7a5d3bc0b3570f65ae5",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "cmd",
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
          "source": 8,
          "tests": 10,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      }
    },
    {
      "id": "d0d7dd5fb4bedaef466bb4eda38dff4b684736e71af2e3d0d42bdeeae3ea655d",
      "parent_id": "7a0c287bae0e03d9f1203b6ee00b64a4eeded4d167e6e7a5d3bc0b3570f65ae5",
      "kind": "package",
      "path": "cmd/cloudflare-admin",
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
      }
    },
    {
      "id": "8e50578244d9d3e5143df90c1b255b428718415d612f9a5bb5488b2ec974cd90",
      "parent_id": "7a0c287bae0e03d9f1203b6ee00b64a4eeded4d167e6e7a5d3bc0b3570f65ae5",
      "kind": "package",
      "path": "cmd/factory-runner",
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
      }
    },
    {
      "id": "566a1aab67e3c300d17794aef78f3677751c1609db8638ad5738126b35e5c109",
      "parent_id": "7a0c287bae0e03d9f1203b6ee00b64a4eeded4d167e6e7a5d3bc0b3570f65ae5",
      "kind": "package",
      "path": "cmd/factoryctl",
      "label": "main",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 5,
          "tests": 7,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 5,
          "tests": 7,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "browser_darwin.go",
          "browser_other.go",
          "human_options_test.go"
        ],
        "samples_omitted": 9
      }
    },
    {
      "id": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
      "parent_id": "7a0c287bae0e03d9f1203b6ee00b64a4eeded4d167e6e7a5d3bc0b3570f65ae5",
      "kind": "package",
      "path": "cmd/factoryd",
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
      }
    },
    {
      "id": "df9f7ce4b9e538f9a2bcee09bb84f2cd7e4e6268205316e3355593ba46a8c3fb",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "package",
      "path": "control-plane",
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
          "source": 12,
          "tests": 4,
          "documentation": 1,
          "configuration": 7,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          ".gitignore",
          "Cargo.lock",
          "Cargo.toml"
        ],
        "samples_omitted": 4
      }
    },
    {
      "id": "805730549746abecfba33a5a25241e57d141c23c2073817316ec4b17a878b9a9",
      "parent_id": "df9f7ce4b9e538f9a2bcee09bb84f2cd7e4e6268205316e3355593ba46a8c3fb",
      "kind": "directory",
      "path": "control-plane/migrations",
      "label": "migrations",
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
          "0001_maintainer_deliveries.sql",
          "0004_maintainer_operations.sql"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "f3811e52e99b4822ca459955bc4303a295ea0dfef3fcf2e8c03467c5bc91e660",
      "parent_id": "df9f7ce4b9e538f9a2bcee09bb84f2cd7e4e6268205316e3355593ba46a8c3fb",
      "kind": "directory",
      "path": "control-plane/scripts",
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
          "test-clean-wrangler-env.sh"
        ],
        "samples_omitted": 1
      }
    },
    {
      "id": "06162c33d517317b7b6e774c4ba8813c830cc7b54757742d71ae50123d489b5e",
      "parent_id": "df9f7ce4b9e538f9a2bcee09bb84f2cd7e4e6268205316e3355593ba46a8c3fb",
      "kind": "directory",
      "path": "control-plane/src",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
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
          "access.rs",
          "github_app.rs",
          "journal.rs"
        ],
        "samples_omitted": 3
      }
    },
    {
      "id": "dbdce54a19c96c05478b263f0c7ec609301e7a63777fea02689fd3f835f9632f",
      "parent_id": "df9f7ce4b9e538f9a2bcee09bb84f2cd7e4e6268205316e3355593ba46a8c3fb",
      "kind": "directory",
      "path": "control-plane/tests",
      "label": "tests",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 4,
          "documentation": 0,
          "configuration": 1,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "cloudflare_boundary.rs",
          "inactive_default.rs",
          "maintainer_webhook.rs"
        ],
        "samples_omitted": 1
      }
    },
    {
      "id": "165c364b082b0c7e0c5883427470ff987dcc046872eb7e5b08f7a97a415bd518",
      "parent_id": "dbdce54a19c96c05478b263f0c7ec609301e7a63777fea02689fd3f835f9632f",
      "kind": "directory",
      "path": "control-plane/tests/fixtures",
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
      }
    },
    {
      "id": "8a4108a6ca1270cc40ccee2fb9c884c537e3077849549c33f837225607924959",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "docs",
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
          "source": 47,
          "tests": 0,
          "documentation": 19,
          "configuration": 0,
          "assets": 0,
          "unclassified": 3
        },
        "samples": [
          "install.md",
          "providers.md"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "f11fa1d1a4a0bc31af4f2928c7f7b971ee3f4970c469afbdce984559df3d1006",
      "parent_id": "8a4108a6ca1270cc40ccee2fb9c884c537e3077849549c33f837225607924959",
      "kind": "directory",
      "path": "docs/development",
      "label": "development",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 10,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 10,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "ARCHITECTURE_REVIEW.md",
          "ATTEMPT_RESULT_RECOVERY.md",
          "BROWSER_BOUNDARY_SIMPLIFICATION_HANDOVER.md"
        ],
        "samples_omitted": 7
      }
    },
    {
      "id": "3aa843acdfc5a6f038d63bd142f7099e0897f6790d8d8ceea331b9a141615c85",
      "parent_id": "8a4108a6ca1270cc40ccee2fb9c884c537e3077849549c33f837225607924959",
      "kind": "directory",
      "path": "docs/internal",
      "label": "internal",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 5,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 47,
          "tests": 0,
          "documentation": 7,
          "configuration": 0,
          "assets": 0,
          "unclassified": 3
        },
        "samples": [
          "LIVE_DOGFOOD_STATE.md",
          "accounts-usage-spike-2026-09-06.md",
          "handoff-2026-08-17.md"
        ],
        "samples_omitted": 2
      }
    },
    {
      "id": "a36a3bcd57e506941f7d4a6192a586f952722691c882106ca33a62159494d9a7",
      "parent_id": "3aa843acdfc5a6f038d63bd142f7099e0897f6790d8d8ceea331b9a141615c85",
      "kind": "directory",
      "path": "docs/internal/abandoned-worktree-patches",
      "label": "abandoned-worktree-patches",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 3
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 3
        },
        "samples": [
          "issue-242-tui-keymap-private-extraction.patch",
          "issue-276-tranche1-reset-author.patch",
          "stage-status-hygiene.patch"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "255bfa3b39f3c480522d8eb92e5fe008c7007d3cb843d4bb649bbc68f359eab6",
      "parent_id": "3aa843acdfc5a6f038d63bd142f7099e0897f6790d8d8ceea331b9a141615c85",
      "kind": "directory",
      "path": "docs/internal/dogfood",
      "label": "dogfood",
      "language": "",
      "size_bucket": "small",
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
          "2026-08-17.md"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "273a76a3461d3e9bd24343f96e5177fe481e0c7b28056f05e5c24e2fda1c8a3d",
      "parent_id": "3aa843acdfc5a6f038d63bd142f7099e0897f6790d8d8ceea331b9a141615c85",
      "kind": "directory",
      "path": "docs/internal/mutation-kit",
      "label": "mutation-kit",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 47,
          "tests": 0,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 47,
          "tests": 0,
          "documentation": 1,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "README.md",
          "c1.py",
          "c2.py"
        ],
        "samples_omitted": 45
      }
    },
    {
      "id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "internal",
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
          "source": 144,
          "tests": 163,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      }
    },
    {
      "id": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/api",
      "label": "api",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 6,
          "tests": 9,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 6,
          "tests": 9,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "client.go",
          "client_test.go",
          "overseer_control_test.go"
        ],
        "samples_omitted": 12
      }
    },
    {
      "id": "8cb93f7f557214d458f3f38efdf96588010c300ba234d43825b019d9bc287e76",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/browser",
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
          "connection.go"
        ],
        "samples_omitted": 8
      }
    },
    {
      "id": "6a6f4362f74356c1be072961fe7c244f26a1b647907d1d65f5cbb5ca383522bf",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/browserprotocol",
      "label": "browserprotocol",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 9,
          "tests": 7,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 9,
          "tests": 7,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "agent_control.go",
          "binary.go",
          "console_control.go"
        ],
        "samples_omitted": 13
      }
    },
    {
      "id": "a82f73bc57662ed710188da7672954ee40d431d3168b2720920c0dc263a56303",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/buildinfo",
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
          "buildinfo.go"
        ],
        "samples_omitted": 1
      }
    },
    {
      "id": "fb033c5404bda5c722244bd21560b12e27d3fce40b58cbb8c42ee2f1577ff89f",
      "parent_id": "a82f73bc57662ed710188da7672954ee40d431d3168b2720920c0dc263a56303",
      "kind": "directory",
      "path": "internal/buildinfo/cmd",
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
      }
    },
    {
      "id": "bd8d4a6dda3d3d81457d5c7ab47e656d6352b5f187cbfb2ac5ef72a7bf91a66c",
      "parent_id": "fb033c5404bda5c722244bd21560b12e27d3fce40b58cbb8c42ee2f1577ff89f",
      "kind": "package",
      "path": "internal/buildinfo/cmd/release-artifact",
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
      }
    },
    {
      "id": "cd1d90a8a361abe6f1ca79b017f2d44977ea71ccf77b4f230654be7082d181ca",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/change",
      "label": "change",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 8,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 8,
          "tests": 8,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "errors.go",
          "git.go",
          "git_darwin.go"
        ],
        "samples_omitted": 13
      }
    },
    {
      "id": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/changeworker",
      "label": "changeworker",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 4,
          "tests": 3,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 4,
          "tests": 3,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "authority_darwin_test.go",
          "codec.go",
          "codec_test.go"
        ],
        "samples_omitted": 4
      }
    },
    {
      "id": "e8770f640b8dbead8fdd7f7bbe7efb3cc45f590814620778cfd4259cf28c91ed",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/cloudflareadmin",
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
          "credentials.go"
        ],
        "samples_omitted": 6
      }
    },
    {
      "id": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/daemon",
      "label": "daemon",
      "language": "go",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 34,
          "tests": 39,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 34,
          "tests": 39,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "accounts.go",
          "accounts_test.go",
          "api_dispatch.go"
        ],
        "samples_omitted": 70
      }
    },
    {
      "id": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/e2e",
      "label": "e2e_test",
      "language": "go",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "browser_pty_darwin_test.go",
          "daemon_blackbox_darwin_test.go",
          "database_darwin_test.go"
        ],
        "samples_omitted": 1
      }
    },
    {
      "id": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/install",
      "label": "install",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 11,
          "tests": 7,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 11,
          "tests": 7,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "install.go",
          "install_darwin.go",
          "install_darwin_test.go"
        ],
        "samples_omitted": 15
      }
    },
    {
      "id": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/kernel",
      "label": "kernel",
      "language": "go",
      "size_bucket": "large",
      "inventory": {
        "direct": {
          "source": 35,
          "tests": 46,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 35,
          "tests": 46,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "account_test.go",
          "admission.go",
          "admission_test.go"
        ],
        "samples_omitted": 78
      }
    },
    {
      "id": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/provider",
      "label": "provider",
      "language": "go",
      "size_bucket": "medium",
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
          "account_darwin_test.go",
          "provider.go",
          "provider_darwin_test.go"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "8f1d96588c8b296c38a2d8807722569e9acc9921f2d5d7f2e2ab488f7a2e39bb",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/relayhost",
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
          "fake_loopback_test.go"
        ],
        "samples_omitted": 11
      }
    },
    {
      "id": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/runner",
      "label": "runner",
      "language": "go",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 16,
          "tests": 16,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 16,
          "tests": 16,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "attempt_control_eof_darwin_test.go",
          "attempt_darwin.go",
          "attempt_darwin_test.go"
        ],
        "samples_omitted": 29
      }
    },
    {
      "id": "7f2d63a37864935dd19f238e4a435123c4d495dc279b5f14cd13ce546a6ded1f",
      "parent_id": "b8f5e5614a48956214ec4a56f3d99988c9bdb659358dae0956d7de8466ae7302",
      "kind": "package",
      "path": "internal/topology",
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
      }
    },
    {
      "id": "991387e965ee0f69cb0b6c09e0259f88fe772eddb86b3a779519a2c3d6b8e63d",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "launchd",
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
      }
    },
    {
      "id": "cdc7a21809f9d80332b774cb18f6e1261710db59dc831dce21b6cd8e91b04c58",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "protocol",
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
          "configuration": 69,
          "assets": 0,
          "unclassified": 2
        },
        "samples": [],
        "samples_omitted": 0
      }
    },
    {
      "id": "ce00f20d0cc09400c04852e1adaf6238ec7f142e98c81da5a3e6461d125dfea8",
      "parent_id": "cdc7a21809f9d80332b774cb18f6e1261710db59dc831dce21b6cd8e91b04c58",
      "kind": "directory",
      "path": "protocol/browser",
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
          "configuration": 69,
          "assets": 0,
          "unclassified": 2
        },
        "samples": [
          "manifest.json"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "3201a0687038b5d4d86f411d4544667672294f63aeeb313416c9acda47ecd957",
      "parent_id": "ce00f20d0cc09400c04852e1adaf6238ec7f142e98c81da5a3e6461d125dfea8",
      "kind": "directory",
      "path": "protocol/browser/fixtures",
      "label": "fixtures",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 68,
          "assets": 0,
          "unclassified": 2
        },
        "total": {
          "source": 0,
          "tests": 0,
          "documentation": 0,
          "configuration": 68,
          "assets": 0,
          "unclassified": 2
        },
        "samples": [
          "account_link.json",
          "account_link_result.json",
          "account_update.json"
        ],
        "samples_omitted": 67
      }
    },
    {
      "id": "218de1820031265bac5c8cbf0a297f4554dc9d0fae56a9d0bdacb873f1885f52",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "package",
      "path": "relay",
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
          "tests": 4,
          "documentation": 1,
          "configuration": 4,
          "assets": 0,
          "unclassified": 1
        },
        "samples": [
          "README.md",
          "package-lock.json",
          "package.json"
        ],
        "samples_omitted": 2
      }
    },
    {
      "id": "72972f390d5f7521542d8dd26a96a8c943c8319f0ef14938115c6d852ceb754c",
      "parent_id": "218de1820031265bac5c8cbf0a297f4554dc9d0fae56a9d0bdacb873f1885f52",
      "kind": "directory",
      "path": "relay/fixtures",
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
      }
    },
    {
      "id": "6da8a9992369235873c5aef29f4888c010eec9dd5b19ecdd8ba5dfbe35658b7e",
      "parent_id": "218de1820031265bac5c8cbf0a297f4554dc9d0fae56a9d0bdacb873f1885f52",
      "kind": "directory",
      "path": "relay/scripts",
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
      }
    },
    {
      "id": "7ce83475762db93c51f675e7ec839dd57240de5f292411b37dc7e13d17c58988",
      "parent_id": "218de1820031265bac5c8cbf0a297f4554dc9d0fae56a9d0bdacb873f1885f52",
      "kind": "directory",
      "path": "relay/src",
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
          "index.ts"
        ],
        "samples_omitted": 2
      }
    },
    {
      "id": "3c2242ac86a827438a590c711a3373c39144add67738ea7cc2f1dbf0117d9d6d",
      "parent_id": "218de1820031265bac5c8cbf0a297f4554dc9d0fae56a9d0bdacb873f1885f52",
      "kind": "directory",
      "path": "relay/tests",
      "label": "tests",
      "language": "",
      "size_bucket": "small",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 4,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "helpers.mjs",
          "relay.integration.test.mjs",
          "tokens.vectors.test.mjs"
        ],
        "samples_omitted": 1
      }
    },
    {
      "id": "7f9ca09631965391e05aa53bdffdd4e16fc5ba90d88f224b7f07f4f4f76a334a",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "scripts",
      "label": "scripts",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 67,
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 68,
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "bootstrap-maintainer-v2.sh",
          "check-toolchain-pins.sh",
          "cloudflare-env-clean.sh"
        ],
        "samples_omitted": 66
      }
    },
    {
      "id": "37d67d59db7cdf08c55b148efc29532fc7b648e6182805570113f609dc108fb2",
      "parent_id": "7f9ca09631965391e05aa53bdffdd4e16fc5ba90d88f224b7f07f4f4f76a334a",
      "kind": "directory",
      "path": "scripts/test-fixtures",
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
      }
    },
    {
      "id": "4166151fe9787ac9da4c3a4e6e9e6147bd40be7c15315a7a63ac29028d3753b9",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "directory",
      "path": "spikes",
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
      }
    },
    {
      "id": "ca98ea89b3f430da6af3991126994ce85d4a7f76f86c57e63894060747c69a98",
      "parent_id": "4166151fe9787ac9da4c3a4e6e9e6147bd40be7c15315a7a63ac29028d3753b9",
      "kind": "package",
      "path": "spikes/browser-connectivity",
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
          "probe.html"
        ],
        "samples_omitted": 2
      }
    },
    {
      "id": "beb73574937bbdbae14b573f86108e1a79271b03a26cb8d9bc230a7fd5cbd50e",
      "parent_id": "919663c49d377ad0c7cbed0eaabac2d38a6a0dd8a765d3b5e982b7ab66fe01b3",
      "kind": "package",
      "path": "web",
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
          "source": 43,
          "tests": 26,
          "documentation": 0,
          "configuration": 12,
          "assets": 1,
          "unclassified": 1
        },
        "samples": [
          ".gitignore",
          "package.json",
          "pnpm-lock.yaml"
        ],
        "samples_omitted": 4
      }
    },
    {
      "id": "9c2ed569d505ffc7bb0d605530daa6814bb598d0573876a38ba7362cf6d663b9",
      "parent_id": "beb73574937bbdbae14b573f86108e1a79271b03a26cb8d9bc230a7fd5cbd50e",
      "kind": "directory",
      "path": "web/apps",
      "label": "apps",
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
          "tests": 0,
          "documentation": 0,
          "configuration": 2,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      }
    },
    {
      "id": "9b8ee07ed9fa8755cdc6559732fd936e3c1166ba160a354caaf06e6262d84f3f",
      "parent_id": "9c2ed569d505ffc7bb0d605530daa6814bb598d0573876a38ba7362cf6d663b9",
      "kind": "package",
      "path": "web/apps/dev",
      "label": "dark-factory-dev",
      "language": "typescript",
      "size_bucket": "small",
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
          "source": 3,
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
      }
    },
    {
      "id": "bb7e962da65654baf5fd1d6175f7e9279d13a3471cbb7b042407d58abcaad535",
      "parent_id": "9b8ee07ed9fa8755cdc6559732fd936e3c1166ba160a354caaf06e6262d84f3f",
      "kind": "directory",
      "path": "web/apps/dev/src",
      "label": "src",
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
          "main.tsx",
          "styles.css"
        ],
        "samples_omitted": 0
      }
    },
    {
      "id": "2d55cf0a080824345427690b7b5a5f74f54b36b5c72b58f3526c1cee98869d0b",
      "parent_id": "beb73574937bbdbae14b573f86108e1a79271b03a26cb8d9bc230a7fd5cbd50e",
      "kind": "directory",
      "path": "web/fixtures",
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
      }
    },
    {
      "id": "43cd154deefc82c9d08b265cbbca768577e513b1ff3e09f8260299c89fe0fc22",
      "parent_id": "beb73574937bbdbae14b573f86108e1a79271b03a26cb8d9bc230a7fd5cbd50e",
      "kind": "directory",
      "path": "web/packages",
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
          "source": 37,
          "tests": 25,
          "documentation": 0,
          "configuration": 3,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [],
        "samples_omitted": 0
      }
    },
    {
      "id": "85dd5a19e4b25aa552f028bcb786093f326897281a17b3dd214fd5c44c02d80e",
      "parent_id": "43cd154deefc82c9d08b265cbbca768577e513b1ff3e09f8260299c89fe0fc22",
      "kind": "package",
      "path": "web/packages/client",
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
          "source": 15,
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
      }
    },
    {
      "id": "c7930f4d0b3ddbaaae4f7c356d64425d8f5371ef8052a5ce5dfe0320b323493a",
      "parent_id": "85dd5a19e4b25aa552f028bcb786093f326897281a17b3dd214fd5c44c02d80e",
      "kind": "directory",
      "path": "web/packages/client/src",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 9,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 15,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "control.ts",
          "errors.ts",
          "index.ts"
        ],
        "samples_omitted": 6
      }
    },
    {
      "id": "6660b4f14fec72641e6a5917aef9e637280235c087aca6073164e90ebab46b22",
      "parent_id": "c7930f4d0b3ddbaaae4f7c356d64425d8f5371ef8052a5ce5dfe0320b323493a",
      "kind": "directory",
      "path": "web/packages/client/src/remote",
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
          "manager.ts"
        ],
        "samples_omitted": 3
      }
    },
    {
      "id": "ff09356180ca749c46dfdc4bf02cd9fa91ec36ff71828574e1ccfb699b072202",
      "parent_id": "85dd5a19e4b25aa552f028bcb786093f326897281a17b3dd214fd5c44c02d80e",
      "kind": "directory",
      "path": "web/packages/client/test",
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
          "remote-fake.mjs"
        ],
        "samples_omitted": 7
      }
    },
    {
      "id": "2ac83c1992d05b558fd97ba5ed542e550499d4cbd521c9cf541fa09ae899287d",
      "parent_id": "ff09356180ca749c46dfdc4bf02cd9fa91ec36ff71828574e1ccfb699b072202",
      "kind": "directory",
      "path": "web/packages/client/test/e2e",
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
      }
    },
    {
      "id": "17580c795cd22527271faf563fe77104b722a8083a9a4dcd6b63e146b71e03ce",
      "parent_id": "43cd154deefc82c9d08b265cbbca768577e513b1ff3e09f8260299c89fe0fc22",
      "kind": "package",
      "path": "web/packages/ui",
      "label": "@dark-factory/ui",
      "language": "typescript",
      "size_bucket": "medium",
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
          "source": 22,
          "tests": 14,
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
      }
    },
    {
      "id": "11636d370bb15d9558663d08558c922b1626162f74e21ae4223ceb7f4fe2f2cc",
      "parent_id": "17580c795cd22527271faf563fe77104b722a8083a9a4dcd6b63e146b71e03ce",
      "kind": "directory",
      "path": "web/packages/ui/src",
      "label": "src",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 12,
          "tests": 0,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 22,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [
          "console-screens.tsx",
          "console-sidebar.tsx",
          "console-view.ts"
        ],
        "samples_omitted": 9
      }
    },
    {
      "id": "b2e6bb92ca06a9924fe39dd632d6f6c113086b63c2c33e967bfee776fa95c8a2",
      "parent_id": "11636d370bb15d9558663d08558c922b1626162f74e21ae4223ceb7f4fe2f2cc",
      "kind": "directory",
      "path": "web/packages/ui/src/factory-scene",
      "label": "factory-scene",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 5,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 8,
          "tests": 1,
          "documentation": 0,
          "configuration": 0,
          "assets": 1,
          "unclassified": 0
        },
        "samples": [
          "appearance.ts",
          "factory-scene.test.mjs",
          "factory-scene.tsx"
        ],
        "samples_omitted": 3
      }
    },
    {
      "id": "d5d76c227cdb894c685799fea168673cd39897474c88bee80f115f7a00d237a3",
      "parent_id": "b2e6bb92ca06a9924fe39dd632d6f6c113086b63c2c33e967bfee776fa95c8a2",
      "kind": "directory",
      "path": "web/packages/ui/src/factory-scene/sprites",
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
          "sprites.generated.ts"
        ],
        "samples_omitted": 1
      }
    },
    {
      "id": "a07ac2504a223923499172bca8553691c2dadbebfddc7dc88041fe351527dc57",
      "parent_id": "11636d370bb15d9558663d08558c922b1626162f74e21ae4223ceb7f4fe2f2cc",
      "kind": "directory",
      "path": "web/packages/ui/src/remote",
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
      }
    },
    {
      "id": "3938746d0a7ce7f95b196abfc0df9363456625ce4b4721d84f368e134d75494c",
      "parent_id": "17580c795cd22527271faf563fe77104b722a8083a9a4dcd6b63e146b71e03ce",
      "kind": "directory",
      "path": "web/packages/ui/test",
      "label": "test",
      "language": "",
      "size_bucket": "medium",
      "inventory": {
        "direct": {
          "source": 0,
          "tests": 11,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "total": {
          "source": 0,
          "tests": 13,
          "documentation": 0,
          "configuration": 0,
          "assets": 0,
          "unclassified": 0
        },
        "samples": [
          "artifact-inventory.test.mjs",
          "console-view.test.mjs",
          "factory-app-controller.test.mjs"
        ],
        "samples_omitted": 8
      }
    },
    {
      "id": "36a177418988f13f5564de8d7464b5efdbca87c8ed42c4e19f916223a90cbc51",
      "parent_id": "3938746d0a7ce7f95b196abfc0df9363456625ce4b4721d84f368e134d75494c",
      "kind": "directory",
      "path": "web/packages/ui/test/fixtures",
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
      }
    },
    {
      "id": "50355d56cd6dceeaba85a1547906158295ebe2c436f6edb1b4c67781d660df00",
      "parent_id": "beb73574937bbdbae14b573f86108e1a79271b03a26cb8d9bc230a7fd5cbd50e",
      "kind": "directory",
      "path": "web/scripts",
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
      }
    }
  ],
  "dependencies": {
    "source": "go-imports-package-manifests",
    "edges": [
      {
        "from": "17580c795cd22527271faf563fe77104b722a8083a9a4dcd6b63e146b71e03ce",
        "to": "85dd5a19e4b25aa552f028bcb786093f326897281a17b3dd214fd5c44c02d80e",
        "weight": 1
      },
      {
        "from": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 3
      },
      {
        "from": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
        "to": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
        "weight": 2
      },
      {
        "from": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 2
      },
      {
        "from": "566a1aab67e3c300d17794aef78f3677751c1609db8638ad5738126b35e5c109",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 1
      },
      {
        "from": "566a1aab67e3c300d17794aef78f3677751c1609db8638ad5738126b35e5c109",
        "to": "a82f73bc57662ed710188da7672954ee40d431d3168b2720920c0dc263a56303",
        "weight": 1
      },
      {
        "from": "566a1aab67e3c300d17794aef78f3677751c1609db8638ad5738126b35e5c109",
        "to": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
        "weight": 7
      },
      {
        "from": "566a1aab67e3c300d17794aef78f3677751c1609db8638ad5738126b35e5c109",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 3
      },
      {
        "from": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 4
      },
      {
        "from": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "to": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
        "weight": 3
      },
      {
        "from": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "to": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "weight": 1
      },
      {
        "from": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "to": "cd1d90a8a361abe6f1ca79b017f2d44977ea71ccf77b4f230654be7082d181ca",
        "weight": 4
      },
      {
        "from": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "to": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
        "weight": 5
      },
      {
        "from": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 3
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 65
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
        "weight": 6
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "6a6f4362f74356c1be072961fe7c244f26a1b647907d1d65f5cbb5ca383522bf",
        "weight": 23
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "weight": 5
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "7f2d63a37864935dd19f238e4a435123c4d495dc279b5f14cd13ce546a6ded1f",
        "weight": 3
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "8cb93f7f557214d458f3f38efdf96588010c300ba234d43825b019d9bc287e76",
        "weight": 22
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "8f1d96588c8b296c38a2d8807722569e9acc9921f2d5d7f2e2ab488f7a2e39bb",
        "weight": 5
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
        "weight": 9
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "cd1d90a8a361abe6f1ca79b017f2d44977ea71ccf77b4f230654be7082d181ca",
        "weight": 6
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
        "weight": 24
      },
      {
        "from": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 9
      },
      {
        "from": "8cb93f7f557214d458f3f38efdf96588010c300ba234d43825b019d9bc287e76",
        "to": "6a6f4362f74356c1be072961fe7c244f26a1b647907d1d65f5cbb5ca383522bf",
        "weight": 10
      },
      {
        "from": "8e50578244d9d3e5143df90c1b255b428718415d612f9a5bb5488b2ec974cd90",
        "to": "7de27928b0b7868f8f874bfe1fe79f8f301f3c05834ad16a998cee6637efd2d4",
        "weight": 1
      },
      {
        "from": "8e50578244d9d3e5143df90c1b255b428718415d612f9a5bb5488b2ec974cd90",
        "to": "a82f73bc57662ed710188da7672954ee40d431d3168b2720920c0dc263a56303",
        "weight": 1
      },
      {
        "from": "8e50578244d9d3e5143df90c1b255b428718415d612f9a5bb5488b2ec974cd90",
        "to": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
        "weight": 1
      },
      {
        "from": "8f1d96588c8b296c38a2d8807722569e9acc9921f2d5d7f2e2ab488f7a2e39bb",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 2
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 2
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "476aedb420f9707ed6d58dcde0614816e0153b787261d40d62aed88239016364",
        "weight": 1
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "weight": 2
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "8f1d96588c8b296c38a2d8807722569e9acc9921f2d5d7f2e2ab488f7a2e39bb",
        "weight": 1
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "a82f73bc57662ed710188da7672954ee40d431d3168b2720920c0dc263a56303",
        "weight": 1
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
        "weight": 2
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "cd1d90a8a361abe6f1ca79b017f2d44977ea71ccf77b4f230654be7082d181ca",
        "weight": 2
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
        "weight": 1
      },
      {
        "from": "95b29bd0d651754b9b5bf039cc870d845121bc32356effba205eb30e1ec8461c",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 2
      },
      {
        "from": "9b8ee07ed9fa8755cdc6559732fd936e3c1166ba160a354caaf06e6262d84f3f",
        "to": "17580c795cd22527271faf563fe77104b722a8083a9a4dcd6b63e146b71e03ce",
        "weight": 1
      },
      {
        "from": "9b8ee07ed9fa8755cdc6559732fd936e3c1166ba160a354caaf06e6262d84f3f",
        "to": "85dd5a19e4b25aa552f028bcb786093f326897281a17b3dd214fd5c44c02d80e",
        "weight": 1
      },
      {
        "from": "bd8d4a6dda3d3d81457d5c7ab47e656d6352b5f187cbfb2ac5ef72a7bf91a66c",
        "to": "a82f73bc57662ed710188da7672954ee40d431d3168b2720920c0dc263a56303",
        "weight": 1
      },
      {
        "from": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 4
      },
      {
        "from": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 4
      },
      {
        "from": "d0d7dd5fb4bedaef466bb4eda38dff4b684736e71af2e3d0d42bdeeae3ea655d",
        "to": "e8770f640b8dbead8fdd7f7bbe7efb3cc45f590814620778cfd4259cf28c91ed",
        "weight": 1
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 2
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "8a7921b43b91d5a2ce531d408c3fb65f067b8246929a8f250251d743e20d7c45",
        "weight": 1
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "8cb93f7f557214d458f3f38efdf96588010c300ba234d43825b019d9bc287e76",
        "weight": 1
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "c76331fc607a6f2e21f36a288a3657d8449b0185690fb972d88ca3e4f76215d1",
        "weight": 2
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "cd1d90a8a361abe6f1ca79b017f2d44977ea71ccf77b4f230654be7082d181ca",
        "weight": 2
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "ce93ade07fc1394d5c391e8ae8a7fbde82747d7a55d57cb720925c06d0c5b1a5",
        "weight": 2
      },
      {
        "from": "f60d4aa9009ec49c4408a7b1b9d4e5be834d1bec8cacc38406be1c46d1aef04f",
        "to": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "weight": 3
      },
      {
        "from": "fcbaa7d154971bcc8da49786697242ad913c69618c13b852e3fe612a42cdd483",
        "to": "0c30c55fdd03e784464b35e0ae326d369c49954aed75a20c8f912fe763a62185",
        "weight": 7
      }
    ],
    "omitted": 0
  },
  "inventoryOmitted": 0
};

export default topology;
