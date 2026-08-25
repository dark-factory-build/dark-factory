use std::{fs, path::Path};

fn project_file(path: &str) -> String {
    fs::read_to_string(Path::new(env!("CARGO_MANIFEST_DIR")).join(path))
        .unwrap_or_else(|error| panic!("failed to read {path}: {error}"))
}

#[test]
fn production_runtime_is_cloudflare_only() {
    let manifest = project_file("Cargo.toml");

    assert!(manifest.contains("worker = { version = \"=0.8.5\""));
    for removed_dependency in ["vercel_runtime", "sqlx", "reqwest", "url ="] {
        assert!(
            !manifest.contains(removed_dependency),
            "removed production dependency remains: {removed_dependency}"
        );
    }

    for removed_path in [
        "api/broker.rs",
        "tools/runtime_bootstrap.rs",
        "scripts/bootstrap-production.sh",
        "src/neon.rs",
        "vercel.json",
        ".vercelignore",
        ".env.example",
    ] {
        assert!(
            !Path::new(env!("CARGO_MANIFEST_DIR"))
                .join(removed_path)
                .exists(),
            "removed deployment path remains: {removed_path}"
        );
    }
}

/// A global body limit silently rejected valid publication envelopes before
/// `publish_commit` could apply its typed file-count and per-file bounds. Keep
/// the signed webhook limit separate and derive the MCP ceiling above every
/// request its schema permits, so neither path is unbounded or contradictory.
#[test]
fn body_limits_are_scoped_to_their_routes() {
    let lib = project_file("src/lib.rs");
    let mcp = project_file("src/mcp.rs");
    assert!(lib.contains(
        "axum::routing::post(maintainer::receive)\n                .layer(DefaultBodyLimit::max(maintainer::MAX_BODY_BYTES))"
    ));
    assert!(lib.contains(
        "axum::routing::post(mcp::receive).layer(DefaultBodyLimit::max(mcp::MAX_BODY_BYTES))"
    ));
    assert!(!lib.contains(".layer(DefaultBodyLimit::max(MAX_BODY_BYTES))"));
    assert!(mcp.contains("pub(crate) const MAX_BODY_BYTES: usize = 52 * 1024 * 1024;"));
}

/// Workers' `Request` constructor accepts only `follow` and `manual` and throws on
/// `error`, so `RequestRedirect::Error` means the request is never built and the call
/// fails closed with no diagnosis. Shipping it once left `/readyz` permanently 503 and
/// every authenticated MCP call 401, and neither the unit tests nor the `workerd`
/// integration test could reach the affected paths. Assert it over the source instead.
#[test]
fn no_outbound_request_uses_a_redirect_mode_workers_rejects() {
    // Walk the directory rather than list files: an enumerated allowlist would
    // silently stop covering the next module added, and the next module added
    // on this branch is the one that publishes commits.
    let source_dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
    let mut checked = 0_usize;
    for entry in fs::read_dir(&source_dir).expect("src/ is readable") {
        let path = entry.expect("readable directory entry").path();
        if path.extension().is_none_or(|extension| extension != "rs") {
            continue;
        }
        let source = fs::read_to_string(&path).expect("readable source file");
        assert!(
            !source.contains("RequestRedirect::Error"),
            "{} builds an outbound request Workers will refuse to construct",
            path.display()
        );
        checked += 1;
    }
    assert!(
        checked >= 6,
        "expected to scan every module, scanned {checked}"
    );
}

