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

/// Workers' `Request` constructor accepts only `follow` and `manual` and throws on
/// `error`, so `RequestRedirect::Error` means the request is never built and the call
/// fails closed with no diagnosis. Shipping it once left `/readyz` permanently 503 and
/// every authenticated MCP call 401, and neither the unit tests nor the `workerd`
/// integration test could reach the affected paths. Assert it over the source instead.
#[test]
fn no_outbound_request_uses_a_redirect_mode_workers_rejects() {
    for source in [
        "src/access.rs",
        "src/github_app.rs",
        "src/journal.rs",
        "src/lib.rs",
        "src/maintainer.rs",
        "src/mcp.rs",
    ] {
        assert!(
            !project_file(source).contains("RequestRedirect::Error"),
            "{source} builds an outbound request Workers will refuse to construct"
        );
    }
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
    assert!(journal.contains("0002_maintainer_operations.sql"));
    assert!(journal.contains("sha256"));
    assert!(!journal.contains("DATABASE_URL"));
    assert!(!journal.contains("neon_superuser"));
}

#[test]
fn mcp_surface_is_repository_bound_and_typed() {
    let mcp = project_file("src/mcp.rs");
    let access = project_file("src/access.rs");

    for tool in [
        "maintainer_status",
        "create_pull_request",
        "submit_pull_request_review",
        "observe_pull_request_checks",
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
