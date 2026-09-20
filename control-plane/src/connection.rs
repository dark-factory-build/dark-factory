//! GitHub user authorization inside the existing Maintainer. No GitHub
//! credential leaves the broker, and installation visibility is never a grant.
#![cfg_attr(test, allow(dead_code))]
use serde::{Deserialize, Serialize};
use sha2::{Digest as _, Sha256};
use zeroize::Zeroize as _;

pub(crate) const PREFIX: &str = "/v1/github/connections";
const CLIENT_ID: &str = "DARK_FACTORY_MAINTAINER_CLIENT_ID";
const CLIENT_SECRET: &str = "DARK_FACTORY_MAINTAINER_CLIENT_SECRET";
const CALLBACK: &str = "DARK_FACTORY_MAINTAINER_CALLBACK_URL";
const SESSION_KEY: &str = "github_connection_v1";

fn digest(value: &str) -> String {
    hex::encode(Sha256::digest(value.as_bytes()))
}
fn valid_id(value: &str) -> bool {
    value.len() == 64
        && value
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

#[derive(Deserialize, Serialize)]
struct Connection {
    id: String,
    pending: Option<Pending>,
    #[serde(default)]
    confirmation: Option<Confirmation>,
    tokens: Option<Tokens>,
    user: Option<User>,
    repositories: Vec<Delegation>,
}
#[derive(Deserialize, Serialize)]
struct Confirmation {
    code_digest: String,
    expires_at: u64,
    attempts: u8,
}
impl Confirmation {
    fn expired(&self, now: u64) -> bool {
        self.expires_at <= now || self.attempts >= 5
    }
}
#[derive(Deserialize, Serialize)]
struct Pending {
    state_digest: String,
    verifier: String,
    expires_at: u64,
}
impl Drop for Pending {
    fn drop(&mut self) {
        self.verifier.zeroize();
    }
}
#[derive(Deserialize, Serialize)]
struct Tokens {
    access_token: String,
    refresh_token: String,
    expires_in: u64,
    refresh_token_expires_in: u64,
    token_type: String,
    #[serde(default)]
    issued_at: u64,
}
impl Drop for Tokens {
    fn drop(&mut self) {
        self.access_token.zeroize();
        self.refresh_token.zeroize();
    }
}
impl Tokens {
    fn validate(&self) -> Result<(), ()> {
        if self.token_type != "bearer"
            || self.access_token.is_empty()
            || self.refresh_token.is_empty()
            || self.expires_in == 0
            || self.expires_in > 86_400
            || self.refresh_token_expires_in == 0
            || self.refresh_token_expires_in > 31_622_400
        {
            return Err(());
        }
        Ok(())
    }
}
#[derive(Clone, Deserialize, Serialize)]
struct User {
    id: i64,
    login: String,
    #[serde(default, rename = "type")]
    account_type: String,
}
#[derive(Clone, Deserialize, Serialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
struct Delegation {
    installation_id: i64,
    repository_id: i64,
    repository: String,
}
#[derive(Deserialize, Serialize)]
struct Permissions {
    #[serde(default)]
    pull: bool,
    #[serde(default)]
    push: bool,
    #[serde(default)]
    maintain: bool,
    #[serde(default)]
    admin: bool,
}
#[derive(Deserialize, Serialize)]
struct Repository {
    id: i64,
    full_name: String,
    permissions: Permissions,
}
impl Repository {
    fn allows(&self, delegated: &Delegation, write: bool) -> bool {
        self.id == delegated.repository_id
            && self.full_name.eq_ignore_ascii_case(&delegated.repository)
            && self.permissions.pull
            && (!write
                || self.permissions.push
                || self.permissions.maintain
                || self.permissions.admin)
    }
}
#[derive(Deserialize, Serialize)]
struct Installation {
    id: i64,
    app_id: i64,
    repository_selection: String,
    suspended_at: Option<String>,
    account: User,
    #[serde(default)]
    html_url: String,
}
impl Installation {
    fn active(&self, app_id: i64) -> bool {
        self.app_id == app_id
            && self.id > 0
            && self.repository_selection == "selected"
            && self.suspended_at.is_none()
    }
}

// Enumeration fails closed when a new tool is introduced without a grant rule.
fn tool_write(name: &str) -> Option<bool> {
    match name {
        "maintainer_status"
        | "observe_operation"
        | "observe_file"
        | "observe_tree"
        | "observe_ref"
        | "observe_issue"
        | "list_issues"
        | "list_pull_requests"
        | "observe_pull_request_review"
        | "observe_release"
        | "observe_release_workflow"
        | "observe_control_plane_deploy"
        | "observe_pull_request_checks"
        | "observe_pull_request_workflows"
        | "read_pull_request_job_log"
        | "observe_pull_request_merge" => Some(false),
        "create_issue"
        | "resolve_issue"
        | "create_pull_request"
        | "update_pull_request_body"
        | "close_pull_request"
        | "submit_pull_request_review"
        | "publish_commit"
        | "publish_release_tag"
        | "recover_release"
        | "dispatch_control_plane_deploy"
        | "enqueue_pull_request"
        | "merge_pull_request_at_head"
        | "rerun_failed_pull_request_jobs" => Some(true),
        _ => None,
    }
}

#[cfg(target_arch = "wasm32")]
pub(crate) use cloudflare::{configured, durable, receive};
#[cfg(target_arch = "wasm32")]
mod cloudflare {
    use super::*;
    use crate::{
        BrokerState,
        github_app::{Error as GitHubError, RepositoryName, github_json, read_github_response},
    };
    use axum::{
        body::Bytes,
        extract::State,
        http::{HeaderMap, Method as HttpMethod, Uri},
        response::{IntoResponse as _, Response as HttpResponse},
    };
    use base64::{Engine as _, engine::general_purpose::URL_SAFE_NO_PAD};
    use serde_json::{Value, json};
    use std::collections::BTreeMap;
    use worker::{Env, Method, Request, RequestInit, Response, Storage, Url};

    struct OAuth {
        client_id: String,
        client_secret: String,
        callback: String,
        app_id: i64,
    }
    impl Drop for OAuth {
        fn drop(&mut self) {
            self.client_secret.zeroize();
        }
    }
    impl OAuth {
        fn load(env: &Env) -> Result<Self, ()> {
            let read = |key| env.secret(key).map(|s| s.to_string()).map_err(|_| ());
            let client_id = read(CLIENT_ID)?;
            let client_secret = read(CLIENT_SECRET)?;
            let callback = read(CALLBACK)?;
            let url = Url::parse(&callback).map_err(|_| ())?;
            if client_id.is_empty()
                || client_id.len() > 128
                || client_secret.len() < 20
                || url.scheme() != "https"
                || url.host_str().is_none()
                || !url.username().is_empty()
                || url.password().is_some()
                || url.query().is_some()
                || url.fragment().is_some()
                || url.path() != format!("{PREFIX}/callback")
            {
                return Err(());
            }
            let app_id = read(crate::APP_ID_BINDING)?.parse().map_err(|_| ())?;
            Ok(Self {
                client_id,
                client_secret,
                callback,
                app_id,
            })
        }
        async fn exchange(&self, fields: Value) -> Result<Tokens, GitHubError> {
            let mut body = fields;
            body["client_id"] = json!(self.client_id);
            body["client_secret"] = json!(self.client_secret);
            let headers = worker::Headers::new();
            headers
                .set("accept", "application/json")
                .map_err(|_| GitHubError::Unavailable)?;
            headers
                .set("content-type", "application/json")
                .map_err(|_| GitHubError::Unavailable)?;
            let serialized = zeroize::Zeroizing::new(
                serde_json::to_string(&body).map_err(|_| GitHubError::Unavailable)?,
            );
            let mut init = RequestInit::new();
            init.with_method(Method::Post)
                .with_redirect(worker::RequestRedirect::Manual)
                .with_headers(headers)
                .with_body(Some(serialized.as_str().into()));
            let url = "https://github.com/login/oauth/access_token";
            let request =
                Request::new_with_init(url, &init).map_err(|_| GitHubError::Unavailable)?;
            let response = worker::Fetch::Request(request)
                .send()
                .await
                .map_err(|_| GitHubError::Unavailable)?;
            let bytes = zeroize::Zeroizing::new(read_github_response(response, url, 16384).await?);
            let payload: Value =
                serde_json::from_slice(&bytes).map_err(|_| GitHubError::Unavailable)?;
            if let Some(error) = payload.get("error").and_then(Value::as_str) {
                return Err(
                    if matches!(
                        error,
                        "bad_verification_code"
                            | "bad_refresh_token"
                            | "expired_token"
                            | "invalid_grant"
                    ) {
                        GitHubError::Rejected(401)
                    } else {
                        GitHubError::Unavailable
                    },
                );
            }
            let mut tokens: Tokens =
                serde_json::from_slice(&bytes).map_err(|_| GitHubError::Unavailable)?;
            tokens.validate().map_err(|_| GitHubError::Unavailable)?;
            tokens.issued_at = now();
            Ok(tokens)
        }
    }
    pub(crate) fn configured(env: &Env) -> bool {
        OAuth::load(env).is_ok()
    }
    fn now() -> u64 {
        (js_sys::Date::now() / 1000.0) as u64
    }
    fn random() -> Result<String, ()> {
        use wasm_bindgen::JsCast as _;
        let crypto = js_sys::Reflect::get(&js_sys::global(), &"crypto".into()).map_err(|_| ())?;
        let random: js_sys::Function = js_sys::Reflect::get(&crypto, &"getRandomValues".into())
            .map_err(|_| ())?
            .dyn_into()
            .map_err(|_| ())?;
        let bytes = js_sys::Uint8Array::new_with_length(32);
        random.call1(&crypto, &bytes).map_err(|_| ())?;
        Ok(hex::encode(bytes.to_vec()))
    }
    fn reply(value: Value, status: u16) -> worker::Result<Response> {
        let mut response = Response::from_json(&value)?.with_status(status);
        response.headers_mut().set("cache-control", "no-store")?;
        response
            .headers_mut()
            .set("referrer-policy", "no-referrer")?;
        Ok(response)
    }
    fn denied() -> worker::Result<Response> {
        reply(json!({"error":"unauthorized"}), 401)
    }
    fn github_failure(error: GitHubError) -> worker::Result<Response> {
        match error {
            GitHubError::Rejected(401 | 404) => denied(),
            // GitHub also uses 403 for rate limits. Without the response
            // headers it cannot prove revocation; retain the connection.
            _ => reply(json!({"error":"github_unavailable"}), 503),
        }
    }

    // Only the route dispatcher can address these DOs. All public requests are
    // reauthenticated inside the DO; no caller-supplied principal is trusted.
    #[worker::send]
    pub(crate) async fn receive(
        State(state): State<BrokerState>,
        method: HttpMethod,
        mut uri: Uri,
        headers: HeaderMap,
        body: Bytes,
    ) -> HttpResponse {
        let Some(env) = state.connection_env.as_ref() else {
            return axum::http::StatusCode::NOT_FOUND.into_response();
        };
        // Keep this administrative route outside the customer OAuth prefix,
        // so its existing Access policy still supplies a verified assertion.
        let legacy_prefix = "/v1/github/maintainer/connections/";
        let legacy_migration = uri.path().starts_with(legacy_prefix);
        if legacy_migration {
            let Some(mcp) = state.mcp.as_ref() else {
                return axum::http::StatusCode::UNAUTHORIZED.into_response();
            };
            if !mcp.authorize_legacy_migration(&headers).await {
                return axum::http::StatusCode::UNAUTHORIZED.into_response();
            }
            if uri.query().is_some() {
                return axum::http::StatusCode::BAD_REQUEST.into_response();
            }
            let path = uri.path().strip_prefix(legacy_prefix).unwrap_or("");
            let Ok(internal) = format!("{PREFIX}/{path}").parse::<Uri>() else {
                return axum::http::StatusCode::BAD_REQUEST.into_response();
            };
            uri = internal;
        }
        match forward(env, method, uri, headers, body, legacy_migration).await {
            Ok(response) => response.into(),
            Err(_) => (
                axum::http::StatusCode::SERVICE_UNAVAILABLE,
                [("cache-control", "no-store")],
                "connection unavailable",
            )
                .into_response(),
        }
    }
    async fn forward(
        env: &Env,
        method: HttpMethod,
        uri: Uri,
        headers: HeaderMap,
        body: Bytes,
        legacy_migration: bool,
    ) -> worker::Result<Response> {
        let oauth = OAuth::load(env).map_err(|_| worker::Error::RustError("inactive".into()))?;
        let path = uri.path();
        let path_query = uri.path_and_query().map(|p| p.as_str()).unwrap_or("/");
        let is_start = path == PREFIX && method == HttpMethod::POST;
        let mut credential = None;
        let id = if is_start {
            if !body.is_empty() && body.as_ref() != b"{}" {
                return reply(json!({"error":"invalid_request"}), 400);
            }
            let secret =
                random().map_err(|_| worker::Error::RustError("random unavailable".into()))?;
            let id = digest(&secret);
            credential = Some(zeroize::Zeroizing::new(secret));
            id
        } else if path == format!("{PREFIX}/callback") && method == HttpMethod::GET {
            let url = Url::parse(&format!("https://connection.internal{path_query}"))?;
            let states: Vec<_> = url.query_pairs().filter(|(k, _)| k == "state").collect();
            let Some((_, state)) = states.first().filter(|_| states.len() == 1) else {
                return denied();
            };
            state.split('.').next().unwrap_or("").to_owned()
        } else {
            path.strip_prefix(&format!("{PREFIX}/"))
                .unwrap_or("")
                .split('/')
                .next()
                .unwrap_or("")
                .to_owned()
        };
        if !valid_id(&id) {
            return denied();
        }
        let namespace = env.durable_object(crate::journal::NAMESPACE_BINDING)?;
        let stub =
            namespace.get_by_name(&format!("maintainer:{}:connection:{id}", oauth.app_id))?;
        let mut init = RequestInit::new();
        init.with_method(Method::from(method.as_str().to_owned()))
            .with_body((!body.is_empty()).then(|| body.to_vec().into()));
        let forwarded = worker::Headers::new();
        let authorizations: Vec<_> = headers.get_all("authorization").iter().collect();
        if authorizations.len() == 1 {
            if let Ok(value) = authorizations[0].to_str() {
                forwarded.set("authorization", value)?;
            }
        }
        if is_start {
            forwarded.set("x-connection-id", &id)?;
        }
        // This marker is created only after Access verification; arbitrary
        // incoming headers are never forwarded to this internal authority.
        if legacy_migration {
            forwarded.set("x-legacy-receipt-migration", "verified")?;
        }
        init.with_headers(forwarded);
        let request =
            Request::new_with_init(&format!("https://connection.internal{path_query}"), &init)?;
        let mut response = stub.fetch_with_request(request).await?;
        if let Some(secret) = credential {
            if response.status_code() == 200 {
                let mut result: Value = response.json().await?;
                result["credential"] = json!(secret.as_str());
                return reply(result, 201);
            }
        }
        Ok(response)
    }

    pub(crate) async fn durable(
        storage: &Storage,
        env: &Env,
        mut request: Request,
    ) -> worker::Result<Response> {
        let oauth = match OAuth::load(env) {
            Ok(v) => v,
            Err(_) => return denied(),
        };
        let path = request.path();
        if path == PREFIX && request.method() == Method::Post {
            if storage.get::<Connection>(SESSION_KEY).await?.is_some() {
                return denied();
            }
            let Some(id) = request
                .headers()
                .get("x-connection-id")?
                .filter(|id| valid_id(id))
            else {
                return denied();
            };
            let nonce =
                random().map_err(|_| worker::Error::RustError("random unavailable".into()))?;
            let state = format!("{id}.{nonce}");
            let verifier =
                random().map_err(|_| worker::Error::RustError("random unavailable".into()))?;
            let challenge = URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()));
            let expires_at = now() + 600;
            let mut url = Url::parse("https://github.com/login/oauth/authorize")?;
            url.query_pairs_mut()
                .append_pair("client_id", &oauth.client_id)
                .append_pair("redirect_uri", &oauth.callback)
                .append_pair("state", &state)
                .append_pair("code_challenge", &challenge)
                .append_pair("code_challenge_method", "S256");
            let connection = Connection {
                id: id.clone(),
                pending: Some(Pending {
                    state_digest: digest(&state),
                    verifier,
                    expires_at,
                }),
                confirmation: None,
                tokens: None,
                user: None,
                repositories: vec![],
            };
            storage.put(SESSION_KEY, &connection).await?;
            return reply(
                json!({"connection_id":id,"authorization_url":url.as_str(),"expires_at":expires_at}),
                200,
            );
        }
        let Some(mut connection) = storage.get::<Connection>(SESSION_KEY).await? else {
            return denied();
        };
        if path == format!("{PREFIX}/callback") && request.method() == Method::Get {
            let url = request.url()?;
            let pairs: Vec<_> = url.query_pairs().collect();
            let get = |key| {
                let found: Vec<_> = pairs.iter().filter(|(k, _)| k == key).collect();
                if found.len() == 1 {
                    Some(found[0].1.to_string())
                } else {
                    None
                }
            };
            let (Some(state), Some(code)) = (get("state"), get("code")) else {
                return denied();
            };
            let Some(pending) = connection.pending.take() else {
                return denied();
            };
            if pending.expires_at <= now() || pending.state_digest != digest(&state) {
                return denied();
            }
            // Consume before network I/O. Failed exchange requires a fresh flow.
            storage.put(SESSION_KEY, &connection).await?;
            let tokens = match oauth.exchange(json!({"code":code,"redirect_uri":oauth.callback,"code_verifier":pending.verifier})).await { Ok(tokens) => tokens, Err(error) => return github_failure(error) };
            let user: User =
                match github_json("https://api.github.com/user", &tokens.access_token).await {
                    Ok(user) => user,
                    Err(error) => return github_failure(error),
                };
            if user.id <= 0 {
                return denied();
            }
            let code = random()
                .map_err(|_| worker::Error::RustError("random unavailable".into()))?[..10]
                .to_ascii_uppercase();
            connection.confirmation = Some(Confirmation {
                code_digest: digest(&code),
                expires_at: now() + 600,
                attempts: 0,
            });
            connection.user = Some(user);
            connection.tokens = Some(tokens);
            storage.put(SESSION_KEY, &connection).await?;
            let mut response = Response::ok(format!(
                "Confirmation code: {code}\n\nEnter this code only in the Dark Factory console or CLI on the host where you initiated this connection. Do not send this code to another person. The code expires in 10 minutes."
            ))?;
            response.headers_mut().set("cache-control", "no-store")?;
            response
                .headers_mut()
                .set("referrer-policy", "no-referrer")?;
            return Ok(response);
        }
        let bearer = request.headers().get("authorization")?.unwrap_or_default();
        let Some(secret) = bearer.strip_prefix("Bearer ").filter(|s| valid_id(s)) else {
            return denied();
        };
        if digest(secret) != connection.id {
            return denied();
        }
        let base = format!("{PREFIX}/{}", connection.id);
        if path == base && request.method() == Method::Delete {
            connection.tokens = None;
            connection.pending = None;
            connection.confirmation = None;
            connection.user = None;
            connection.repositories.clear();
            storage.put(SESSION_KEY, &connection).await?;
            return reply(json!({"state":"disconnected"}), 200);
        }
        if let Some(confirmation) = connection.confirmation.as_mut() {
            if confirmation.expired(now()) {
                connection.confirmation = None;
                connection.tokens = None;
                connection.user = None;
                storage.put(SESSION_KEY, &connection).await?;
                return denied();
            }
            if path == format!("{base}/confirm") && request.method() == Method::Post {
                #[derive(Deserialize)]
                #[serde(deny_unknown_fields)]
                struct Confirm {
                    code: String,
                }
                let supplied = request.json::<Confirm>().await.ok();
                confirmation.attempts += 1;
                if supplied.is_none_or(|value| digest(&value.code) != confirmation.code_digest) {
                    storage.put(SESSION_KEY, &connection).await?;
                    return denied();
                }
                if let Err(error) = refresh_and_verify(storage, &oauth, &mut connection).await {
                    return github_failure(error);
                }
                connection.confirmation = None;
                storage.put(SESSION_KEY, &connection).await?;
                return reply(json!({"state":"connected"}), 200);
            }
            if path == base && request.method() == Method::Get {
                return reply(
                    json!({"state":"awaiting_confirmation","connection_id":connection.id,"repositories":[]}),
                    200,
                );
            }
            return denied();
        }
        if connection.tokens.is_none() {
            if path == base && request.method() == Method::Get {
                return reply(
                    json!({"state":if connection.pending.as_ref().is_some_and(|p| p.expires_at > now()) {"pending"} else {"disconnected"},"connection_id":connection.id,"repositories":[]}),
                    200,
                );
            }
            return denied();
        }
        if let Err(error) = refresh_and_verify(storage, &oauth, &mut connection).await {
            return github_failure(error);
        }
        let token = &connection
            .tokens
            .as_ref()
            .ok_or_else(|| worker::Error::RustError("unauthorized".into()))?
            .access_token;
        if path == format!("{base}/legacy-receipt") && request.method() == Method::Post {
            if request
                .headers()
                .get("x-legacy-receipt-migration")?
                .as_deref()
                != Some("verified")
            {
                return denied();
            }
            #[derive(Deserialize)]
            #[serde(deny_unknown_fields)]
            struct Proof {
                kind: String,
                arguments: Value,
            }
            let Ok(proof) = request.json::<Proof>().await else {
                return reply(json!({"error":"invalid_request"}), 400);
            };
            let source = proof
                .arguments
                .get("source_repository")
                .and_then(Value::as_str)
                .map(str::to_owned);
            let Ok((operation, repository)) =
                crate::github_app::legacy_receipt_request(&proof.kind, proof.arguments)
            else {
                return reply(json!({"error":"invalid_request"}), 400);
            };
            let Some(delegated) = connection
                .repositories
                .iter()
                .find(|d| d.repository.eq_ignore_ascii_case(&repository))
            else {
                return denied();
            };
            if let Err(error) = authorize(token, oauth.app_id, delegated, true).await {
                return github_failure(error);
            }
            if let Some(source) = source {
                let Some(source) = connection
                    .repositories
                    .iter()
                    .find(|d| d.repository.eq_ignore_ascii_case(&source))
                else {
                    return denied();
                };
                if let Err(error) = authorize(token, oauth.app_id, source, false).await {
                    return github_failure(error);
                }
            }
            let state = BrokerState::from_worker_env(env)
                .map_err(|_| worker::Error::RustError("inactive".into()))?;
            let Some(mcp) = state.mcp else {
                return denied();
            };
            return match mcp
                .transfer_legacy_receipt(&connection.id, delegated.repository_id, &operation)
                .await
            {
                Ok(true) => reply(
                    json!({"transferred":true,"operation_id":operation.operation_id}),
                    200,
                ),
                Ok(false) => reply(json!({"error":"receipt_proof_conflict"}), 409),
                Err(_) => reply(json!({"error":"journal_unavailable"}), 503),
            };
        }
        if path == base && request.method() == Method::Get {
            // Status contains private delegation metadata, so access loss is
            // checked here too, not only before repository operations.
            for delegated in &connection.repositories {
                if let Err(error) = authorize(token, oauth.app_id, delegated, false).await {
                    return github_failure(error);
                }
            }
            return reply(
                json!({"state":"connected","connection_id":connection.id,"github_user":connection.user,"repositories":connection.repositories}),
                200,
            );
        }
        if request.method() == Method::Get
            && (path == format!("{base}/installations") || path == format!("{base}/repositories"))
        {
            let url = request.url()?;
            let pairs: Vec<_> = url.query_pairs().collect();
            let query: BTreeMap<_, _> = pairs
                .iter()
                .map(|(k, v)| (k.as_ref(), v.as_ref()))
                .collect();
            if pairs.len() != query.len()
                || query
                    .keys()
                    .any(|key| !matches!(*key, "page" | "installation_id"))
            {
                return reply(json!({"error":"invalid_query"}), 400);
            }
            let page = match query.get("page") {
                Some(value) => match value.parse::<u32>() {
                    Ok(page) => page,
                    Err(_) => return reply(json!({"error":"invalid_page"}), 400),
                },
                None => 1,
            };
            if !(1..=1000).contains(&page) {
                return reply(json!({"error":"invalid_page"}), 400);
            }
            if path.ends_with("/installations") {
                let state = BrokerState::from_worker_env(env)
                    .map_err(|_| worker::Error::RustError("inactive".into()))?;
                let Some(mcp) = state.mcp else {
                    return denied();
                };
                let installation_url = match mcp.installation_url().await {
                    Ok(url) => url,
                    // App identity failure is broker unavailability, not loss
                    // of the authenticated customer's GitHub authorization.
                    Err(_) => return github_failure(GitHubError::Unavailable),
                };
                let response: InstallationPage = match github_json(
                    &format!("https://api.github.com/user/installations?per_page=100&page={page}"),
                    token,
                )
                .await
                {
                    Ok(v) => v,
                    Err(error) => return github_failure(error),
                };
                if page == 1000 && response.installations.len() == 100 {
                    return reply(json!({"error":"pagination_limit"}), 503);
                }
                let next_page = (response.installations.len() == 100).then_some(page + 1);
                return reply(
                    json!({"installations":response.installations.into_iter().filter(|i| i.app_id == oauth.app_id && i.id > 0).map(|i| {
                        let eligibility = if i.suspended_at.is_some() { "suspended" } else if i.repository_selection != "selected" { "all_repositories_unsupported" } else { "available" };
                        let mut value = serde_json::to_value(&i).expect("installation serialization");
                        value["eligibility"] = json!(eligibility);
                        value
                    }).collect::<Vec<_>>(),"next_page":next_page,"installation_url":installation_url}),
                    200,
                );
            }
            let Some(installation_id) = query
                .get("installation_id")
                .and_then(|s| s.parse::<i64>().ok())
                .filter(|i| *i > 0)
            else {
                return reply(json!({"error":"invalid_installation"}), 400);
            };
            if let Err(error) = installation(token, oauth.app_id, installation_id).await {
                return github_failure(error);
            }
            let response: RepositoryPage = match github_json(&format!("https://api.github.com/user/installations/{installation_id}/repositories?per_page=100&page={page}"),token).await { Ok(v) => v, Err(error) => return github_failure(error) };
            if page == 1000 && response.repositories.len() == 100 {
                return reply(json!({"error":"pagination_limit"}), 503);
            }
            let next_page = (response.repositories.len() == 100).then_some(page + 1);
            return reply(
                json!({"repositories":response.repositories,"next_page":next_page}),
                200,
            );
        }
        if path == format!("{base}/repositories") && request.method() == Method::Put {
            #[derive(Deserialize)]
            #[serde(deny_unknown_fields)]
            struct Selection {
                repositories: Vec<Delegation>,
            }
            let Ok(mut selection) = request.json::<Selection>().await else {
                return reply(json!({"error":"invalid_selection"}), 400);
            };
            if selection.repositories.len() > 100 {
                return reply(json!({"error":"invalid_selection"}), 400);
            }
            let mut ids = std::collections::BTreeSet::new();
            for delegated in &mut selection.repositories {
                if RepositoryName::requested(&mut delegated.repository).is_err()
                    || !ids.insert(delegated.repository_id)
                {
                    return denied();
                }
                if let Err(error) = authorize(token, oauth.app_id, delegated, false).await {
                    return github_failure(error);
                }
            }
            connection.repositories = selection.repositories;
            storage.put(SESSION_KEY, &connection).await?;
            return reply(json!({"repositories":connection.repositories}), 200);
        }
        if path == format!("{base}/mcp") && request.method() == Method::Post {
            let Ok(mut rpc) = request.json::<Value>().await else {
                return reply(json!({"error":"invalid_request"}), 400);
            };
            let method = rpc.get("method").and_then(Value::as_str).unwrap_or("");
            let mut repository = None;
            let mut grants = BTreeMap::new();
            if method == "tools/call" {
                let name = rpc
                    .pointer("/params/name")
                    .and_then(Value::as_str)
                    .unwrap_or("");
                let Some(write) = tool_write(name) else {
                    return denied();
                };
                let Some(repo) = rpc
                    .pointer("/params/arguments/repository")
                    .and_then(Value::as_str)
                else {
                    return denied();
                };
                let Some(delegated) = connection
                    .repositories
                    .iter()
                    .find(|d| d.repository.eq_ignore_ascii_case(repo))
                else {
                    return denied();
                };
                if let Err(error) = authorize(token, oauth.app_id, delegated, write).await {
                    return github_failure(error);
                }
                grants.insert(
                    delegated.repository.to_ascii_lowercase(),
                    (delegated.installation_id, delegated.repository_id),
                );
                if let Some(source) = rpc.pointer("/params/arguments/source_repository") {
                    let Some(source) = source.as_str() else {
                        return denied();
                    };
                    let Some(delegated_source) = connection
                        .repositories
                        .iter()
                        .find(|d| d.repository.eq_ignore_ascii_case(source))
                    else {
                        return denied();
                    };
                    if let Err(error) =
                        authorize(token, oauth.app_id, delegated_source, false).await
                    {
                        return github_failure(error);
                    }
                    grants.insert(
                        delegated_source.repository.to_ascii_lowercase(),
                        (
                            delegated_source.installation_id,
                            delegated_source.repository_id,
                        ),
                    );
                }
                repository = Some(format!("github:{}", delegated.repository_id));
                // Legacy observation's request shape remains unchanged.
                if name == "observe_operation" {
                    rpc.pointer_mut("/params/arguments")
                        .and_then(Value::as_object_mut)
                        .map(|args| args.remove("repository"));
                }
            }
            let state = BrokerState::from_worker_env(env)
                .map_err(|_| worker::Error::RustError("inactive".into()))?;
            let Some(mcp) = state.mcp else {
                return denied();
            };
            let Some(user) = connection.user.as_ref() else {
                return denied();
            };
            let Ok(author) = crate::github_app::GitAuthor::from_github(user.id, &user.login) else {
                return denied();
            };
            let response = crate::mcp::connection_dispatch(
                rpc,
                &mcp,
                &connection.id,
                repository.as_deref(),
                grants,
                author,
            )
            .await;
            return response.try_into();
        }
        reply(json!({"error":"not_found"}), 404)
    }
    async fn refresh_and_verify(
        storage: &Storage,
        oauth: &OAuth,
        connection: &mut Connection,
    ) -> Result<(), GitHubError> {
        let tokens = connection
            .tokens
            .as_ref()
            .ok_or(GitHubError::Rejected(401))?;
        if tokens.issued_at.saturating_add(tokens.expires_in) <= now() + 60 {
            if tokens
                .issued_at
                .saturating_add(tokens.refresh_token_expires_in)
                <= now()
            {
                return Err(GitHubError::Rejected(401));
            }
            let replacement = oauth
                .exchange(
                    json!({"grant_type":"refresh_token","refresh_token":tokens.refresh_token}),
                )
                .await?;
            connection.tokens = Some(replacement);
            storage
                .put(SESSION_KEY, &*connection)
                .await
                .map_err(|_| GitHubError::Unavailable)?;
        }
        let tokens = connection
            .tokens
            .as_ref()
            .ok_or(GitHubError::Rejected(401))?;
        let user: User = github_json("https://api.github.com/user", &tokens.access_token).await?;
        if connection.user.as_ref().map(|u| u.id) != Some(user.id)
            || crate::github_app::GitAuthor::from_github(user.id, &user.login).is_err()
        {
            return Err(GitHubError::Rejected(401));
        }
        connection.user = Some(user);
        Ok(())
    }
    #[derive(Deserialize)]
    struct InstallationPage {
        installations: Vec<Installation>,
    }
    #[derive(Deserialize)]
    struct RepositoryPage {
        repositories: Vec<Repository>,
    }
    async fn installation(token: &str, app_id: i64, id: i64) -> Result<(), GitHubError> {
        // ponytail: scan at most 100,000 installations; beyond that refuse,
        // then replace with a GitHub direct user-installation lookup if added.
        for page in 1..=1000 {
            let response: InstallationPage = github_json(
                &format!("https://api.github.com/user/installations?per_page=100&page={page}"),
                token,
            )
            .await?;
            if response
                .installations
                .iter()
                .any(|i| i.id == id && i.active(app_id))
            {
                return Ok(());
            }
            if response.installations.len() < 100 {
                break;
            }
        }
        Err(GitHubError::Rejected(401))
    }
    async fn authorize(
        token: &str,
        app_id: i64,
        delegated: &Delegation,
        write: bool,
    ) -> Result<(), GitHubError> {
        installation(token, app_id, delegated.installation_id).await?;
        // The intersection endpoint proves this precise repository still belongs
        // to this user's installation. A repository-visible App token cannot.
        for page in 1..=1000 {
            let response: RepositoryPage = github_json(&format!("https://api.github.com/user/installations/{}/repositories?per_page=100&page={page}",delegated.installation_id),token).await?;
            if let Some(repository) = response
                .repositories
                .iter()
                .find(|r| r.id == delegated.repository_id)
            {
                return repository
                    .allows(delegated, write)
                    .then_some(())
                    .ok_or(GitHubError::Rejected(401));
            }
            if response.repositories.len() < 100 {
                break;
            }
        }
        Err(GitHubError::Rejected(401))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn confirmation_expiry_and_attempt_limit_are_closed_boundaries() {
        let mut confirmation = Confirmation {
            code_digest: digest("0123456789"),
            expires_at: 600,
            attempts: 0,
        };
        assert!(!confirmation.expired(599));
        assert!(confirmation.expired(600));
        confirmation.attempts = 4;
        assert!(!confirmation.expired(0));
        confirmation.attempts = 5;
        assert!(confirmation.expired(0));
    }
    #[test]
    fn read_only_visibility_never_grants_publication_and_repository_ids_bind_access() {
        let delegation = Delegation {
            installation_id: 1,
            repository_id: 2,
            repository: "team/repo".into(),
        };
        let mut repository = Repository {
            id: 2,
            full_name: "team/repo".into(),
            permissions: Permissions {
                pull: true,
                push: false,
                maintain: false,
                admin: false,
            },
        };
        assert!(repository.allows(&delegation, false));
        assert!(!repository.allows(&delegation, true));
        repository.permissions.push = true;
        assert!(repository.allows(&delegation, true));
        repository.id = 3;
        assert!(!repository.allows(&delegation, false));
        let mut installation = Installation {
            id: 1,
            app_id: 4,
            repository_selection: "selected".into(),
            suspended_at: None,
            html_url: "https://github.com/settings/installations/1".into(),
            account: User {
                id: 5,
                login: "alice".into(),
                account_type: "User".into(),
            },
        };
        assert!(installation.active(4));
        installation.suspended_at = Some("now".into());
        assert!(!installation.active(4));
        assert_eq!(tool_write("publish_commit"), Some(true));
        assert_eq!(tool_write("unknown"), None);
    }
}