#[test]
fn wrangler_keeps_the_unconfigured_worker_private_and_inert() {
    let wrangler = project_file("wrangler.toml");

    assert!(wrangler.contains("workers_dev = false"));
    assert!(wrangler.contains("preview_urls = false"));
    assert!(wrangler.contains("class_name = \"MaintainerDeliveryJournal\""));
    assert!(wrangler.contains("type = \"durable-object\""));
    assert!(wrangler.contains("storage = \"sqlite\""));
    for required_secret in [
        "DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET",
        "DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET_REVISION",
        "DARK_FACTORY_MAINTAINER_APP_ID",
        "DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8",
        "DARK_FACTORY_MAINTAINER_PERMISSION_REVISION",
        "DARK_FACTORY_MAINTAINER_REPOSITORY",
        "DARK_FACTORY_MAINTAINER_REPOSITORY_OWNER_ID",
        "DARK_FACTORY_MAINTAINER_REPOSITORY_ID",
        "DARK_FACTORY_MAINTAINER_OPERATOR_EMAIL_SHA256",
        "DARK_FACTORY_CLOUDFLARE_ACCESS_TEAM_DOMAIN",
        "DARK_FACTORY_CLOUDFLARE_ACCESS_AUD",
    ] {
        assert!(
            wrangler.contains(required_secret),
            "missing required deployment binding: {required_secret}"
        );
    }
    assert!(!wrangler.contains("route ="));
    assert!(!wrangler.contains("routes ="));
}

#[test]
fn durable_object_is_sharded_by_app_and_exact_replay_identity() {
    let journal = project_file("src/journal.rs");

    assert!(journal.contains("pub struct MaintainerDeliveryJournal"));
    assert!(journal.contains("DARK_FACTORY_MAINTAINER_DELIVERIES"));
    assert!(journal.contains("delivery_shard_name"));
    assert!(journal.contains("operation_shard_name"));
    assert!(journal.contains("app_id"));
    assert!(journal.contains("delivery_id"));
    assert!(journal.contains("operation_id"));
    assert!(journal.contains("0004_maintainer_operations.sql"));
    assert!(journal.contains("sha256"));
    assert!(!journal.contains("DATABASE_URL"));
    assert!(!journal.contains("neon_superuser"));
}

/// A tool's declared `outputSchema` and the Rust struct it serializes live in
/// two files with nothing tying them together. A rename in one alone produces
/// a surface that advertises a field it never sends -- which compiles, passes
/// every test, and is only visible to a caller validating the response. That
/// happened during this change and nothing caught it.
#[test]
fn declared_output_schemas_name_the_fields_the_results_carry() {
    let mcp = project_file("src/mcp.rs");
    let github_app = project_file("src/github_app.rs");

    // Scoped to each tool's OWN block, not to `mcp.rs` as a whole. A whole-file
    // `contains` is satisfied by the field's own `required` array, or by a
    // different tool's schema, so it passes while the property it is meant to
    // pin has been renamed away -- verified by mutation: renaming the
    // `verdict` property survived a whole-file check.
    let tool_block = |name: &str| -> String {
        let start = mcp
            .find(&format!(r#""name": "{name}""#))
            .unwrap_or_else(|| panic!("missing tool: {name}"));
        let rest = &mcp[start..];
        let end = rest[1..]
            .find(r#""name": ""#)
            .map_or(rest.len(), |offset| offset + 1);
        // The tool's OUTPUT schema only. Its `inputSchema` names some of the
        // same fields -- `head_sha` in particular -- so a whole-tool slice
        // passes while the output declaration has lost the field, which is
        // the drift this test exists to catch.
        let block = &rest[..end];
        let output = block
            .find(r#""outputSchema""#)
            .unwrap_or_else(|| panic!("{name} declares no outputSchema"));
        block[output..].to_string()
    };

    for (tool, struct_name, fields) in [
        (
            "enqueue_pull_request",
            "EnqueueResult",
            &["pull_number", "head_sha", "entry_id", "state_when_recorded"][..],
        ),
        (
            "submit_pull_request_review",
            "ReviewResult",
            // `verdict` especially: it is the field the required `review`
            // check reads, so a rename that missed the MCP schema would make
            // every recorded verdict invisible while every test stayed green.
            &["review_id", "url", "head_sha", "state", "verdict"][..],
        ),
        (
            "publish_commit",
            "CommitResult",
            &["branch", "commit_sha", "parent_sha"][..],
        ),
        (
            "create_pull_request",
            "PullRequestResult",
            &["number", "url", "head_sha"][..],
        ),
        (
            "maintainer_status",
            "RepositoryResult",
            &[
                "repository",
                "repository_id",
                "default_branch",
                "default_sha",
            ][..],
        ),
        ("create_issue", "IssueResult", &["number", "url"][..]),
        (
            "observe_pull_request_merge",
            "PullRequestMergeResult",
            &[
                "pull_number",
                "head_sha",
                "base",
                "pull_state",
                "state",
                "entry_id",
                "queue_state",
                "merge_commit_sha",
            ][..],
        ),
    ] {
        let start = github_app
            .find(&format!("struct {struct_name} {{"))
            .unwrap_or_else(|| panic!("missing struct: {struct_name}"));
        let body = &github_app[start..start + github_app[start..].find('}').unwrap()];
        let block = tool_block(tool);
        for field in fields {
            assert!(
                body.contains(&format!("{field}: ")),
                "{struct_name} does not carry {field}"
            );
            assert!(
                block.contains(&format!(r#""{field}": {{"#)),
                "{tool}'s outputSchema does not declare {field}"
            );
        }
    }

    let operation = tool_block("observe_operation");
    let journal = project_file("src/journal.rs");
    for field in ["kind", "request_digest", "state", "result_json"] {
        assert!(
            journal.contains(&format!("pub(crate) {field}: ")),
            "OperationObservation does not carry {field}"
        );
    }
    for field in ["operation_id", "kind", "request_digest", "state", "result"] {
        assert!(
            operation.contains(&format!(r#""{field}": {{"#)),
            "observe_operation's outputSchema does not declare {field}"
        );
    }
}

#[test]
fn mcp_surface_is_repository_bound_and_typed() {
    let mcp = project_file("src/mcp.rs");
    let access = project_file("src/access.rs");

    for tool in [
        "maintainer_status",
        "observe_operation",
        "create_issue",
        "observe_issue",
        "resolve_issue",
        "publish_release_tag",
        "recover_release",
        "observe_release",
        "observe_release_workflow",
        "dispatch_control_plane_deploy",
        "observe_control_plane_deploy",
        "create_pull_request",
        "submit_pull_request_review",
        "observe_pull_request_checks",
        "observe_pull_request_workflows",
        "read_pull_request_job_log",
        "rerun_failed_pull_request_jobs",
        "observe_pull_request_merge",
        "publish_commit",
        "enqueue_pull_request",
    ] {
        assert!(mcp.contains(tool), "missing typed MCP tool: {tool}");
    }
    for forbidden in ["generic_request", "graphql", "shell", "access_token"] {
        assert!(
            !mcp.contains(forbidden),
            "generic authority leaked: {forbidden}"
        );
    }
    assert!(access.contains("cf-access-jwt-assertion"));
    assert!(access.contains("cf-access-authenticated-user-email"));
    assert!(access.contains("expected_email_digest"));
    assert!(access.contains("cdn-cgi/access/certs"));
    assert!(access.contains("verify_rs256"));
    assert!(access.contains("claims.audience"));
    assert!(access.contains("claims.issuer"));
}

/// The deployment gate rolls back unless the live `/readyz` body carries the
/// exact label the Worker emits. They live in two files, so a rename that
/// touches only one strands production on a rollback loop — or, worse, passes
/// against a label that no longer means what the gate thinks it does.
#[test]
fn the_deployment_gate_asserts_the_readiness_label_the_worker_emits() {
    let lib = project_file("src/lib.rs");
    let workflow = std::fs::read_to_string(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../.github/workflows/deploy-control-plane.yml"),
    )
    .unwrap();

    let headless = r#""maintainer_operations":"mcp_repository_bound_operator_and_headless""#;
    assert!(lib.contains(headless));
    assert!(lib.contains(r#""maintainer_operations":"mcp_repository_bound_operator_only""#));
    // The gate must require the headless variant specifically: the binding
    // behind it is optional and inherited across versions, so this is the only
    // check that catches a deployment which silently lost it.
    assert!(workflow.contains(headless));
    assert!(!workflow.contains("mcp_repository_bound_operator_only"));
    assert!(!lib.contains("mcp_six_tools"));
    assert!(!workflow.contains("mcp_six_tools"));
}

/// The enqueue and publish paths are `wasm32`-only, so a host test cannot
/// drive them. What it can do is hold the contract they were got wrong on:
/// GitHub's refusals must reach a determinate answer rather than being folded
/// into "unavailable" and reported as an outcome nobody knows.
#[test]
fn github_refusals_stay_determinate() {
    let github_app = project_file("src/github_app.rs");
    let mcp = project_file("src/mcp.rs");
    let journal = project_file("src/journal.rs");

    // The transport reports the status; it is the only place that sees one.
    assert!(github_app.contains("Err(Error::Rejected(response.status_code()))"));
    // A missing branch is a 404 and nothing else.
    assert!(github_app.contains("Err(Error::Rejected(404))"));
    // GraphQL answers 200 with an `errors` array, so a status check alone
    // reads a refused mutation as a success.
    //
    // The transport hands the classification back rather than deciding: a
    // mutation and a read need opposite answers to the same response. Only
    // error classes GitHub rejects before execution are `Rejected`; an untyped
    // error may follow a server-side timeout on work already under way, so it
    // is `Unknown` and fails safe.
    assert!(github_app.contains("enum GraphQlFailure"));
    // The table itself is exercised by
    // `github_app::tests::only_pre_execution_error_classes_are_rejections`.
    assert!(github_app.contains("fn classify_graphql_errors("));
    // The classification is asked at the PAYLOAD, not the envelope. A field
    // error nulls its field and leaves `data` an object, so GitHub's ordinary
    // refusal shape is a populated `data` with a null mutation field beside an
    // errors array -- an envelope-level early return routes every real refusal
    // past the classification.
    assert!(github_app.contains("Ok((envelope.data, classify_graphql_errors(&envelope.errors)))"));
    // The decision itself is exercised by
    // `github_app::tests::an_enqueue_outcome_is_decided_from_the_payload_not_the_envelope`,
    // which is a real test rather than a grep. What is asserted here is only
    // that the transport still hands both halves back, so that decision keeps
    // getting the inputs it needs.
    assert!(github_app.contains("fn enqueue_outcome("));
    // Errors are logged whatever `data` carried.
    assert!(github_app.contains("for error in &envelope.errors {"));
    // A failed read is never reported as a refusal of the operation it was
    // reconciling.
    assert!(github_app.contains("if failure.is_some() {"));
    // The boundary requires every permission the operations mint, so a missing
    // grant fails at `/readyz` rather than at token mint.
    assert!(
        github_app
            .contains(r#"permission_at_least(&installation.permissions, "merge_queues", "write")"#)
    );
    assert!(
        github_app.contains(r#"permission_at_least(&installation.permissions, "issues", "write")"#)
    );
    assert!(
        github_app
            .contains(r#"permission_at_least(&installation.permissions, "actions", "write")"#)
    );
    // A branch with no queue fails closed instead of falling back to a merge.
    assert!(!github_app.contains("/merge\""));
    assert!(!mcp.contains("merge_pull_request_at_head"));
    // GitHub owns atomic post-merge cleanup. There is no unsafe read-then-delete
    // ref mutation in this surface.
    assert!(github_app.contains("delete_branch_on_merge"));
    assert!(github_app.contains("require_branch_cleanup(&repository)?;"));
    assert!(!github_app.contains("delete_generated_branch"));
    // Publication, PR creation, and enqueue derive the default branch from
    // GitHub rather than trusting a caller-supplied branch name.
    assert!(github_app.contains("request.base != repository.default_branch"));
    assert!(github_app.contains("request.branch == repository.default_branch"));
    // A refusal releases the claim so the same operation ID stays retryable.
    assert!(github_app.contains("OperationTransition::Refused"));
    assert!(github_app.contains("RefusalReason::AlreadyQueued"));
    // Reconciliation rematerializes the content-addressed request tree; a
    // copied marker and parent cannot cause a different tree to be adopted.
    assert!(github_app.contains("head.tree.sha != self.materialize_tree(token, request).await?"));
    // These fixed REST contracts are exact rather than generic 2xx guesses.
    assert!(github_app.contains("request.promote.to_string()"));
    assert!(github_app.contains("token.as_str(),\n            201,"));
    assert!(journal.contains(r#"Ok(("planned", None, "'executing','indeterminate'"))"#));
    // And the caller is told which of the two it got.
    assert!(mcp.contains(r#""refused""#));
    assert!(mcp.contains(r#""indeterminate""#));
}
